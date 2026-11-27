package server

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5/middleware"

	"lofi-radio-backend/internal/api"
	"lofi-radio-backend/internal/repository"
)

func serveHealth(t *testing.T, method string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	buildRoutes(Options{}).ServeHTTP(rec, httptest.NewRequest(method, "/health", nil))
	return rec
}

func TestHealthGet(t *testing.T) {
	rec := serveHealth(t, http.MethodGet)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != `{"status":"up"}` {
		t.Fatalf("body = %q, want {\"status\":\"up\"}", body)
	}
}

func TestHealthHead(t *testing.T) {
	rec := serveHealth(t, http.MethodHead)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty", rec.Body.String())
	}
}

func TestHealthRejectsPost(t *testing.T) {
	rec := serveHealth(t, http.MethodPost)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

// captureRequestLog points chi's DefaultLogger at a buffer for one test.
// It mutates a package global, so tests using it must not run in parallel.
func captureRequestLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := middleware.DefaultLogger
	middleware.DefaultLogger = middleware.RequestLogger(&middleware.DefaultLogFormatter{
		Logger:  log.New(&buf, "", 0),
		NoColor: true,
	})
	t.Cleanup(func() { middleware.DefaultLogger = prev })
	return &buf
}

func TestHealthIsNotLogged(t *testing.T) {
	buf := captureRequestLog(t)
	routes := buildRoutes(Options{})

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		routes.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, "/health", nil))
	}

	if buf.Len() != 0 {
		t.Fatalf("health probes were logged: %q", buf.String())
	}
}

func TestOtherRoutesAreLogged(t *testing.T) {
	buf := captureRequestLog(t)

	buildRoutes(Options{}).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil))

	if !strings.Contains(buf.String(), "/openapi.yaml") {
		t.Fatalf("request log = %q, want an /openapi.yaml entry", buf.String())
	}
}

// Every response, including errors raised before routing, must stop browsers
// from sniffing a content type.
func TestEveryResponseSendsNoSniff(t *testing.T) {
	routes := buildRoutes(Options{
		Songs:          api.NewSongsHandler(repository.NewMemSongRepo(), t.TempDir()),
		BaseURL:        "http://localhost:8080",
		AllowedOrigins: []string{frontendOrigin},
	})
	preflight := func(r *http.Request) {
		r.Header.Set("Origin", frontendOrigin)
		r.Header.Set("Access-Control-Request-Method", http.MethodGet)
	}
	foreign := func(r *http.Request) { r.Header.Set("Origin", foreignOrigin) }

	tests := []struct {
		name, method, path string
		prepare            func(*http.Request)
		wantStatus         int
	}{
		{name: "health get", method: http.MethodGet, path: "/health", wantStatus: http.StatusOK},
		{name: "health head", method: http.MethodHead, path: "/health", wantStatus: http.StatusOK},
		{name: "openapi spec", method: http.MethodGet, path: "/openapi.yaml", wantStatus: http.StatusOK},
		{name: "swagger page", method: http.MethodGet, path: "/swagger", wantStatus: http.StatusOK},
		{name: "json api", method: http.MethodGet, path: "/v1/songs", wantStatus: http.StatusOK},
		{name: "not found", method: http.MethodGet, path: "/no-such-path", wantStatus: http.StatusNotFound},
		{name: "cors preflight", method: http.MethodOptions, path: "/v1/songs", prepare: preflight, wantStatus: http.StatusOK},
		{name: "foreign write", method: http.MethodPost, path: "/v1/queue", prepare: foreign, wantStatus: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.prepare != nil {
				tt.prepare(req)
			}
			rec := httptest.NewRecorder()
			routes.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
			}
		})
	}
}
