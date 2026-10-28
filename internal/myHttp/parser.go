package myHttp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

const (
	intentUnspecified = iota // 0: client didn't send a Connection header
	intentToKeepAlive        // 1: client explicitly sent "keep-alive"
	intentToClose            // 2: client explicitly sent "close"
)

const (
	maxRequestLineLen = 8192
	maxHeaderLineLen  = 8192
	maxHeaderCount    = 100
)

type chunkedBody struct {
	chunked     io.Reader
	src         *bufio.Reader
	trailerRead bool
}

func (b *chunkedBody) Read(p []byte) (int, error) {
	n, err := b.chunked.Read(p)
	if errors.Is(err, io.EOF) && !b.trailerRead {
		b.trailerRead = true
		if trailerErr := skipTrailerSection(b.src); trailerErr != nil {
			return n, trailerErr
		}
	}

	return n, err
}

func skipTrailerSection(src *bufio.Reader) error {
	for {
		ln, err := src.ReadString('\n')
		if err != nil {
			return io.ErrUnexpectedEOF
		}
		if !strings.HasSuffix(ln, "\r\n") {
			return errors.New("trailer line not terminated by CLRF")
		}

		if ln == "\r\n" {
			return nil
		}
	}
}

func shouldKeepConnAlive(protocol string, header http.Header) bool {
	intent := intentUnspecified

	// GET /hello HTTP/1.1
	// Host: example.com
	// Connection: keep-alive, close
	for _, v := range header.Values("Connection") {
		for part := range strings.SplitSeq(v, ",") {
			part = strings.TrimSpace(strings.ToLower(part))
			switch part {
			case "close":
				intent = intentToClose
			case "keep-alive":
				if intent != intentToClose {
					intent = intentToKeepAlive
				}
			}
		}
	}

	switch protocol {
	case "HTTP/1.1":
		return intent != intentToClose //stays open by default, unless the intent was to close
	case "HTTP/1.0":
		return intent == intentToKeepAlive //closes by default, unless the intent was to keep-alive
	default:
		return false
	}
}

func validateHTTPVersion(protocol string) (statusCode int, ok bool) {
	if !strings.HasPrefix(protocol, "HTTP/") {
		return http.StatusBadRequest, false
	}

	//HTTP/1.1, HTTP/1.0, HTTP/0.9, HTTP/3.0 ...
	ver := strings.TrimPrefix(protocol, "HTTP/")
	chunks := strings.Split(ver, ".")
	if len(chunks) != 2 {
		return http.StatusBadRequest, false
	}

	for _, chunk := range chunks {
		if len(chunk) == 0 {
			return http.StatusBadRequest, false
		}

		for i := 0; i < len(chunk); i++ {
			if chunk[i] < '0' || chunk[i] > '9' {
				return http.StatusBadRequest, false
			}
		}
	}

	major, _ := strconv.Atoi(chunks[0])
	minor, _ := strconv.Atoi(chunks[1])

	//we support HTTP/1.0 and HTTP/1.1 for now
	if (major == 1 && minor == 0) || (major == 1 && minor == 1) {
		return http.StatusOK, true
	}

	return http.StatusHTTPVersionNotSupported, false
}

func isToken(str string) bool {
	if str == "" {
		return false
	}

	for i := 0; i < len(str); i++ {
		c := str[i]
		isAlphaNumeric := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !isAlphaNumeric && strings.IndexByte("!#$%&*+-_.'^|~`", c) < 0 {
			return false
		}
	}

	return true
}

