package api

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"lofi-radio-backend/internal/models"
	"lofi-radio-backend/internal/player"
	"lofi-radio-backend/internal/websocket"
)

// fakeCounter satisfies playerQueueRepo.
type fakeCounter struct {
	n   int
	err error
}

func (f fakeCounter) Count(context.Context) (int, error) { return f.n, f.err }

func playerRouter(mgr *fakeQueueManager, queue fakeCounter, hub *fakeBroadcaster, st *player.StateTracker) http.Handler {
	return RegisterRoutesHTTP(nil, nil, NewPlayerHandler(queue, mgr, hub, st, "{artist} - {title}"))
}

func TestPlayerState(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name        string
		mgr         *fakeQueueManager
		queue       fakeCounter
		current     *models.Song
		wantCode    int
		wantTotal   float64
		wantCurrent bool
		wantNext    string // "" means next_song is absent
	}{
		{name: "playing", mgr: &fakeQueueManager{totalSongs: 12}, queue: fakeCounter{n: 3}, current: &models.Song{ID: "c", Title: "Rain"}, wantCode: http.StatusOK, wantTotal: 12, wantCurrent: true},
		{name: "idle omits current_song", mgr: &fakeQueueManager{totalSongs: 12}, queue: fakeCounter{n: 3}, wantCode: http.StatusOK, wantTotal: 12},
		// A library of 3 with one song blocked: total_songs counts rotation only.
		{name: "blocked songs not counted", mgr: &fakeQueueManager{totalSongs: 2}, queue: fakeCounter{n: 3}, wantCode: http.StatusOK, wantTotal: 2},
		{name: "next song", mgr: &fakeQueueManager{totalSongs: 12, items: []*models.QueueItem{{ID: "q1", SongID: "gone"}, {ID: "q2", SongID: "n", Song: &models.Song{ID: "n"}}}}, queue: fakeCounter{n: 3}, wantCode: http.StatusOK, wantTotal: 12, wantNext: "n"},
		// next_song is optional, so a failed queue read omits it instead of a 500.
		{name: "queue list error omits next_song", mgr: &fakeQueueManager{totalSongs: 12, listErr: boom, items: []*models.QueueItem{{ID: "q2", SongID: "n", Song: &models.Song{ID: "n"}}}}, queue: fakeCounter{n: 3}, wantCode: http.StatusOK, wantTotal: 12},
		{name: "song count error", mgr: &fakeQueueManager{libraryErr: boom}, wantCode: http.StatusInternalServerError},
		{name: "queue count error", mgr: &fakeQueueManager{}, queue: fakeCounter{err: boom}, wantCode: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := player.NewStateTracker()
			if tt.current != nil {
				st.SetCurrentSong(tt.current)
			}
			rec := serve(t, playerRouter(tt.mgr, tt.queue, &fakeBroadcaster{}, st), http.MethodGet, "/v1/player", "", nil)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if tt.wantCode != http.StatusOK {
				return
			}
			var got map[string]any
			decodeBody(t, rec, &got)
			if got["total_songs"] != tt.wantTotal || got["queue_length"] != float64(3) {
				t.Fatalf("counts = %v", got)
			}
			cur, hasCurrent := got["current_song"].(map[string]any)
			if hasCurrent != tt.wantCurrent {
				t.Fatalf("current_song present = %v, want %v (%v)", hasCurrent, tt.wantCurrent, got)
			}
			if hasCurrent && cur["id"] != "c" {
				t.Fatalf("current_song = %v", cur)
			}
			next, hasNext := got["next_song"].(map[string]any)
			if hasNext != (tt.wantNext != "") || (hasNext && next["id"] != tt.wantNext) {
				t.Fatalf("next_song = %v, want id %q", got["next_song"], tt.wantNext)
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
			h := NewPlayerHandler(fakeCounter{}, &fakeQueueManager{}, &fakeBroadcaster{}, st, tt.format)
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

func TestPlayerNowPlayingTextIdle(t *testing.T) {
	playing := &models.Song{Title: "Rain", Artist: "Nujabes"}
	tests := []struct {
		name     string
		current  *models.Song
		query    string
		wantCode int
		want     string
	}{
		{name: "playing ignores idle", current: playing, query: "?idle=x", wantCode: http.StatusOK, want: "Nujabes - Rain"},
		{name: "idle text while idle", query: "?idle=Radio%20is%20idle", wantCode: http.StatusOK, want: "Radio is idle"},
		{name: "no idle param keeps empty body", wantCode: http.StatusOK},
		{name: "CR LF flattened and trimmed", query: "?idle=%0D%0Aoff%0D%0Aair%0A", wantCode: http.StatusOK, want: "off  air"},
		{name: "tab flattened", query: "?idle=a%09b", wantCode: http.StatusOK, want: "a b"},
		{name: "NUL flattened", query: "?idle=a%00b", wantCode: http.StatusOK, want: "a b"},
		{name: "leading ESC flattened and trimmed", query: "?idle=%1B%5B31mx", wantCode: http.StatusOK, want: "[31mx"},
		{name: "C1 NEL flattened", query: "?idle=a%C2%85b", wantCode: http.StatusOK, want: "a b"},
		{name: "line separator flattened", query: "?idle=a%E2%80%A8b", wantCode: http.StatusOK, want: "a b"},
		{name: "only tabs keeps empty body", query: "?idle=%09%09", wantCode: http.StatusOK},
		{name: "200 multibyte runes accepted", query: "?idle=" + url.QueryEscape(strings.Repeat("é", 200)), wantCode: http.StatusOK, want: strings.Repeat("é", 200)},
		{name: "201 runes rejected", query: "?idle=" + strings.Repeat("a", 201), wantCode: http.StatusBadRequest},
		{name: "201 runes rejected while playing", current: playing, query: "?idle=" + strings.Repeat("a", 201), wantCode: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := player.NewStateTracker()
			if tt.current != nil {
				st.SetCurrentSong(tt.current)
			}
			h := NewPlayerHandler(fakeCounter{}, &fakeQueueManager{}, &fakeBroadcaster{}, st, "{artist} - {title}")
			rec := serve(t, RegisterRoutesHTTP(nil, nil, h), http.MethodGet, "/v1/now-playing.txt"+tt.query, "", nil)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.wantCode, rec.Body.String())
			}
			if tt.wantCode != http.StatusOK {
				var body map[string]string
				decodeBody(t, rec, &body)
				if body["error"] != "idle must be at most 200 characters" {
					t.Fatalf("error body = %v", body)
				}
				return
			}
			if got := rec.Body.String(); got != tt.want {
				t.Errorf("body = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPlayerSkip(t *testing.T) {
	playable := &models.QueueItem{ID: "q1", SongID: "n", Song: &models.Song{ID: "n"}}
	// dead is an entry whose song left the library: ListQueue reports Song=nil.
	dead := &models.QueueItem{ID: "q2", SongID: "gone"}
	tests := []struct {
		name          string
		mgr           *fakeQueueManager
		wantCode      int
		wantBody      map[string]string
		wantBroadcast bool
	}{
		{name: "manual_only, empty queue", mgr: &fakeQueueManager{}, wantCode: http.StatusConflict, wantBody: map[string]string{"error": "queue is empty"}},
		{name: "manual_only, only dead entries", mgr: &fakeQueueManager{items: []*models.QueueItem{dead}}, wantCode: http.StatusConflict, wantBody: map[string]string{"error": "queue is empty"}},
		{name: "manual_only, playable entry", mgr: &fakeQueueManager{items: []*models.QueueItem{dead, playable}}, wantCode: http.StatusOK, wantBody: map[string]string{"message": "skip broadcast"}, wantBroadcast: true},
		{name: "auto-select, empty queue", mgr: &fakeQueueManager{autoSelects: true}, wantCode: http.StatusOK, wantBody: map[string]string{"message": "skip broadcast"}, wantBroadcast: true},
		{name: "list error", mgr: &fakeQueueManager{autoSelects: true, listErr: errors.New("boom")}, wantCode: http.StatusInternalServerError, wantBody: map[string]string{"error": "failed to skip current song"}},
		{name: "auto-select, empty queue, empty library", mgr: &fakeQueueManager{autoSelects: true, emptyLibrary: true}, wantCode: http.StatusConflict, wantBody: map[string]string{"error": "no songs available"}},
		{name: "auto-select, library count error", mgr: &fakeQueueManager{autoSelects: true, libraryErr: errors.New("boom")}, wantCode: http.StatusInternalServerError, wantBody: map[string]string{"error": "failed to skip current song"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := &fakeBroadcaster{}
			h := NewPlayerHandler(fakeCounter{}, tt.mgr, hub, player.NewStateTracker(), "{artist} - {title}")
			rec := serve(t, RegisterRoutesHTTP(nil, nil, h), http.MethodPost, "/v1/player:skip", "", nil)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.wantCode, rec.Body.String())
			}
			var body map[string]string
			decodeBody(t, rec, &body)
			if !maps.Equal(body, tt.wantBody) {
				t.Fatalf("body = %v, want %v", body, tt.wantBody)
			}
			wantMsgs := 0
			if tt.wantBroadcast {
				wantMsgs = 1
			}
			if len(hub.msgs) != wantMsgs || (wantMsgs == 1 && hub.msgs[0].Type != websocket.MsgSkipNow) {
				t.Fatalf("broadcasts = %v, want %d %s", hub.types(), wantMsgs, websocket.MsgSkipNow)
			}
		})
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
			rec := serve(t, playerRouter(&fakeQueueManager{}, fakeCounter{}, &fakeBroadcaster{}, st), http.MethodGet, tt.target, "", nil)
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
