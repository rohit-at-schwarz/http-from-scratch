package main

import (
	"net/http"
	"strings"
	"time"

	"httpServer/internal/myHttp"
)

func RouteLabel(r *http.Request) string {
	path := r.URL.Path

	switch {
	case path == "/hello":
		return "/hello"
	case path == "/health":
		return "/health"
	case path == "/metrics":
		return "/metrics"
	case path == "/users":
		return "/users"
	case strings.HasPrefix(path, "/users/"):
		return "/users/:user"
	default:
		return "unmatched"
	}
}

func MetricsMiddleware(collector *myHttp.MetricsCollector, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		interceptor := myHttp.NewResponseWriterInterceptor(w)

		next.ServeHTTP(interceptor, r)

		duration := time.Since(start)
		method := myHttp.MethodLabel(r.Method)
		route := RouteLabel(r)

		collector.Observe(method, route, interceptor.StatusCode, duration)
	})
}

func MetricsHandler(collector *myHttp.MetricsCollector) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		collector.WritePrometheus(w)
	}
}
