package myHttp

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func stubHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}
}

func TestRouter_Routing(t *testing.T) {
	r := NewRouter()

	r.HandleFunc("/hello", stubHandler("exact hello"))
	r.HandleFunc("/files/", stubHandler("files subtree"))
	r.HandleFunc("/files/images/", stubHandler("nested images subtree"))
	r.HandleFunc("/", stubHandler("root fallback"))

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "exact match",
			path:       "/hello",
			wantStatus: http.StatusOK,
			wantBody:   "exact hello",
		},
		{
			name:       "exact match does not match subtree",
			path:       "/hello/world",
			wantStatus: http.StatusOK,
			wantBody:   "root fallback", // /hello is not a subtree, so it falls back to /
		},
		{
			name:       "subtree match",
			path:       "/files/doc.pdf",
			wantStatus: http.StatusOK,
			wantBody:   "files subtree",
		},
		{
			name:       "longest prefix match wins",
			path:       "/files/images/logo.png",
			wantStatus: http.StatusOK,
			wantBody:   "nested images subtree",
		},
		{
			name:       "empty path normalizes to root",
			path:       "",
			wantStatus: http.StatusOK,
			wantBody:   "root fallback",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://example.com"+tc.path, nil)
			rec := httptest.NewRecorder()

			r.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if rec.Body.String() != tc.wantBody {
				t.Errorf("body = %q, want %q", rec.Body.String(), tc.wantBody)
			}
		})
	}
}

func TestRouter_NotFound(t *testing.T) {
	r := NewRouter()
	r.HandleFunc("/exact", stubHandler("ok"))

	req := httptest.NewRequest(http.MethodGet, "/unregistered", nil)
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestRouter_RegistrationPanics(t *testing.T) {
	tests := []struct {
		name string
		fn   func(r *Router)
	}{
		{
			name: "pattern missing leading slash",
			fn: func(r *Router) {
				r.HandleFunc("no-slash", stubHandler("ok"))
			},
		},
		{
			name: "nil handler to Handle",
			fn: func(r *Router) {
				r.Handle("/valid", nil)
			},
		},
		{
			name: "nil handler func to HandleFunc",
			fn: func(r *Router) {
				r.HandleFunc("/valid", nil)
			},
		},
		{
			name: "duplicate route registration",
			fn: func(r *Router) {
				r.HandleFunc("/duplicate", stubHandler("first"))
				r.HandleFunc("/duplicate", stubHandler("second"))
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRouter()

			defer func() {
				if r := recover(); r == nil {
					t.Fatalf("expected panic, but function ran without panicking")
				}
			}()

			tc.fn(r)
		})
	}
}
