package http1

import (
	"fmt"
	"net"
	"net/http"
)

func ListenAndServe(addr string, handler http.Handler) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()

	if handler == nil {
		handler = http.DefaultServeMux
	}

	for {
		conn, err := listener.Accept()
		if err != nil {
			return fmt.Errorf("connection: %w", err)
		}

		//since this is a indefinite loop, we dont want to
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
	defer conn.Close()

	buffer := make([]byte, 1024)
	n, err := conn.Read(buffer)
	if err != nil {
		return fmt.Errorf("read %w", err)
	}

	fmt.Println("\nBuffer slice (dec):")
	fmt.Println(buffer[:n])

	fmt.Println("\nBuffer slice (hex):")
	fmt.Printf("% x\n", buffer[:n])

	fmt.Println("\nBuffer slice (string)")
	fmt.Print(string(buffer[:n]))

	return nil

	/*
			Functions that might be useful:
			* ParseRequest()
		  * mux.ServeHTTP()
	*/
}
