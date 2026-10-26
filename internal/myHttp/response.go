package myHttp

import (
	"fmt"
	"net"
	"net/http"
)

type Response struct {
	conn   net.Conn
	header http.Header
}

func NewResponse(conn net.Conn) *Response {
	return &Response{
		conn:   conn,
		header: make(http.Header),
	}
}

func (r *Response) Header() http.Header {
	return r.header
}

// the fmt.Fprintf internally calls this implicitly in the handlers
func (r *Response) Write(data []byte) (n int, err error) {
	r.WriteHeader(http.StatusOK)
	return r.conn.Write(data)
}

// A response can look something like this:
//
//	HTTP/1.1 200 OK\r\n
//	Content-Type: text/plain\r\n
//	Content-Length: 5\r\n
//	\r\n
//	hello
func (r *Response) WriteHeader(statusCode int) {
	protocol := "HTTP/1.1"
	statusText := http.StatusText(statusCode)
	fmt.Fprintf(r.conn, "%s %d %s\r\n", protocol, statusCode, statusText)

	if len(r.header) == 0 {
		fmt.Println("WriteHeader: r.header is empty")
	}
	for k, v := range r.header {
		for _, v := range v {
			fmt.Fprintf(r.conn, "%s: %s\r\n", k, v)
		}
	}

	fmt.Fprintf(r.conn, "\r\n")
}
