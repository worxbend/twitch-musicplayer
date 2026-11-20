package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"lofi-radio-backend/internal/models"
	queuemgr "lofi-radio-backend/internal/queue"
	"lofi-radio-backend/internal/websocket"
)

// fakeQueueManager is a scripted queueManager that records the song ids it
// was asked to queue.
type fakeQueueManager struct {
	items        []*models.QueueItem
	addErr       error
	playNextErr  error
	nextSong     *models.Song
	nextErr      error
	clearErr     error
	addedID      string
	playNextID   string
	addedSource  models.QueueSource
	clearedCalls int
}

func (f *fakeQueueManager) AddToQueue(_ context.Context, songID string, source models.QueueSource) (*models.QueueItem, error) {
	if f.addErr != nil {
		return nil, f.addErr
	}
	f.addedID, f.addedSource = songID, source
	item := &models.QueueItem{ID: "q1", SongID: songID, Source: source}
	f.items = append(f.items, item)
	return item, nil
}

func (f *fakeQueueManager) PlayNext(_ context.Context, songID string) error {
	f.playNextID = songID
	return f.playNextErr
}

func (f *fakeQueueManager) ListQueue(context.Context) ([]*models.QueueItem, error) {
	return f.items, nil
}

func (f *fakeQueueManager) NextSong(context.Context) (*models.Song, error) {
	return f.nextSong, f.nextErr
}

func (f *fakeQueueManager) ClearQueue(context.Context) error {
	f.clearedCalls++
	return f.clearErr
}

type fakeQueueRepo struct {
	removeErr error
	removedID string
}

func (f *fakeQueueRepo) Remove(_ context.Context, id string) error {
	f.removedID = id
	return f.removeErr
}

// fakeBroadcaster records every message handed to Broadcast.
type fakeBroadcaster struct {
	msgs []websocket.Message
}

func (f *fakeBroadcaster) Broadcast(msg websocket.Message) {
	f.msgs = append(f.msgs, msg)
}

func (f *fakeBroadcaster) ClientCount() int { return 0 }

// types lists the message types broadcast so far, in order.
func (f *fakeBroadcaster) types() []string {
	out := make([]string, len(f.msgs))
	for i, m := range f.msgs {
		out[i] = m.Type
	}
	return out
}

func queueRouter(mgr *fakeQueueManager, repo *fakeQueueRepo, b *fakeBroadcaster) http.Handler {
	return RegisterRoutesHTTP(nil, NewQueueHandler(mgr, repo, b), nil)
}

// assertQueueUpdated checks that exactly one queue_updated message with the
// given reason was broadcast.
func assertQueueUpdated(t *testing.T, b *fakeBroadcaster, reason string) {
	t.Helper()
	if len(b.msgs) != 1 || b.msgs[0].Type != websocket.MsgQueueUpdated {
		t.Fatalf("broadcasts = %v, want one %s", b.types(), websocket.MsgQueueUpdated)
	}
	var p websocket.QueueUpdatedPayload
	if err := json.Unmarshal(b.msgs[0].Payload, &p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if p.Reason != reason {
		t.Fatalf("reason = %q, want %q", p.Reason, reason)
	}
}

// oversizeBody is a syntactically valid request just over maxJSONBody.
var oversizeBody = fmt.Sprintf(`{"song_id":%q}`, strings.Repeat("a", 5<<10))

func TestQueueAdd(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		addErr   error
		wantCode int
	}{
		{name: "bad json", body: `{`, wantCode: http.StatusBadRequest},
		{name: "empty song_id", body: `{"song_id":""}`, wantCode: http.StatusBadRequest},
		{name: "oversize body", body: oversizeBody, wantCode: http.StatusRequestEntityTooLarge},
		{name: "unknown song", body: `{"song_id":"x"}`, addErr: fmt.Errorf("add: %w", queuemgr.ErrSongNotFound), wantCode: http.StatusNotFound},
		{name: "manager error", body: `{"song_id":"x"}`, addErr: errors.New("boom"), wantCode: http.StatusInternalServerError},
		{name: "success", body: `{"song_id":"x"}`, wantCode: http.StatusCreated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := &fakeQueueManager{addErr: tt.addErr}
			b := &fakeBroadcaster{}
			rec := serve(t, queueRouter(mgr, &fakeQueueRepo{}, b), http.MethodPost, "/v1/queue", tt.body, nil)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantCode, rec.Body.String())
			}
			if tt.wantCode != http.StatusCreated {
				if len(b.msgs) != 0 {
					t.Fatalf("unexpected broadcasts %v", b.types())
				}
				return
			}
			var item models.QueueItem
			decodeBody(t, rec, &item)
			if item.SongID != "x" || mgr.addedSource != models.QueueSourceManual {
				t.Fatalf("item = %+v, source = %q", item, mgr.addedSource)
			}
			assertQueueUpdated(t, b, "song_added")
		})
	}
}

