// Tests for the HTTP/1.1 server side of HandleConnection, per RFC 9112.
//
// These tests define the expected contract for HandleConnection:
//
//   - HandleConnection reads consecutive requests from one connection and
//     services each of them with the provided handler, keeping the connection
//     alive (HTTP/1.1 keep-alive semantics, RFC 9112 §9).
//   - Keep-alive is the default for HTTP/1.1. For HTTP/1.0 the connection is
//     closed after the response unless the request carries
//     "Connection: keep-alive" (RFC 9112 §9.2).
//   - A request carrying "Connection: close" is answered, then the connection
//     is closed (RFC 9112 §9.1).
//   - Malformed requests (bad request line, invalid methods/targets/header
//     field names or values, unterminated or truncated messages, ...) are
//     answered with 400 Bad Request and the connection is closed, because
//     the stream can no longer be re-synchronised (RFC 9112 §3.2, §6.1).
//   - Requests with a syntactically valid but unsupported version
//     (e.g. HTTP/2.0) are answered with 505 HTTP Version Not Supported and
//     the connection is closed (RFC 9112 §2, §9.3).
//   - The request body is framed by Content-Length or Transfer-Encoding:
//     chunked (RFC 9112 §6.1). If both are present, Transfer-Encoding takes
//     precedence.
//   - Keep-alive responses with a body MUST be framed (Content-Length or
//     chunked); close-delimited bodies are only valid together with
//     "Connection: close".
//   - HandleConnection returns nil when the connection ends cleanly, either
//     because the client disconnected (EOF) or because the server closed it
//     intentionally after sending a response.
package http1

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// In-memory network connection used as test infrastructure.
//
// net.Pipe is NOT suitable here: it is unbuffered (writes block until the
// peer reads, which deadlocks for "Expect: 100-continue" style exchanges
// where the server writes before it has finished reading) and it does not
// honour deadlines. The connection below is buffered, supports half-close
// (CloseWrite) so that "request truncated, then EOF" cases can be tested,
// and is protected by timeouts in the test helpers instead of deadlines.
// ---------------------------------------------------------------------------

// errWriteClosed is returned by Write after the write side of the pipe has
// been closed (either by CloseWrite or Close).
var errWriteClosed = errors.New("http1 test: write on closed connection")

// onePipe is a one-directional, unbounded, buffered byte pipe with
// half-close support. Exactly one goroutine writes to it and exactly one
// goroutine reads from it; the mutex protects the shared state.
type onePipe struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	closed bool // writer has closed the pipe; readers drain and get io.EOF
}

func newOnePipe() *onePipe {
	p := &onePipe{}
	p.cond = sync.NewCond(&p.mu)
	return p
}

// write appends b to the buffer. It never blocks and returns errWriteClosed
// if the pipe was closed by the writer.
func (p *onePipe) write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, errWriteClosed
	}
	p.buf = append(p.buf, b...)
	p.cond.Broadcast()
	return len(b), nil
}

// read reads up to len(b) bytes, blocking until data is available. It
// returns io.EOF once the pipe has been closed and fully drained.
func (p *onePipe) read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.buf) == 0 && !p.closed {
		p.cond.Wait()
	}
	if len(p.buf) == 0 { // closed and drained
		return 0, io.EOF
	}
	n := copy(b, p.buf)
	p.buf = p.buf[n:]
	return n, nil
}

// closeWriter marks the pipe as closed: readers will drain remaining data
// and then receive io.EOF, and further writes fail.
func (p *onePipe) closeWriter() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.cond.Broadcast()
}

// memConn is a buffered, full-duplex in-memory net.Conn.
type memConn struct {
	in  *onePipe // data written by the peer, read by this end
	out *onePipe // data written by this end, read by the peer

	mu     sync.Mutex
	closed bool // Close() was called on this end
}

// newMemPipe returns two connected in-memory connections: what is written to
// a can be read from b and vice versa.
func newMemPipe() (a, b *memConn) {
	aToB := newOnePipe() // a writes, b reads
	bToA := newOnePipe() // b writes, a reads
	a = &memConn{in: bToA, out: aToB}
	b = &memConn{in: aToB, out: bToA}
	return a, b
}

func (c *memConn) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, net.ErrClosed
	}
	c.mu.Unlock()
	return c.in.read(b)
}

func (c *memConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, net.ErrClosed
	}
	c.mu.Unlock()
	return c.out.write(b)
}

// CloseWrite closes only the write side: the peer's Read will return the
// remaining data and then io.EOF. The read side stays open.
func (c *memConn) CloseWrite() error {
	c.out.closeWriter()
	return nil
}

// Close fully closes this end: both directions are terminated, so the peer's
// Read gets EOF (after draining) and the peer's Write fails.
func (c *memConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	c.out.closeWriter()
	c.in.closeWriter()
	return nil
}

