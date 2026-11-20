package api

import (
	"context"
	"errors"
	"net/http"
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
	return RegisterRoutesHTTP(nil, nil, NewPlayerHandler(songs, queue, hub, st))
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
