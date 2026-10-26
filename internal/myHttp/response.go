package myHttp

import (
	"net"
	"net/http"
)

type Response struct {
}

func NewResponse(conn net.Conn) *Response {
	panic("NewResponse: Not Implemented")
}

func (r *Response) Header() http.Header {
	panic("Response.Header: Not Implemented")
}

func (r *Response) Write(data []byte) (n int, err error) {
	panic("Response.Write: Not Implemented")
}

func (r *Response) WriteHeader(statusCode int) {
	panic("Response.WriteHeader: Not Implemented")
}