func isValidFieldValue(str string) bool {
	for i := 0; i < len(str); i++ {
		c := str[i]
		if (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}

	return true
}

func ParseRequest(reader *bufio.Reader) (*http.Request, error) {
	ln, err := reader.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) {
			//the client simply closed the connection so this isnt an issue
			if len(ln) == 0 {
				return nil, io.EOF
			}

			return nil, newHTTPError(http.StatusBadRequest, "truncated request ln")
		}

		return nil, fmt.Errorf("failed to read the request line: %w", err)
	}

	if len(ln) > maxRequestLineLen {
		return nil, newHTTPError(http.StatusRequestURITooLong, "request line too long")
	}

	//this will give us something like this 'GET /hello HTTP/1.1' from 'GET /hello HTTP/1.1\r\n'
	ln = strings.TrimRight(ln, "\r\n")
	subStrings := strings.Split(ln, " ")
	if len(subStrings) != 3 || subStrings[0] == "" || subStrings[1] == "" || subStrings[2] == "" {
		return nil, newHTTPError(http.StatusBadRequest, "malformed request line")
	}

	method, rawURI, protocol := subStrings[0], subStrings[1], subStrings[2]
	parsedURL, err := url.ParseRequestURI(rawURI)
	if err != nil {
		return nil, newHTTPError(http.StatusBadRequest, "invalid request URI")
	}

	request := &http.Request{
		Method:     method,
		URL:        parsedURL,
		Proto:      protocol,
		Header:     make(http.Header),
		RequestURI: rawURI,
	}

	//now that we have the first line parsed already, we can parse the rest of the lines
	//as well in a for loop, since all of them have the same structure of 1 entry per line
	//and a format of <key>: <val>, we can use deterministic parsing here to get all the headers
	headerCount := 0
	for {
		currHeaderLn, err := reader.ReadString('\n')
		if err != nil {
			return nil, newHTTPError(http.StatusBadRequest, "truncated headers")
		}

		if len(currHeaderLn) > maxHeaderLineLen {
			return nil, newHTTPError(http.StatusRequestHeaderFieldsTooLarge, "header line too long")
		}

		currHeaderLn = strings.TrimSuffix(currHeaderLn, "\r\n")
		if currHeaderLn == "" {
			//empty line means the header section is done (\r\n\r\n reached)
			break
		}

		headerCount++
		if headerCount > maxHeaderCount {
			return nil, newHTTPError(http.StatusRequestHeaderFieldsTooLarge, "too many headers")
		}

		idxCol := strings.IndexByte(currHeaderLn, ':')
		//field name cannot be empty, start with a colon, or contain spaces
		if idxCol <= 0 || !isToken(currHeaderLn[:idxCol]) {
			return nil, newHTTPError(http.StatusBadRequest, "malformed header field name")
		}

		currKey := strings.TrimSpace(currHeaderLn[0:idxCol])
		currVal := strings.Trim(currHeaderLn[idxCol+1:], " \t")
		if !isValidFieldValue(currVal) {
			return nil, newHTTPError(http.StatusBadRequest, "malformed header field value")
		}

		request.Header.Add(currKey, currVal)
	}

	if protocol == "HTTP/1.1" && len(request.Header.Values("Host")) == 0 {
		return nil, newHTTPError(http.StatusBadRequest, "missing Host Headers")
	}

	//apparently the router and any standard middleware expect req.Host to be
	//populated so they can route requests based on the domain
	request.Host = request.Header.Get("Host")

	clValues := request.Header.Values("Content-Length")
	if len(clValues) > 0 {
		firstVal := strings.TrimSpace(clValues[0])
		for _, v := range clValues[1:] {
			if strings.TrimSpace(v) != firstVal {
				return nil, newHTTPError(http.StatusBadRequest, "conflicting Content-Length values")
			}
		}
	}

	isChunked := false
	for _, v := range request.Header.Values("Transfer-Encoding") {
		for _, enc := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(enc), "chunked") {
				isChunked = true
				break
			}
		}
	}

	//Transfer-Encoding takes precedence over Content-Length
	if isChunked {
		request.TransferEncoding = []string{"chunked"}
		request.ContentLength = -1
		request.Body = io.NopCloser(&chunkedBody{chunked: httputil.NewChunkedReader(reader), src: reader})
	} else if len(clValues) > 0 {
		firstVal := strings.TrimSpace(clValues[0])
		cl, err := strconv.ParseInt(firstVal, 10, 64)
		if err != nil || cl < 0 {
			return nil, newHTTPError(http.StatusBadRequest, "invalid Content-Length value")
		}
		request.ContentLength = cl
		request.Body = io.NopCloser(io.LimitReader(reader, cl))
	} else {
		request.Body = http.NoBody
	}

	return request, nil
}