func TestQueueRemove(t *testing.T) {
	tests := []struct {
		name      string
		removeErr error
		wantCode  int
	}{
		{name: "removed", wantCode: http.StatusNoContent},
		{name: "unknown item", removeErr: sql.ErrNoRows, wantCode: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeQueueRepo{removeErr: tt.removeErr}
			b := &fakeBroadcaster{}
			rec := serve(t, queueRouter(&fakeQueueManager{}, repo, b), http.MethodDelete, "/v1/queue/q7", "", nil)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if repo.removedID != "q7" {
				t.Fatalf("removed id = %q, want q7", repo.removedID)
			}
			if tt.wantCode == http.StatusNoContent {
				assertQueueUpdated(t, b, "song_removed")
			} else if len(b.msgs) != 0 {
				t.Fatalf("unexpected broadcasts %v", b.types())
			}
		})
	}
}

func TestQueuePlayNext(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		err      error
		wantCode int
	}{
		{name: "bad json", body: `not json`, wantCode: http.StatusBadRequest},
		{name: "empty song_id", body: `{}`, wantCode: http.StatusBadRequest},
		{name: "oversize body", body: oversizeBody, wantCode: http.StatusRequestEntityTooLarge},
		{name: "unknown song", body: `{"song_id":"x"}`, err: queuemgr.ErrSongNotFound, wantCode: http.StatusNotFound},
		{name: "manager error", body: `{"song_id":"x"}`, err: errors.New("boom"), wantCode: http.StatusInternalServerError},
		{name: "success", body: `{"song_id":"x"}`, wantCode: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := &fakeQueueManager{playNextErr: tt.err}
			b := &fakeBroadcaster{}
			rec := serve(t, queueRouter(mgr, &fakeQueueRepo{}, b), http.MethodPost, "/v1/queue:play-next", tt.body, nil)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantCode, rec.Body.String())
			}
			wantMsgs := 0
			if tt.wantCode == http.StatusOK {
				wantMsgs = 1
				if mgr.playNextID != "x" {
					t.Fatalf("play-next id = %q, want x", mgr.playNextID)
				}
			}
			// PlayNext broadcasts only skip_now; the queue_updated that follows
			// comes from the client's need_song round trip.
			if len(b.msgs) != wantMsgs || (wantMsgs == 1 && b.msgs[0].Type != websocket.MsgSkipNow) {
				t.Fatalf("broadcasts = %v, want %d %s", b.types(), wantMsgs, websocket.MsgSkipNow)
			}
		})
	}
}

func TestQueueSkip(t *testing.T) {
	tests := []struct {
		name     string
		song     *models.Song
		err      error
		wantCode int
	}{
		{name: "next song", song: &models.Song{ID: "n"}, wantCode: http.StatusOK},
		{name: "empty queue", err: queuemgr.ErrQueueEmpty, wantCode: http.StatusConflict},
		{name: "wrapped empty queue", err: fmt.Errorf("next: %w", queuemgr.ErrQueueEmpty), wantCode: http.StatusConflict},
		{name: "other error", err: errors.New("boom"), wantCode: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := &fakeQueueManager{nextSong: tt.song, nextErr: tt.err}
			b := &fakeBroadcaster{}
			rec := serve(t, queueRouter(mgr, &fakeQueueRepo{}, b), http.MethodPost, "/v1/queue:skip", "", nil)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if tt.wantCode != http.StatusOK {
				if len(b.msgs) != 0 {
					t.Fatalf("unexpected broadcasts %v", b.types())
				}
				return
			}
			var got struct {
				Song models.Song `json:"song"`
			}
			decodeBody(t, rec, &got)
			if got.Song.ID != "n" {
				t.Fatalf("song = %+v, want id n", got.Song)
			}
			assertQueueUpdated(t, b, "skipped")
		})
	}
}

func TestQueueClear(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{name: "cleared", wantCode: http.StatusOK},
		{name: "manager error", err: errors.New("boom"), wantCode: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := &fakeQueueManager{clearErr: tt.err}
			b := &fakeBroadcaster{}
			rec := serve(t, queueRouter(mgr, &fakeQueueRepo{}, b), http.MethodPost, "/v1/queue:clear", "", nil)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if mgr.clearedCalls != 1 {
				t.Fatalf("ClearQueue calls = %d, want 1", mgr.clearedCalls)
			}
			if tt.wantCode == http.StatusOK {
				assertQueueUpdated(t, b, "cleared")
			}
		})
	}
}

func TestQueueList(t *testing.T) {
	mgr := &fakeQueueManager{items: []*models.QueueItem{{ID: "q1"}, {ID: "q2"}}}
	rec := serve(t, queueRouter(mgr, &fakeQueueRepo{}, &fakeBroadcaster{}), http.MethodGet, "/v1/queue", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		Items []models.QueueItem `json:"items"`
		Total int                `json:"total"`
	}
	decodeBody(t, rec, &got)
	if got.Total != 2 || len(got.Items) != 2 {
		t.Fatalf("got %+v", got)
	}
}
