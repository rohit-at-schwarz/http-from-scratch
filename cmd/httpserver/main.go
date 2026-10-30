package main

import (
	"fmt"
	"httpServer/internal/myHttp"
	"net/http"
)

const addr = ":8080"

func main() {
	myHttp.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello")
	})

	store := NewUserStore()
	_, _ = store.Add("Alice")
	_, _ = store.Add("Bob")
	myHttp.HandleFunc("/users", usersCollectionHandler(store))
	myHttp.HandleFunc("/users/", userDetailHandler(store))

	myHttp.HandleFunc("/health", healthHandler())

	collector := myHttp.NewMetricsCollector()
	myHttp.HandleFunc("/metrics", MetricsHandler(collector))

	if err := myHttp.ListenAndServe(addr, nil); err != nil {
		panic(err)
	}
}
