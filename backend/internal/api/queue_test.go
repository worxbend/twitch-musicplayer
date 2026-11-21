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
	listErr      error
	clearErr     error
	autoSelects  bool
	emptyLibrary bool
	libraryErr   error
	libraryCalls int
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
	return f.items, f.listErr
}

func (f *fakeQueueManager) AutoSelects() bool { return f.autoSelects }

// TotalSongs reports one song unless emptyLibrary is set, so the zero value is
// a non-empty library.
func (f *fakeQueueManager) TotalSongs(context.Context) (int, error) {
	f.libraryCalls++
	if f.emptyLibrary {
		return 0, f.libraryErr
	}
	return 1, f.libraryErr
}

func (f *fakeQueueManager) ClearQueue(context.Context) error {
	f.clearedCalls++
	return f.clearErr
}

type fakeQueueRepo struct {
	removeErr error
	removedID string

	movePos   int
	moved     bool
	moveErr   error
	moveCalls []moveCall
}

type moveCall struct {
	id    string
	delta int
}

func (f *fakeQueueRepo) Move(_ context.Context, id string, delta int) (int, bool, error) {
	f.moveCalls = append(f.moveCalls, moveCall{id: id, delta: delta})
	return f.movePos, f.moved, f.moveErr
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

func TestQueueMove(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		repo      fakeQueueRepo
		wantCode  int
		wantDelta int // 0 means the repo must not be called
		wantBody  string
	}{
		{name: "up", body: `{"direction":"up"}`, repo: fakeQueueRepo{movePos: 1, moved: true}, wantCode: http.StatusOK, wantDelta: -1, wantBody: `{"id":"q7","moved":true,"position":1}`},
		{name: "down at end", body: `{"direction":"down"}`, repo: fakeQueueRepo{movePos: 3}, wantCode: http.StatusOK, wantDelta: 1, wantBody: `{"id":"q7","moved":false,"position":3}`},
		{name: "bad direction", body: `{"direction":"left"}`, wantCode: http.StatusBadRequest},
		{name: "empty body", body: ``, wantCode: http.StatusBadRequest},
		{name: "oversize body", body: fmt.Sprintf(`{"direction":%q}`, strings.Repeat("a", 5<<10)), wantCode: http.StatusRequestEntityTooLarge},
		{name: "unknown item", body: `{"direction":"up"}`, repo: fakeQueueRepo{moveErr: sql.ErrNoRows}, wantCode: http.StatusNotFound, wantDelta: -1},
		{name: "repo error", body: `{"direction":"down"}`, repo: fakeQueueRepo{moveErr: errors.New("boom")}, wantCode: http.StatusInternalServerError, wantDelta: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := tt.repo
			b := &fakeBroadcaster{}
			rec := serve(t, queueRouter(&fakeQueueManager{}, &repo, b), http.MethodPost, "/v1/queue/q7:move", tt.body, nil)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantCode, rec.Body.String())
			}
			switch {
			case tt.wantDelta == 0 && len(repo.moveCalls) != 0:
				t.Fatalf("repo.Move called %v, want no call", repo.moveCalls)
			case tt.wantDelta != 0 && (len(repo.moveCalls) != 1 || repo.moveCalls[0] != moveCall{id: "q7", delta: tt.wantDelta}):
				t.Fatalf("repo.Move calls = %v, want [{q7 %d}]", repo.moveCalls, tt.wantDelta)
			}
			if tt.wantBody != "" {
				if got := strings.TrimSpace(rec.Body.String()); got != tt.wantBody {
					t.Fatalf("body = %s, want %s", got, tt.wantBody)
				}
			}
			if tt.repo.moved {
				assertQueueUpdated(t, b, "song_moved")
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
	front := &models.QueueItem{ID: "q1", SongID: "n", Song: &models.Song{ID: "n"}}
	behind := &models.QueueItem{ID: "q2", SongID: "m", Song: &models.Song{ID: "m"}}
	// dead is an entry whose song left the library: ListQueue reports Song=nil.
	dead := &models.QueueItem{ID: "q3", SongID: "gone"}
	tests := []struct {
		name         string
		items        []*models.QueueItem
		listErr      error
		autoSelects  bool
		emptyLibrary bool
		libraryErr   error
		wantCode     int
		wantSongID   string
		wantErr      string
	}{
		{name: "non-empty queue", items: []*models.QueueItem{front, behind}, wantCode: http.StatusOK, wantSongID: "n"},
		{name: "playable entry, auto-select, empty library", items: []*models.QueueItem{front}, autoSelects: true, emptyLibrary: true, wantCode: http.StatusOK, wantSongID: "n"},
		{name: "dead head skipped", items: []*models.QueueItem{dead, behind}, wantCode: http.StatusOK, wantSongID: "m"},
		{name: "all entries dead, auto-select", items: []*models.QueueItem{dead}, autoSelects: true, wantCode: http.StatusOK},
		{name: "all entries dead, manual_only", items: []*models.QueueItem{dead}, wantCode: http.StatusConflict},
		{name: "empty queue, manual_only", wantCode: http.StatusConflict},
		{name: "empty queue, auto-select", autoSelects: true, wantCode: http.StatusOK},
		{name: "empty queue, auto-select, empty library", autoSelects: true, emptyLibrary: true, wantCode: http.StatusConflict, wantErr: "no songs available"},
		{name: "all entries dead, auto-select, empty library", items: []*models.QueueItem{dead}, autoSelects: true, emptyLibrary: true, wantCode: http.StatusConflict, wantErr: "no songs available"},
		{name: "empty queue, auto-select, library count error", autoSelects: true, libraryErr: errors.New("boom"), wantCode: http.StatusInternalServerError, wantErr: "failed to skip current song"},
		{name: "list error", listErr: errors.New("boom"), wantCode: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := &fakeQueueManager{items: tt.items, listErr: tt.listErr, autoSelects: tt.autoSelects, emptyLibrary: tt.emptyLibrary, libraryErr: tt.libraryErr}
			b := &fakeBroadcaster{}
			rec := serve(t, queueRouter(mgr, &fakeQueueRepo{}, b), http.MethodPost, "/v1/queue:skip", "", nil)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if len(mgr.items) != len(tt.items) {
				t.Fatalf("queue length = %d, want %d unchanged", len(mgr.items), len(tt.items))
			}
			if tt.wantCode != http.StatusOK {
				if len(b.msgs) != 0 {
					t.Fatalf("unexpected broadcasts %v", b.types())
				}
				if tt.wantErr != "" {
					var body map[string]string
					decodeBody(t, rec, &body)
					if body["error"] != tt.wantErr {
						t.Fatalf("error = %q, want %q", body["error"], tt.wantErr)
					}
				}
				return
			}
			if tt.wantSongID != "" && mgr.libraryCalls != 0 {
				t.Fatalf("TotalSongs calls = %d, want 0 when the queue is playable", mgr.libraryCalls)
			}
			var got struct {
				Song *models.Song `json:"song"`
			}
			decodeBody(t, rec, &got)
			switch {
			case tt.wantSongID == "" && got.Song != nil:
				t.Fatalf("song = %+v, want null", got.Song)
			case tt.wantSongID != "" && (got.Song == nil || got.Song.ID != tt.wantSongID):
				t.Fatalf("song = %+v, want id %s", got.Song, tt.wantSongID)
			}
			if len(b.msgs) != 2 || b.msgs[0].Type != websocket.MsgSkipNow {
				t.Fatalf("broadcasts = %v, want %s then %s", b.types(), websocket.MsgSkipNow, websocket.MsgQueueUpdated)
			}
			b.msgs = b.msgs[1:]
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
