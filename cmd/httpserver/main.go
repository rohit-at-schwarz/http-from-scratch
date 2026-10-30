package main

import (
	"context"
	"fmt"
	"httpServer/internal/myHttp"
	"net/http"
	"os/signal"
	"syscall"
	"time"
)

const addr = ":8080"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := setupTracing(ctx)
	if err != nil {
		panic(err)
	}

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

	handler := TracingMiddleware(MetricsMiddleware(collector, myHttp.DefaultRouter))

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- myHttp.ListenAndServe(addr, handler)
	}()

	select {
	case err = <-serveErr:
	case <-ctx.Done():
	}

	flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if flushErr := shutdownTracing(flushCtx); flushErr != nil {
		fmt.Printf("flush traces: %v\n", flushErr)
	}

	if err != nil {
		panic(err)
	}
}
