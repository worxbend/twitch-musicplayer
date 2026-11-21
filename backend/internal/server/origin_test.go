package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ws "github.com/gorilla/websocket"

	"lofi-radio-backend/internal/models"
	"lofi-radio-backend/internal/player"
	"lofi-radio-backend/internal/websocket"
)

const (
	frontendOrigin = "http://localhost:3000"
	foreignOrigin  = "https://evil.example"
)

type idleQueueManager struct{}

func (idleQueueManager) NextSong(context.Context) (*models.Song, error)         { return nil, nil }
func (idleQueueManager) ListQueue(context.Context) ([]*models.QueueItem, error) { return nil, nil }
func (idleQueueManager) TotalSongs(context.Context) (int, error)                { return 0, nil }
func (idleQueueManager) Requeue(context.Context, string) error                  { return nil }

func originTestRoutes() http.Handler {
	hub := websocket.NewHub()
	go hub.Run()
	return buildRoutes(Options{
		Hub:            hub,
		QueueMgr:       idleQueueManager{},
		StateTracker:   player.NewStateTracker(),
		BaseURL:        "http://localhost:8080/radio",
		AllowedOrigins: []string{frontendOrigin},
	})
}

func TestWebSocketUpgradeChecksOrigin(t *testing.T) {
	srv := httptest.NewServer(originTestRoutes())
	t.Cleanup(srv.Close)
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	tests := []struct {
		name, origin string
		wantStatus   int
	}{
		{name: "foreign page", origin: foreignOrigin, wantStatus: http.StatusForbidden},
		{name: "frontend page", origin: frontendOrigin, wantStatus: http.StatusSwitchingProtocols},
		{name: "non-browser client", origin: "", wantStatus: http.StatusSwitchingProtocols},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := http.Header{}
			if tt.origin != "" {
				header.Set("Origin", tt.origin)
			}
			conn, resp, err := ws.DefaultDialer.Dial(url, header)
			if conn != nil {
				conn.Close()
			}
			if resp == nil {
				t.Fatalf("dial: no response (%v)", err)
			}
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d (%v), want %d", resp.StatusCode, err, tt.wantStatus)
			}
		})
	}
}

func preflight(routes http.Handler, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodOptions, "/v1/queue", nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, req)
	return rec
}

func TestCORSAllowsOnlyConfiguredOrigins(t *testing.T) {
	routes := originTestRoutes()

	if got := preflight(routes, foreignOrigin).Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("foreign preflight Access-Control-Allow-Origin = %q, want none", got)
	}
	rec := preflight(routes, frontendOrigin)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != frontendOrigin {
		t.Errorf("frontend preflight Access-Control-Allow-Origin = %q, want %q", got, frontendOrigin)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want none", got)
	}
}

// A no-cors POST from any page still reaches the server: CORS only hides the
// response. Writes from a foreign page must be refused before the handler.
func TestForeignOriginWritesAreRejected(t *testing.T) {
	tests := []struct {
		name, method, origin string
		wantReached          bool
	}{
		{name: "foreign post", method: http.MethodPost, origin: foreignOrigin},
		{name: "foreign delete", method: http.MethodDelete, origin: foreignOrigin},
		{name: "frontend post", method: http.MethodPost, origin: frontendOrigin, wantReached: true},
		{name: "own swagger page post", method: http.MethodPost, origin: "http://localhost:8080", wantReached: true},
		{name: "non-browser post", method: http.MethodPost, wantReached: true},
		{name: "foreign get", method: http.MethodGet, origin: foreignOrigin, wantReached: true},
	}
	allow := newOriginPolicy([]string{frontendOrigin}, "http://localhost:8080/radio")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })
			req := httptest.NewRequest(tt.method, "/v1/queue:clear", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			rec := httptest.NewRecorder()
			rejectForeignWrites(allow)(next).ServeHTTP(rec, req)

			if reached != tt.wantReached {
				t.Fatalf("handler reached = %v, want %v", reached, tt.wantReached)
			}
			if !tt.wantReached && rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
			}
		})
	}
}
