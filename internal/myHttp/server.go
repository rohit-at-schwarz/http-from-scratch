package myHttp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
)

func ListenAndServe(addr string, handler http.Handler) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer func() {
		err = listener.Close()
		if err != nil {
			_ = fmt.Errorf("close listener: %w", err)
		}
	}()

	if handler == nil {
		handler = http.DefaultServeMux
	}

	for {
		conn, err := listener.Accept()
		if err != nil {
			return fmt.Errorf("connection: %w", err)
		}

		//since this is a while(true) loop, we dont want to
		//block the main processor if one client takes too long
		//so spin up a go routine for each connection so the
		//main server stays active
		go func() {
			err := HandleConnection(conn, handler)
			if err != nil {
				fmt.Printf("Error handling connection: %v\n", err)
			}
		}()
	}
}

func HandleConnection(conn net.Conn, mux http.Handler) error {
	defer func(conn net.Conn) {
		err := conn.Close()
		if err != nil {
			_ = fmt.Errorf("close connection: %w", err)
		}
	}(conn)

	reader := bufio.NewReader(conn)

	for {
		req, err := ParseRequest(reader)
		if err != nil {
			//if EOF signal has been received, we can return
			//early from here since there is nothing more to
			//process
			if errors.Is(err, io.EOF) {
				return nil
			}

			return fmt.Errorf("parse request: %w", err)
		}

		fmt.Printf("Request parsed as: %s %s %s\n", req.Method, req.URL.Path, req.Proto)
		fmt.Printf("Host received: %s\n", req.Host)
		fmt.Println("Headers received:")
		for k, v := range req.Header {
			fmt.Printf("\t%s: %v\n", k, v)
		}

		response := NewResponse(conn)
		mux.ServeHTTP(response, req)

		if !shouldKeepConnAlive(req.Proto, req.Header) {
			break
		}
	}

	return nil
}
