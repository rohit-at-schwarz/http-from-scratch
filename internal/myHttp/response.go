package myHttp

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"strconv"
)

type Response struct {
	conn           net.Conn
	header         http.Header
	didWriteHeader bool

	statusCode int
	body       bytes.Buffer
}

func NewResponse(conn net.Conn) *Response {
	return &Response{
		conn:       conn,
		header:     make(http.Header),
		statusCode: http.StatusOK,
	}
}

func (r *Response) Header() http.Header {
	return r.header
}

// the fmt.Fprintf internally calls this implicitly in the handlers
func (r *Response) Write(data []byte) (n int, err error) {
	r.WriteHeader(http.StatusOK)
	return r.body.Write(data)
}

// A response can look something like this:
//
//	HTTP/1.1 200 OK\r\n
//	Content-Type: text/plain\r\n
//	Content-Length: 5\r\n
//	\r\n
//	hello
func (r *Response) WriteHeader(statusCode int) {
	if r.didWriteHeader {
		return
	}

	r.didWriteHeader = true
	r.statusCode = statusCode
}

func (r *Response) complete(method string) error {
	r.WriteHeader(http.StatusOK)

	//responses on persistent connections must be framed
	if r.header.Get("Content-Length") == "" && r.header.Get("Transfer-Encoding") == "" {
		r.header.Set("Content-Length", strconv.Itoa(r.body.Len()))
	}

	statusText := http.StatusText(r.statusCode)
	_, err := fmt.Fprintf(r.conn, "%s %d %s\r\n", "HTTP/1.1", r.statusCode, statusText)
	if err != nil {
		return err
	}

	for k, vs := range r.header {
		for _, v := range vs {
			if _, err := fmt.Fprintf(r.conn, "%s: %s\r\n", k, v); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintf(r.conn, "\r\n"); err != nil {
		return err
	}

	//response to a HEAD request MUST NOT contain a message body.
	if method != http.MethodHead && r.body.Len() > 0 {
		if _, err := r.conn.Write(r.body.Bytes()); err != nil {
			return err
		}
	}

	return nil
}
