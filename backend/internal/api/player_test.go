package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"

	"lofi-radio-backend/internal/models"
	"lofi-radio-backend/internal/player"
	"lofi-radio-backend/internal/websocket"
)

// fakeCounter satisfies both playerSongRepo and playerQueueRepo.
type fakeCounter struct {
	n   int
	err error
}

func (f fakeCounter) Count(context.Context) (int, error) { return f.n, f.err }

func playerRouter(songs, queue fakeCounter, hub *fakeBroadcaster, st *player.StateTracker) http.Handler {
	return RegisterRoutesHTTP(nil, nil, NewPlayerHandler(songs, queue, hub, st, "{artist} - {title}"))
}

func TestPlayerState(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name        string
		songs       fakeCounter
		queue       fakeCounter
		current     *models.Song
		wantCode    int
		wantCurrent bool
	}{
		{name: "playing", songs: fakeCounter{n: 12}, queue: fakeCounter{n: 3}, current: &models.Song{ID: "c", Title: "Rain"}, wantCode: http.StatusOK, wantCurrent: true},
		{name: "idle omits current_song", songs: fakeCounter{n: 12}, queue: fakeCounter{n: 3}, wantCode: http.StatusOK},
		{name: "song count error", songs: fakeCounter{err: boom}, wantCode: http.StatusInternalServerError},
		{name: "queue count error", songs: fakeCounter{n: 1}, queue: fakeCounter{err: boom}, wantCode: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := player.NewStateTracker()
			if tt.current != nil {
				st.SetCurrentSong(tt.current)
			}
			rec := serve(t, playerRouter(tt.songs, tt.queue, &fakeBroadcaster{}, st), http.MethodGet, "/v1/player", "", nil)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if tt.wantCode != http.StatusOK {
				return
			}
			var got map[string]any
			decodeBody(t, rec, &got)
			if got["total_songs"] != float64(12) || got["queue_length"] != float64(3) {
				t.Fatalf("counts = %v", got)
			}
			cur, hasCurrent := got["current_song"].(map[string]any)
			if hasCurrent != tt.wantCurrent {
				t.Fatalf("current_song present = %v, want %v (%v)", hasCurrent, tt.wantCurrent, got)
			}
			if hasCurrent && cur["id"] != "c" {
				t.Fatalf("current_song = %v", cur)
			}
		})
	}
}

func TestPlayerNowPlayingText(t *testing.T) {
	tests := []struct {
		name    string
		format  string
		current *models.Song
		want    string
	}{
		{name: "artist and title", format: "{artist} - {title}", current: &models.Song{Title: "Rain", Artist: "Nujabes"}, want: "Nujabes - Rain"},
		{name: "no artist renders title only", format: "{artist} - {title}", current: &models.Song{Title: "Rain"}, want: "Rain"},
		{name: "idle renders empty body", format: "{artist} - {title}"},
		{name: "custom format with album", format: "{title} ({album})", current: &models.Song{Title: "Rain", Artist: "Nujabes", Album: "Modal Soul"}, want: "Rain (Modal Soul)"},
		{name: "newline in title becomes space", format: "{artist} - {title}", current: &models.Song{Title: "Rain\nDrops", Artist: "Nujabes"}, want: "Nujabes - Rain Drops"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := player.NewStateTracker()
			if tt.current != nil {
				st.SetCurrentSong(tt.current)
			}
			h := NewPlayerHandler(fakeCounter{}, fakeCounter{}, &fakeBroadcaster{}, st, tt.format)
			rec := serve(t, RegisterRoutesHTTP(nil, nil, h), http.MethodGet, "/v1/now-playing.txt", "", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Body.String(); got != tt.want {
				t.Errorf("body = %q, want %q", got, tt.want)
			}
			if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
				t.Errorf("Content-Type = %q", got)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q", got)
			}
		})
	}
}

func TestPlayerSkipBroadcastsSkipNow(t *testing.T) {
	hub := &fakeBroadcaster{}
	rec := serve(t, playerRouter(fakeCounter{}, fakeCounter{}, hub, player.NewStateTracker()), http.MethodPost, "/v1/player:skip", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(hub.msgs) != 1 || hub.msgs[0].Type != websocket.MsgSkipNow {
		t.Fatalf("broadcasts = %v, want one %s", hub.types(), websocket.MsgSkipNow)
	}
}

func TestPlayerHistory(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		songs     int
		wantCode  int
		wantTotal int
	}{
		{name: "empty", target: "/v1/history", wantCode: http.StatusOK},
		{name: "default limit", target: "/v1/history", songs: 3, wantCode: http.StatusOK, wantTotal: 3},
		{name: "limit 2", target: "/v1/history?limit=2", songs: 3, wantCode: http.StatusOK, wantTotal: 2},
		{name: "limit clamped", target: "/v1/history?limit=999", songs: player.MaxHistory + 5, wantCode: http.StatusOK, wantTotal: player.MaxHistory},
		{name: "non-integer limit", target: "/v1/history?limit=abc", wantCode: http.StatusBadRequest},
		{name: "zero limit", target: "/v1/history?limit=0", wantCode: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := player.NewStateTracker()
			for i := range tt.songs {
				st.SetCurrentSong(&models.Song{ID: strconv.Itoa(i)})
			}
			rec := serve(t, playerRouter(fakeCounter{}, fakeCounter{}, &fakeBroadcaster{}, st), http.MethodGet, tt.target, "", nil)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.wantCode, rec.Body.String())
			}
			if tt.wantCode != http.StatusOK {
				var body map[string]string
				decodeBody(t, rec, &body)
				if body["error"] != "limit must be a positive integer" {
					t.Fatalf("error body = %v", body)
				}
				return
			}
			var got struct {
				Items []map[string]any `json:"items"`
				Total *int             `json:"total"`
			}
			decodeBody(t, rec, &got)
			if got.Items == nil || got.Total == nil || *got.Total != tt.wantTotal || len(got.Items) != tt.wantTotal {
				t.Fatalf("body = %s, want %d items", rec.Body.String(), tt.wantTotal)
			}
			for i, item := range got.Items {
				song, _ := item["song"].(map[string]any)
				if want := strconv.Itoa(tt.songs - 1 - i); song["id"] != want {
					t.Fatalf("items[%d].song.id = %v, want %s (newest first)", i, song["id"], want)
				}
				if _, ok := item["started_at"].(string); !ok {
					t.Fatalf("items[%d] missing started_at: %v", i, item)
				}
			}
		})
	}
}
