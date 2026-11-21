package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
