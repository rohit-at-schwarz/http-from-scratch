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
		handler = DefaultRouter
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
			//return early since there is nothing more left to process
			if errors.Is(err, io.EOF) {
				return nil
			}

			httpErr, ok := errors.AsType[*HTTPError](err)
			if ok {
				respondWithError(conn, httpErr.StatusCode)
				return nil
			}

			respondWithError(conn, http.StatusBadRequest)
			return nil
		}

		// fmt.Printf("Request parsed as: %s %s %s\n", req.Method, req.URL.Path, req.Proto)
		// fmt.Printf("Host received: %s\n", req.Host)
		// fmt.Println("Headers received:")
		// for k, v := range req.Header {
		// 	fmt.Printf("\t%s: %v\n", k, v)
		// }

		status, ok := validateHTTPVersion(req.Proto)
		if !ok {
			respondWithError(conn, status)
			return nil //close connection after 4xx/5xx protocol error
		}

		response := NewResponse(conn)
		mux.ServeHTTP(response, req)

		if _, err := io.Copy(io.Discard, req.Body); err != nil {
			respondWithError(conn, http.StatusBadRequest)
			return nil
		}

		//we need to flush the response to the wire with a proper framing of the Content-Length
		//after the handler is done executing to not have a deadlock between the client
		//and the server where the client waits for EOF, but the server waits for
		//the next request from the client, and they keep waiting forever on each others input
		err = response.complete(req.Method)
		if err != nil {
			return fmt.Errorf("finish response: %w", err)
		}

		if !shouldKeepConnAlive(req.Proto, req.Header) {
			break
		}
	}

	return nil
}

func respondWithError(conn net.Conn, statusCode int) {
	resp := NewResponse(conn)
	resp.Header().Set("Connection", "close")
	resp.WriteHeader(statusCode)
	_ = resp.complete(http.MethodGet)
}