func (c *memConn) LocalAddr() net.Addr  { return memAddr{} }
func (c *memConn) RemoteAddr() net.Addr { return memAddr{} }
func (c *memConn) SetDeadline(time.Time) error {
	return nil // accepted and ignored; tests use external timeouts
}
func (c *memConn) SetReadDeadline(time.Time) error  { return nil }
func (c *memConn) SetWriteDeadline(time.Time) error { return nil }

type memAddr struct{}

func (memAddr) Network() string { return "mem" }
func (memAddr) String() string  { return "mem" }

// ---------------------------------------------------------------------------
// Test harness.
// ---------------------------------------------------------------------------

// guard runs f in a goroutine and returns its result, failing the test if f
// does not return within timeout. Because the in-memory connection is
// buffered (so writes never block) the only operations that can hang are the
// blocking reads against a server that never responds; guard converts such a
// hang into a clear test failure instead of a stuck test suite.
func guard[T any](t *testing.T, timeout time.Duration, f func() (T, error)) (T, error) {
	t.Helper()
	type result struct {
		v T
		e error
	}
	ch := make(chan result, 1)
	go func() {
		v, e := f()
		ch <- result{v, e}
	}()
	select {
	case r := <-ch:
		return r.v, r.e
	case <-time.After(timeout):
		t.Fatalf("operation did not complete within %v (server likely hung)", timeout)
		var zero T
		return zero, nil
	}
}

// testResponse is the parsed form of one final (non-1xx) HTTP response.
type testResponse struct {
	status        int
	statusLine    string
	header        http.Header
	body          []byte
	informational []int // status codes of any 1xx responses seen first
}

// serverTest wires an in-memory connection to HandleConnection (running in a
// background goroutine) and exposes helpers to drive it from the test.
type serverTest struct {
	t       *testing.T
	client  *memConn
	server  *memConn
	br      *bufio.Reader
	errCh   <-chan error
	timeout time.Duration

	// captured/capErr remember a value already consumed from errCh by the
	// fail-fast check in readFinalResponse, so waitForErr and
	// assertStillOpen can still observe the server's exit. They are only
	// accessed from the test goroutine.
	captured bool
	capErr   error
}

// testTimeout bounds every blocking read in the tests. With a correct
// implementation every operation completes in microseconds (the connection
// is in-memory), so the timeout only guards against a hung server.
const testTimeout = 2 * time.Second

// newServerTest starts HandleConnection on one end of an in-memory pipe and
// returns a handle for the other end. The HandleConnection error is captured
// in errCh (a panic is converted into an error so the suite fails cleanly
// while the server is still unimplemented).
func newServerTest(t *testing.T, mux http.Handler) *serverTest {
	t.Helper()
	client, server := newMemPipe()
	errCh := make(chan error, 1)
	go func() {
		defer func() {
			// Closing the server end turns any client read that is still
			// blocked into an immediate EOF, so a panicked or otherwise
			// exited server can never hang the test suite.
			server.Close()
			if r := recover(); r != nil {
				errCh <- fmt.Errorf("HandleConnection panicked: %v", r)
			}
		}()
		errCh <- HandleConnection(server, mux)
	}()
	st := &serverTest{
		t:       t,
		client:  client,
		server:  server,
		br:      bufio.NewReader(client),
		errCh:   errCh,
		timeout: testTimeout,
	}
	// Closing both ends unblocks any goroutine stuck in a blocking read.
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	return st
}

// writeRaw writes data to the server. The connection is buffered, so this
// does not block.
func (st *serverTest) writeRaw(t *testing.T, data string) {
	t.Helper()
	if _, err := st.client.Write([]byte(data)); err != nil {
		t.Fatalf("writing to server: %v", err)
	}
}

// exchange writes a full request and reads back one final response.
func (st *serverTest) exchange(t *testing.T, request, method string) testResponse {
	t.Helper()
	st.writeRaw(t, request)
	return st.readFinalResponse(t, method)
}

// readFinalResponse reads responses from the connection, collecting any 1xx
// informational responses, and returns the first final (non-1xx) response.
func (st *serverTest) readFinalResponse(t *testing.T, method string) testResponse {
	t.Helper()
	// Fail fast if HandleConnection has already exited with an error: in
	// that case no valid response can be pending. A nil exit means the server
	// closed deliberately (e.g. after a "Connection: close" response), so the
	// response may still be readable from the buffer and we continue.
	select {
	case err := <-st.errCh:
		st.captured = true
		st.capErr = err
		if err != nil {
			t.Fatalf("HandleConnection returned %v before sending a response", err)
		}
	default:
	}
	req := &http.Request{Method: method}
	var info []int
	for {
		resp, err := guard(t, st.timeout, func() (*http.Response, error) {
			return http.ReadResponse(st.br, req)
		})
		if err != nil {
			t.Fatalf("reading response: %v", err)
		}
		body, err := guard(t, st.timeout, func() ([]byte, error) {
			b, e := io.ReadAll(resp.Body)
			resp.Body.Close()
			return b, e
		})
		if err != nil {
			t.Fatalf("reading response body: %v", err)
		}
		if resp.StatusCode/100 == 1 {
			info = append(info, resp.StatusCode)
			continue
		}
		return testResponse{
			status:        resp.StatusCode,
			statusLine:    resp.Status,
			header:        resp.Header,
			body:          body,
			informational: info,
		}
	}
}

