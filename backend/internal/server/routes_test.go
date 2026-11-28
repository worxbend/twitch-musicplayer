package server

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5/middleware"

	"lofi-radio-backend/internal/api"
	"lofi-radio-backend/internal/models"
	"lofi-radio-backend/internal/player"
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

// headTestServer serves the full router over a real listener, so net/http
// drops HEAD bodies as it does in production. The library holds one song,
// "s", whose file is the 10-byte fixture a.mp3.
func headTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	musicDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(musicDir, "a.mp3"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	songs := repository.NewMemSongRepo()
	if err := songs.Create(context.Background(), &models.Song{ID: "s", Path: "a.mp3", Filename: "a.mp3"}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(buildRoutes(Options{
		Songs:  api.NewSongsHandler(songs, musicDir),
		Player: api.NewPlayerHandler(nil, nil, nil, player.NewStateTracker(), "{artist} - {title}"),
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHeadOnGetRoutes(t *testing.T) {
	srv := headTestServer(t)

	tests := []struct {
		name, path  string
		wantStatus  int
		wantHeaders map[string]string
	}{
		{name: "song list", path: "/v1/songs", wantStatus: http.StatusOK},
		{name: "history", path: "/v1/history", wantStatus: http.StatusOK},
		{name: "now playing text", path: "/v1/now-playing.txt", wantStatus: http.StatusOK},
		{
			name: "song content", path: "/v1/songs/s/content", wantStatus: http.StatusOK,
			wantHeaders: map[string]string{"Content-Length": "10", "Accept-Ranges": "bytes"},
		},
		{name: "unknown song content", path: "/v1/songs/missing/content", wantStatus: http.StatusNotFound},
		{name: "post-only queue route", path: "/v1/queue:skip", wantStatus: http.StatusMethodNotAllowed},
		{name: "post-only player route", path: "/v1/player:skip", wantStatus: http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := srv.Client().Head(srv.URL + tt.path)
			if err != nil {
				t.Fatalf("HEAD %s: %v", tt.path, err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}

			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if len(body) != 0 {
				t.Fatalf("body = %q, want empty", body)
			}
			for name, want := range tt.wantHeaders {
				if got := resp.Header.Get(name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
		})
	}
}
