package myHttp

import (
	"net/http"
	"strings"
	"sync"
)

type route struct {
	pattern string
	handler http.Handler
	subtree bool
}

type Router struct {
	routes []route
	mu     sync.Mutex
}

func NewRouter() *Router {
	return &Router{}
}

var DefaultRouter = NewRouter()

func Handle(pattern string, handler http.Handler) {
	DefaultRouter.Handle(pattern, handler)
}

func HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	DefaultRouter.HandleFunc(pattern, handler)
}

func (r *Router) Handle(pattern string, handler http.Handler) {
	if !strings.HasPrefix(pattern, "/") {
		panic("myHttp: pattern must start with a /: " + pattern)
	}

	if handler == nil {
		panic("myHttp: handler is nil for pattern: " + pattern)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, existing := range r.routes {
		if existing.pattern == pattern {
			panic("myHttp: more than 1 registrations detected for pattern: " + pattern)
		}
	}

	r.routes = append(r.routes, route{pattern: pattern, subtree: strings.HasSuffix(pattern, "/"), handler: handler})
}

func (r *Router) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	if handler == nil {
		panic("myHttp: handler is nil for pattern: " + pattern)
	}

	r.Handle(pattern, http.HandlerFunc(handler))
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	handler := r.match(req)
	if handler == nil {
		http.NotFound(w, req)
		return
	}

	handler.ServeHTTP(w, req)
}

func (r *Router) match(req *http.Request) http.Handler {
	path := req.URL.Path
	if path == "" {
		path = "/"
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	var match *route
	for i := range r.routes {
		candidate := &r.routes[i]
		matches := path == candidate.pattern || (candidate.subtree && strings.HasPrefix(path, candidate.pattern))
		if matches && (match == nil || len(candidate.pattern) > len(match.pattern)) {
			match = candidate
		}
	}

	if match == nil {
		return nil
	}

	return match.handler
}
