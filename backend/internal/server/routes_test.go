package server

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5/middleware"
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