// waitForErr blocks until HandleConnection returns, failing the test if it
// does not within the timeout. It returns an error that was already captured
// by readFinalResponse's fail-fast check without re-blocking.
func (st *serverTest) waitForErr(t *testing.T) error {
	t.Helper()
	if st.captured {
		return st.capErr
	}
	select {
	case err := <-st.errCh:
		return err
	case <-time.After(st.timeout):
		t.Fatalf("HandleConnection did not return within %v", st.timeout)
		return nil
	}
}

// assertStillOpen fails the test if HandleConnection has already returned,
// i.e. the server closed a connection it should have kept open.
func (st *serverTest) assertStillOpen(t *testing.T) {
	t.Helper()
	if st.captured {
		t.Fatalf("HandleConnection returned (connection closed) but keep-alive was expected: %v", st.capErr)
	}
	select {
	case err := <-st.errCh:
		t.Fatalf("HandleConnection returned (connection closed) but keep-alive was expected: %v", err)
	default:
	}
}

// closeClient performs a half-close of the client's write side, which the
// server observes as end-of-input (EOF).
func (st *serverTest) closeClient() {
	st.client.CloseWrite()
}

// newTestHandler returns an http.Handler whose responses are derived from the
// parsed request, so asserting on the response also asserts that the server
// parsed the request correctly.
func newTestHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/hello":
			_, _ = w.Write([]byte("hello"))
		case r.URL.Path == "/echo":
			_, _ = io.Copy(w, r.Body)
		case r.URL.Path == "/upper":
			b, _ := io.ReadAll(r.Body)
			_, _ = w.Write([]byte(strings.ToUpper(string(b))))
		case r.URL.Path == "/headers":
			w.Header().Set("X-Test", "yes")
			_, _ = w.Write([]byte("headers set"))
		case strings.HasPrefix(r.URL.Path, "/status/"):
			code, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/status/"))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(code)
			_, _ = fmt.Fprintf(w, "status %d", code)
		default:
			http.NotFound(w, r)
		}
	})
}

// ---------------------------------------------------------------------------
// Tests for the in-memory connection (test infrastructure self-check).
// ---------------------------------------------------------------------------

func TestMemConn(t *testing.T) {
	t.Run("WriteRead round trip", func(t *testing.T) {
		a, b := newMemPipe()
		defer a.Close()
		defer b.Close()

		if n, err := a.Write([]byte("hello world")); err != nil || n != 11 {
			t.Fatalf("Write = %d, %v; want 11, nil", n, err)
		}
		buf := make([]byte, 5)
		if n, err := b.Read(buf); err != nil || string(buf[:n]) != "hello" {
			t.Fatalf("Read1 = %q, %d, %v; want %q", buf[:n], n, err, "hello")
		}
		if n, err := b.Read(buf); err != nil || string(buf[:n]) != " worl" {
			t.Fatalf("Read2 = %q, %d, %v; want %q", buf[:n], n, err, " worl")
		}
		if n, err := b.Read(buf); err != nil || string(buf[:n]) != "d" {
			t.Fatalf("Read3 = %q, %d, %v; want %q", buf[:n], n, err, "d")
		}
	})

	t.Run("both directions", func(t *testing.T) {
		a, b := newMemPipe()
		defer a.Close()
		defer b.Close()

		a.Write([]byte("from-a"))
		b.Write([]byte("from-b"))
		buf := make([]byte, 16)
		if n, _ := b.Read(buf); string(buf[:n]) != "from-a" {
			t.Fatalf("b.Read = %q, want %q", buf[:n], "from-a")
		}
		if n, _ := a.Read(buf); string(buf[:n]) != "from-b" {
			t.Fatalf("a.Read = %q, want %q", buf[:n], "from-b")
		}
	})

	t.Run("CloseWrite yields EOF after draining", func(t *testing.T) {
		a, b := newMemPipe()
		defer a.Close()
		defer b.Close()

		a.Write([]byte("tail"))
		if err := a.CloseWrite(); err != nil {
			t.Fatalf("CloseWrite: %v", err)
		}
		buf := make([]byte, 16)
		if n, err := b.Read(buf); err != nil || string(buf[:n]) != "tail" {
			t.Fatalf("Read = %q, %v; want %q", buf[:n], err, "tail")
		}
		if _, err := b.Read(buf); err != io.EOF {
			t.Fatalf("second Read err = %v, want io.EOF", err)
		}
		// Writes on the still-open read side keep working for the peer.
		if _, err := b.Write([]byte("ok")); err != nil {
			t.Fatalf("b.Write after a.CloseWrite: %v", err)
		}
	})

	t.Run("Close makes further operations fail", func(t *testing.T) {
		a, b := newMemPipe()
		a.Write([]byte("x"))
		if err := a.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		buf := make([]byte, 16)
		if n, err := b.Read(buf); err != nil || string(buf[:n]) != "x" {
			t.Fatalf("drain Read = %q, %v; want %q", buf[:n], err, "x")
		}
		if _, err := b.Read(buf); err != io.EOF {
			t.Fatalf("Read after Close err = %v, want io.EOF", err)
		}
		if _, err := b.Write([]byte("y")); err == nil {
			t.Fatal("b.Write after a.Close: want error, got nil")
		}
		if _, err := a.Read(buf); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("a.Read after Close err = %v, want net.ErrClosed", err)
		}
		if _, err := a.Write([]byte("z")); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("a.Write after Close err = %v, want net.ErrClosed", err)
		}
	})
}

