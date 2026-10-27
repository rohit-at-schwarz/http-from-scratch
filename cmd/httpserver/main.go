package main

import (
	"fmt"
	"httpServer/internal/myHttp"
	"net/http"
)

const addr = ":8080"

func main() {
	//todo: find out why this is here, seems pointless
	//turns out that this registers the route in a global router, so if we do not
	//pass in a custom handler for our server, then the implementation in server.go
	//defines the listener to be http.DefaultServeMux so our server does the listening
	//parsing and forwarding and then the global router takes that parsed request and
	//does what it needs to do
	http.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello")
	})

	store := NewUserStore()
	_, _ = store.Add("Alice")
	_, _ = store.Add("Bob")
	http.HandleFunc("/users", usersHandler(store))

	if err := myHttp.ListenAndServe(addr, nil); err != nil {
		panic(err)
	}
}