// ---------------------------------------------------------------------------
// HandleConnection: single valid requests.
// ---------------------------------------------------------------------------

func TestHandleConnection_SingleRequest(t *testing.T) {
	tests := []struct {
		name       string
		request    string
		method     string
		wantStatus int
		wantBody   string // checked only when checkBody is true
		checkBody  bool
		wantHeader [2]string // optional "Key", "Value" pair to check; empty key = skip
		// keepAlive is true when the server must keep the connection open
		// after the response; false when it must close it and return.
		keepAlive bool
	}{
		{
			name:       "GET path",
			request:    "GET /hello HTTP/1.1\r\nHost: example.com\r\n\r\n",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
			wantBody:   "hello",
			checkBody:  true,
			keepAlive:  true,
		},
		{
			name:       "GET with query string",
			request:    "GET /hello?name=ge HTTP/1.1\r\nHost: example.com\r\n\r\n",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
			wantBody:   "hello",
			checkBody:  true,
			keepAlive:  true,
		},
		{
			name:       "absolute-form target",
			request:    "GET http://example.com/hello HTTP/1.1\r\nHost: example.com\r\n\r\n",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
			wantBody:   "hello",
			checkBody:  true,
			keepAlive:  true,
		},
		{
			name:       "POST with Content-Length body",
			request:    "POST /echo HTTP/1.1\r\nHost: example.com\r\nContent-Length: 4\r\n\r\nping",
			method:     http.MethodPost,
			wantStatus: http.StatusOK,
			wantBody:   "ping",
			checkBody:  true,
			keepAlive:  true,
		},
		{
			name:       "HEAD has no body",
			request:    "HEAD /hello HTTP/1.1\r\nHost: example.com\r\n\r\n",
			method:     http.MethodHead,
			wantStatus: http.StatusOK,
			keepAlive:  true,
		},
		{
			name:       "unknown path is 404",
			request:    "GET /nope HTTP/1.1\r\nHost: example.com\r\n\r\n",
			method:     http.MethodGet,
			wantStatus: http.StatusNotFound,
			keepAlive:  true,
		},
		{
			name:       "handler sets status code",
			request:    "GET /status/201 HTTP/1.1\r\nHost: example.com\r\n\r\n",
			method:     http.MethodGet,
			wantStatus: http.StatusCreated,
			wantBody:   "status 201",
			checkBody:  true,
			keepAlive:  true,
		},
		{
			name:       "handler sets response header",
			request:    "GET /headers HTTP/1.1\r\nHost: example.com\r\n\r\n",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
			wantBody:   "headers set",
			checkBody:  true,
			wantHeader: [2]string{"X-Test", "yes"},
			keepAlive:  true,
		},
		{
			name:       "lowercase method token is valid",
			request:    "get /hello HTTP/1.1\r\nHost: example.com\r\n\r\n",
			method:     "get",
			wantStatus: http.StatusOK,
			wantBody:   "hello",
			checkBody:  true,
			keepAlive:  true,
		},
		{
			name:       "lowercase field names are valid",
			request:    "GET /hello HTTP/1.1\r\nhost: example.com\r\nx-foo: bar\r\n\r\n",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
			wantBody:   "hello",
			checkBody:  true,
			keepAlive:  true,
		},
		{
			name:       "empty field value is valid",
			request:    "GET /hello HTTP/1.1\r\nHost: example.com\r\nX-Empty:\r\n\r\n",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
			wantBody:   "hello",
			checkBody:  true,
			keepAlive:  true,
		},
		{
			name:       "HTTP/1.1 with Connection: close closes after response",
			request:    "GET /hello HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
			wantBody:   "hello",
			checkBody:  true,
			keepAlive:  false,
		},
		{
			name:       "HTTP/1.0 closes by default",
			request:    "GET /hello HTTP/1.0\r\nHost: example.com\r\n\r\n",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
			wantBody:   "hello",
			checkBody:  true,
			keepAlive:  false,
		},
		{
			name:       "HTTP/1.0 with Connection: keep-alive stays open",
			request:    "GET /hello HTTP/1.0\r\nHost: example.com\r\nConnection: keep-alive\r\n\r\n",
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
			wantBody:   "hello",
			checkBody:  true,
			keepAlive:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newServerTest(t, newTestHandler())
			resp := st.exchange(t, tc.request, tc.method)

			if resp.status != tc.wantStatus {
				t.Fatalf("status = %d (%s), want %d", resp.status, resp.statusLine, tc.wantStatus)
			}
			if tc.checkBody && string(resp.body) != tc.wantBody {
				t.Fatalf("body = %q, want %q", resp.body, tc.wantBody)
			}
			if tc.wantHeader[0] != "" {
				if got := resp.header.Get(tc.wantHeader[0]); got != tc.wantHeader[1] {
					t.Fatalf("header %s = %q, want %q", tc.wantHeader[0], got, tc.wantHeader[1])
				}
			}

			if tc.keepAlive {
				st.assertStillOpen(t)
				st.closeClient()
				if err := st.waitForErr(t); err != nil {
					t.Fatalf("HandleConnection returned %v after clean client disconnect, want nil", err)
				}
			} else {
				if err := st.waitForErr(t); err != nil {
					t.Fatalf("HandleConnection returned %v after server-initiated close, want nil", err)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// HandleConnection: multiple requests on one connection (keep-alive).
// ---------------------------------------------------------------------------

func TestHandleConnection_KeepAlive(t *testing.T) {
	tests := []struct {
		name       string
		requests   []string
		methods    []string
		wantStatus []int
		wantBody   []string // checked only for indices listed in checkBody
		checkBody  []bool
	}{
		{
			name: "two sequential GETs",
			requests: []string{
				"GET /hello HTTP/1.1\r\nHost: example.com\r\n\r\n",
				"GET /status/202 HTTP/1.1\r\nHost: example.com\r\n\r\n",
			},
			methods:    []string{http.MethodGet, http.MethodGet},
			wantStatus: []int{http.StatusOK, http.StatusAccepted},
			wantBody:   []string{"hello", "status 202"},
			checkBody:  []bool{true, true},
		},
		{
			name: "mixed methods with bodies",
			requests: []string{
				"GET /hello HTTP/1.1\r\nHost: example.com\r\n\r\n",
				"POST /echo HTTP/1.1\r\nHost: example.com\r\nContent-Length: 3\r\n\r\nabc",
				"GET /hello HTTP/1.1\r\nHost: example.com\r\n\r\n",
			},
			methods:    []string{http.MethodGet, http.MethodPost, http.MethodGet},
			wantStatus: []int{http.StatusOK, http.StatusOK, http.StatusOK},
			wantBody:   []string{"hello", "abc", "hello"},
			checkBody:  []bool{true, true, true},
		},
		{
			name:     "many requests on one connection",
			requests: repeatedRequests(5, "GET /hello HTTP/1.1\r\nHost: example.com\r\n\r\n"),
			methods:  repeatedMethods(5, http.MethodGet),
			wantStatus: func() []int {
				s := make([]int, 5)
				for i := range s {
					s[i] = http.StatusOK
				}
				return s
			}(),
			wantBody:  nil,
			checkBody: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newServerTest(t, newTestHandler())
			for i, req := range tc.requests {
				resp := st.exchange(t, req, tc.methods[i])
				if resp.status != tc.wantStatus[i] {
					t.Fatalf("request %d: status = %d (%s), want %d", i, resp.status, resp.statusLine, tc.wantStatus[i])
				}
				if tc.checkBody != nil && tc.checkBody[i] && string(resp.body) != tc.wantBody[i] {
					t.Fatalf("request %d: body = %q, want %q", i, resp.body, tc.wantBody[i])
				}
			}
			// All requests were served, so the connection must still be open.
			st.assertStillOpen(t)
			st.closeClient()
			if err := st.waitForErr(t); err != nil {
				t.Fatalf("HandleConnection returned %v after clean client disconnect, want nil", err)
			}
		})
	}
}

func repeatedRequests(n int, s string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

func repeatedMethods(n int, m string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = m
	}
	return out
}

// ---------------------------------------------------------------------------
// HandleConnection: HTTP version handling.
// ---------------------------------------------------------------------------

func TestHandleConnection_VersionHandling(t *testing.T) {
	tests := []struct {
		name       string
		request    string
		wantStatus int
		// keepAlive is true when the version is supported and the request does
		// not request close; false when the server must close and return.
		keepAlive bool
	}{
		{name: "HTTP/1.1 supported", request: "GET /hello HTTP/1.1\r\nHost: example.com\r\n\r\n", wantStatus: http.StatusOK, keepAlive: true},
		{name: "HTTP/1.0 supported, closes by default", request: "GET /hello HTTP/1.0\r\nHost: example.com\r\n\r\n", wantStatus: http.StatusOK, keepAlive: false},
		{name: "HTTP/2.0 unsupported", request: "GET /hello HTTP/2.0\r\nHost: example.com\r\n\r\n", wantStatus: http.StatusHTTPVersionNotSupported, keepAlive: false},
		{name: "HTTP/3.0 unsupported", request: "GET /hello HTTP/3.0\r\nHost: example.com\r\n\r\n", wantStatus: http.StatusHTTPVersionNotSupported, keepAlive: false},
		{name: "HTTP/1.2 unsupported", request: "GET /hello HTTP/1.2\r\nHost: example.com\r\n\r\n", wantStatus: http.StatusHTTPVersionNotSupported, keepAlive: false},
		{name: "HTTP/99.99 unsupported", request: "GET /hello HTTP/99.99\r\nHost: example.com\r\n\r\n", wantStatus: http.StatusHTTPVersionNotSupported, keepAlive: false},
		{name: "HTTP/0.9 unsupported", request: "GET /hello HTTP/0.9\r\nHost: example.com\r\n\r\n", wantStatus: http.StatusHTTPVersionNotSupported, keepAlive: false},
		{name: "version missing minor is malformed", request: "GET /hello HTTP/1\r\nHost: example.com\r\n\r\n", wantStatus: http.StatusBadRequest, keepAlive: false},
		{name: "version with extra component is malformed", request: "GET /hello HTTP/1.1.1\r\nHost: example.com\r\n\r\n", wantStatus: http.StatusBadRequest, keepAlive: false},
		{name: "lowercase version scheme is malformed", request: "GET /hello http/1.1\r\nHost: example.com\r\n\r\n", wantStatus: http.StatusBadRequest, keepAlive: false},
		{name: "non-numeric version is malformed", request: "GET /hello HTTP/x.y\r\nHost: example.com\r\n\r\n", wantStatus: http.StatusBadRequest, keepAlive: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newServerTest(t, newTestHandler())
			resp := st.exchange(t, tc.request, http.MethodGet)
			if resp.status != tc.wantStatus {
				t.Fatalf("status = %d (%s), want %d", resp.status, resp.statusLine, tc.wantStatus)
			}
			if tc.keepAlive {
				st.assertStillOpen(t)
				st.closeClient()
				if err := st.waitForErr(t); err != nil {
					t.Fatalf("HandleConnection returned %v, want nil", err)
				}
			} else {
				if err := st.waitForErr(t); err != nil {
					t.Fatalf("HandleConnection returned %v after server-initiated close, want nil", err)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// HandleConnection: corner cases (malformed and truncated requests).
//
// A request that cannot be re-synchronised must be answered with 400 and the
// connection closed (RFC 9112 §3.2). The same applies to a request that is
// truncated by EOF (half-closed client write side below).
// ---------------------------------------------------------------------------

func TestHandleConnection_CornerCases(t *testing.T) {
	// oversized is 64 KiB, beyond any sane request-line / header limit.
	oversized := strings.Repeat("a", 64<<10)
	manyHeaders := make([]string, 0, 10000)
	for i := 0; i < 10000; i++ {
		manyHeaders = append(manyHeaders, fmt.Sprintf("X-Header-%d: v", i))
	}

	tests := []struct {
		name string
		data string
		// closeAfter makes the client half-close after writing data, so the
		// server observes EOF (truncated message).
		closeAfter bool
		wantStatus int
		// wantStatusAny, when non-empty, means the status must be one of these.
		wantStatusAny []int
		wantBody      string
		checkBody     bool
		// keepAlive true = valid corner case: server must keep the connection.
		keepAlive bool
	}{
		{
			name:       "space in method",
			data:       "GE T / HTTP/1.1\r\n\r\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "tab in method",
			data:       "GE\tT / HTTP/1.1\r\n\r\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "NUL byte in method",
			data:       "GET\x00 / HTTP/1.1\r\n\r\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty method",
			data:       " / HTTP/1.1\r\n\r\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "request line is only whitespace",
			data:       "   \r\n\r\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing request target",
			data:       "GET HTTP/1.1\r\n\r\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing version",
			data:       "GET /\r\n\r\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing target and version",
			data:       "GET\r\n\r\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty request line",
			data:       "\r\n\r\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "space in request target",
			data:       "GET /a b HTTP/1.1\r\n\r\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "bare LF line endings",
			data:       "GET / HTTP/1.1\n\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "bare CR line endings",
			data:       "GET / HTTP/1.1\r\r",
			closeAfter: true,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "field name starting with colon",
			data:       "GET / HTTP/1.1\r\n:foo: bar\r\n\r\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "space in field name",
			data:       "GET / HTTP/1.1\r\nbad header: x\r\n\r\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid character in field name",
			data:       "GET / HTTP/1.1\r\nf%o: bar\r\n\r\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "tab in field value",
			data:       "GET / HTTP/1.1\r\nX-Bad: has\ttab\r\n\r\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "bare CR in field value",
			data:       "GET / HTTP/1.1\r\nX-Bad: a\rb\r\n\r\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:          "request line too long",
			data:          strings.Repeat("A", 64<<10) + " / HTTP/1.1\r\n\r\n",
			wantStatusAny: []int{http.StatusBadRequest, http.StatusRequestURITooLong},
		},
		{
			name:          "header line too long",
			data:          "GET / HTTP/1.1\r\nX-Long: " + oversized + "\r\n\r\n",
			wantStatusAny: []int{http.StatusBadRequest, http.StatusRequestHeaderFieldsTooLarge},
		},
		{
			name:          "too many headers",
			data:          "GET / HTTP/1.1\r\n" + strings.Join(manyHeaders, "\r\n") + "\r\n\r\n",
			wantStatusAny: []int{http.StatusBadRequest, http.StatusRequestHeaderFieldsTooLarge},
		},
		{
			name:       "request line truncated by EOF",
			data:       "GET",
			closeAfter: true,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "request line missing terminating CRLF",
			data:       "GET / HTTP/1.1",
			closeAfter: true,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "headers truncated by EOF",
			data:       "GET / HTTP/1.1\r\nHost: example.com\r\n",
			closeAfter: true,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "body shorter than Content-Length",
			data:       "POST /echo HTTP/1.1\r\nHost: example.com\r\nContent-Length: 10\r\n\r\nshort",
			closeAfter: true,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "chunked body truncated by EOF",
			data:       "POST /echo HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello",
			closeAfter: true,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid chunk size",
			data:       "POST /echo HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: chunked\r\n\r\nzz\r\n\r\n",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "conflicting Content-Length values",
			data:       "POST /echo HTTP/1.1\r\nHost: example.com\r\nContent-Length: 5\r\nContent-Length: 3\r\n\r\nabcde",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "identical duplicate Content-Length is valid",
			data:       "POST /echo HTTP/1.1\r\nHost: example.com\r\nContent-Length: 5\r\nContent-Length: 5\r\n\r\nhello",
			wantStatus: http.StatusOK,
			wantBody:   "hello",
			checkBody:  true,
			keepAlive:  true,
		},
		{
			name:       "Transfer-Encoding takes precedence over Content-Length",
			data:       "POST /echo HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\nTransfer-Encoding: chunked\r\n\r\n1\r\na\r\n0\r\n\r\n",
			wantStatus: http.StatusOK,
			wantBody:   "a",
			checkBody:  true,
			keepAlive:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newServerTest(t, newTestHandler())
			st.writeRaw(t, tc.data)
			if tc.closeAfter {
				st.closeClient()
			}
			resp := st.readFinalResponse(t, http.MethodGet)

			switch {
			case len(tc.wantStatusAny) > 0:
				ok := false
				for _, s := range tc.wantStatusAny {
					if resp.status == s {
						ok = true
						break
					}
				}
				if !ok {
					t.Fatalf("status = %d (%s), want one of %v", resp.status, resp.statusLine, tc.wantStatusAny)
				}
			case resp.status != tc.wantStatus:
				t.Fatalf("status = %d (%s), want %d", resp.status, resp.statusLine, tc.wantStatus)
			}
			if tc.checkBody && string(resp.body) != tc.wantBody {
				t.Fatalf("body = %q, want %q", resp.body, tc.wantBody)
			}

			if tc.keepAlive {
				st.assertStillOpen(t)
				st.closeClient()
				if err := st.waitForErr(t); err != nil {
					t.Fatalf("HandleConnection returned %v, want nil", err)
				}
			} else {
				// The server must close the connection after a 4xx response.
				if err := st.waitForErr(t); err != nil {
					t.Fatalf("HandleConnection returned %v after server-initiated close, want nil", err)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// HandleConnection: request body framing (RFC 9112 §6.1, §7).
// ---------------------------------------------------------------------------

func TestHandleConnection_RequestBody(t *testing.T) {
	tests := []struct {
		name       string
		request    string
		method     string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "Content-Length body",
			request:    "POST /echo HTTP/1.1\r\nHost: example.com\r\nContent-Length: 11\r\n\r\nhello world",
			method:     http.MethodPost,
			wantStatus: http.StatusOK,
			wantBody:   "hello world",
		},
		{
			name:       "Content-Length zero body",
			request:    "POST /echo HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n",
			method:     http.MethodPost,
			wantStatus: http.StatusOK,
			wantBody:   "",
		},
		{
			name:       "no Content-Length and no Transfer-Encoding means no body",
			request:    "POST /echo HTTP/1.1\r\nHost: example.com\r\n\r\n",
			method:     http.MethodPost,
			wantStatus: http.StatusOK,
			wantBody:   "",
		},
		{
			name:       "chunked body",
			request:    "POST /echo HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n6\r\n world\r\n0\r\n\r\n",
			method:     http.MethodPost,
			wantStatus: http.StatusOK,
			wantBody:   "hello world",
		},
		{
			name:       "chunked body with chunk extensions",
			request:    "POST /echo HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: chunked\r\n\r\n5;ext=val\r\nhello\r\n0\r\n\r\n",
			method:     http.MethodPost,
			wantStatus: http.StatusOK,
			wantBody:   "hello",
		},
		{
			name:       "chunked body with trailers",
			request:    "POST /echo HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n0\r\nX-Check: done\r\n\r\n",
			method:     http.MethodPost,
			wantStatus: http.StatusOK,
			wantBody:   "hello",
		},
		{
			name:       "chunk size in uppercase hex",
			request:    "POST /echo HTTP/1.1\r\nHost: example.com\r\nTransfer-Encoding: chunked\r\n\r\nA\r\n0123456789\r\n0\r\n\r\n",
			method:     http.MethodPost,
			wantStatus: http.StatusOK,
			wantBody:   "0123456789",
		},
		{
			name:       "lowercase field name and CHUNKED value",
			request:    "POST /echo HTTP/1.1\r\nHost: example.com\r\ntransfer-encoding: CHUNKED\r\n\r\n1\r\na\r\n0\r\n\r\n",
			method:     http.MethodPost,
			wantStatus: http.StatusOK,
			wantBody:   "a",
		},
		{
			name:       "body is fully delivered to the handler",
			request:    "POST /upper HTTP/1.1\r\nHost: example.com\r\nContent-Length: 5\r\n\r\nhello",
			method:     http.MethodPost,
			wantStatus: http.StatusOK,
			wantBody:   "HELLO",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newServerTest(t, newTestHandler())
			resp := st.exchange(t, tc.request, tc.method)
			if resp.status != tc.wantStatus {
				t.Fatalf("status = %d (%s), want %d", resp.status, resp.statusLine, tc.wantStatus)
			}
			if string(resp.body) != tc.wantBody {
				t.Fatalf("body = %q, want %q", resp.body, tc.wantBody)
			}
			st.assertStillOpen(t)
			st.closeClient()
			if err := st.waitForErr(t); err != nil {
				t.Fatalf("HandleConnection returned %v, want nil", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// HandleConnection: Expect: 100-continue (RFC 9112 §10.2).
//
// The server may either send a 100 Continue before the final response or go
// straight to the final response; both are compliant. What is not compliant
// is failing to parse the body or breaking the connection.
// ---------------------------------------------------------------------------

func TestHandleConnection_ExpectContinue(t *testing.T) {
	st := newServerTest(t, newTestHandler())
	resp := st.exchange(t,
		"POST /echo HTTP/1.1\r\nHost: example.com\r\nContent-Length: 5\r\nExpect: 100-continue\r\n\r\nhello",
		http.MethodPost)

	if resp.status != http.StatusOK {
		t.Fatalf("status = %d (%s), want %d", resp.status, resp.statusLine, http.StatusOK)
	}
	if string(resp.body) != "hello" {
		t.Fatalf("body = %q, want %q", resp.body, "hello")
	}
	for _, code := range resp.informational {
		if code/100 != 1 {
			t.Fatalf("unexpected non-1xx informational status %d", code)
		}
	}
	st.assertStillOpen(t)
	st.closeClient()
	if err := st.waitForErr(t); err != nil {
		t.Fatalf("HandleConnection returned %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// HandleConnection: bytes left over after a complete request must be treated
// as the start of the next request. A well-formed server therefore answers
// the first request, then detects the trailing garbage as a malformed
// request and closes the connection.
// ---------------------------------------------------------------------------

func TestHandleConnection_LeftoverBodyBytes(t *testing.T) {
	st := newServerTest(t, newTestHandler())
	st.writeRaw(t, "POST /echo HTTP/1.1\r\nHost: example.com\r\nContent-Length: 5\r\n\r\nhelloGARBAGE\r\n\r\n")

	first := st.readFinalResponse(t, http.MethodPost)
	if first.status != http.StatusOK {
		t.Fatalf("first status = %d (%s), want %d", first.status, first.statusLine, http.StatusOK)
	}
	if string(first.body) != "hello" {
		t.Fatalf("first body = %q, want %q", first.body, "hello")
	}

	second := st.readFinalResponse(t, http.MethodGet)
	if second.status != http.StatusBadRequest {
		t.Fatalf("second status = %d (%s), want %d", second.status, second.statusLine, http.StatusBadRequest)
	}
	if err := st.waitForErr(t); err != nil {
		t.Fatalf("HandleConnection returned %v after server-initiated close, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// HandleConnection: a client that disconnects without sending anything must
// end the connection cleanly without an error and without a response.
// ---------------------------------------------------------------------------

func TestHandleConnection_EmptyConnection(t *testing.T) {
	st := newServerTest(t, newTestHandler())
	st.closeClient()
	if err := st.waitForErr(t); err != nil {
		t.Fatalf("HandleConnection returned %v for immediate client disconnect, want nil", err)
	}
}
