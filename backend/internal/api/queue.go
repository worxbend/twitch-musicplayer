package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"lofi-radio-backend/internal/models"
	queuemgr "lofi-radio-backend/internal/queue"
	"lofi-radio-backend/internal/websocket"
)

type queueManager interface {
	AddToQueue(ctx context.Context, songID string, source models.QueueSource) (*models.QueueItem, error)
	PlayNext(ctx context.Context, songID string) error
	ClearQueue(ctx context.Context) error
	skipSource
}

// skipSource tells a skip handler whether a skip would leave anything to play.
type skipSource interface {
	ListQueue(ctx context.Context) ([]*models.QueueItem, error)
	AutoSelects() bool
	TotalSongs(ctx context.Context) (int, error)
}

type queueRepo interface {
	Remove(ctx context.Context, id string) error
	Move(ctx context.Context, id string, delta int) (position int, moved bool, err error)
}

type queueBroadcaster interface {
	Broadcast(msg websocket.Message)
}

// maxJSONBody caps queue request bodies. A song_id payload is under 100 bytes.
const maxJSONBody = 4 << 10

type addToQueueRequest struct {
	SongID string `json:"song_id"`
}

type playNextRequest struct {
	SongID string `json:"song_id"`
}

type moveQueueItemRequest struct {
	Direction string `json:"direction"`
}

// moveDeltas maps a move direction to the index shift it applies.
var moveDeltas = map[string]int{"up": -1, "down": 1}

// QueueHandler handles queue resource endpoints.
type QueueHandler struct {
	mgr         queueManager
	repo        queueRepo
	broadcaster queueBroadcaster
}

func NewQueueHandler(mgr queueManager, repo queueRepo, broadcaster queueBroadcaster) *QueueHandler {
	return &QueueHandler{mgr: mgr, repo: repo, broadcaster: broadcaster}
}

// List handles GET /v1/queue.
func (h *QueueHandler) List(w http.ResponseWriter, r *http.Request) {
	items, err := h.mgr.ListQueue(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list queue")
		return
	}
	if items == nil {
		items = []*models.QueueItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"total": len(items),
	})
}

// Add handles POST /v1/queue.
func (h *QueueHandler) Add(w http.ResponseWriter, r *http.Request) {
	var req addToQueueRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.SongID == "" {
		writeError(w, http.StatusBadRequest, "song_id is required")
		return
	}

	// The manager returns the entry it created. Reading back the tail of the
	// queue instead would be wrong under the auto_refill strategy, which
	// appends further auto-picked songs behind the manual one.
	item, err := h.mgr.AddToQueue(r.Context(), req.SongID, models.QueueSourceManual)
	if err != nil {
		if errors.Is(err, queuemgr.ErrSongNotFound) {
			writeError(w, http.StatusNotFound, "song not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to add song to queue")
		return
	}

	h.broadcastQueueUpdated(r.Context(), "song_added")
	writeJSON(w, http.StatusCreated, item)
}

// Remove handles DELETE /v1/queue/{id}.
func (h *QueueHandler) Remove(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.repo.Remove(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "queue item not found")
		return
	}
	h.broadcastQueueUpdated(r.Context(), "song_removed")
	w.WriteHeader(http.StatusNoContent)
}

// Move handles POST /v1/queue/{id}:move.
// It shifts the item one place up or down; at either end it is a no-op that
// reports moved=false and broadcasts nothing.
func (h *QueueHandler) Move(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req moveQueueItemRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	delta, ok := moveDeltas[req.Direction]
	if !ok {
		writeError(w, http.StatusBadRequest, `direction must be "up" or "down"`)
		return
	}

	pos, moved, err := h.repo.Move(r.Context(), id, delta)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "queue item not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to move queue item")
		return
	}
	if moved {
		h.broadcastQueueUpdated(r.Context(), "song_moved")
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "position": pos, "moved": moved})
}

// Skip handles POST /v1/queue:skip.
// It broadcasts skip_now so connected audio clients interrupt the current
// track, and returns the queued song that will play next (null when no entry
// is playable and the strategy auto-selects). It does not dequeue: the client's
// need_song -> NextSong flow does, so the next song is heard, not lost.
func (h *QueueHandler) Skip(w http.ResponseWriter, r *http.Request) {
	items, err := h.mgr.ListQueue(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to skip current song")
		return
	}
	reason, err := skipConflict(r.Context(), h.mgr, items)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to skip current song")
		return
	}
	if reason != "" {
		writeError(w, http.StatusConflict, reason)
		return
	}
	var next *models.Song
	if first := models.FirstPlayable(items); first != nil {
		next = first.Song
	}
	if msg, err := websocket.Encode(websocket.MsgSkipNow, nil); err == nil {
		h.broadcaster.Broadcast(msg)
	}
	h.broadcastQueueUpdated(r.Context(), "skipped")
	writeJSON(w, http.StatusOK, map[string]any{"song": next})
}

// skipConflict returns why a skip would leave the stream silent, or "" when
// need_song will find a song. With no playable entry, manual_only finds
// nothing, and an auto strategy finds nothing when the library is empty. Those
// are expected states, not faults. The library is counted only when the queue
// has no playable entry, so a playable queue never pays for it.
func skipConflict(ctx context.Context, src skipSource, items []*models.QueueItem) (string, error) {
	if models.FirstPlayable(items) != nil {
		return "", nil
	}
	if !src.AutoSelects() {
		return "queue is empty", nil
	}
	total, err := src.TotalSongs(ctx)
	if err != nil {
		return "", fmt.Errorf("count songs: %w", err)
	}
	if total == 0 {
		return queuemgr.ErrLibraryEmpty.Error(), nil
	}
	return "", nil
}

// Clear handles POST /v1/queue:clear.
func (h *QueueHandler) Clear(w http.ResponseWriter, r *http.Request) {
	if err := h.mgr.ClearQueue(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to clear queue")
		return
	}
	h.broadcastQueueUpdated(r.Context(), "cleared")
	writeJSON(w, http.StatusOK, map[string]string{"message": "queue cleared"})
}

// PlayNext handles POST /v1/queue:play-next.
// It inserts the requested song at the front of the queue and broadcasts
// skip_now so connected audio clients interrupt their current track.
func (h *QueueHandler) PlayNext(w http.ResponseWriter, r *http.Request) {
	var req playNextRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.SongID == "" {
		writeError(w, http.StatusBadRequest, "song_id is required")
		return
	}

	if err := h.mgr.PlayNext(r.Context(), req.SongID); err != nil {
		if errors.Is(err, queuemgr.ErrSongNotFound) {
			writeError(w, http.StatusNotFound, "song not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to queue song for immediate play")
		return
	}

	// Tell all connected audio clients to abandon the current track and pull
	// the next one (which is now the requested song at position 0).
	if msg, err := websocket.Encode(websocket.MsgSkipNow, nil); err == nil {
		h.broadcaster.Broadcast(msg)
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "song queued for immediate play"})
}

func (h *QueueHandler) broadcastQueueUpdated(ctx context.Context, reason string) {
	if h.broadcaster == nil {
		return
	}

	depth := 0
	if items, err := h.mgr.ListQueue(ctx); err == nil {
		depth = len(items)
	}

	msg, err := websocket.Encode(websocket.MsgQueueUpdated, websocket.QueueUpdatedPayload{
		QueueDepth: depth,
		Reason:     reason,
	})
	if err == nil {
		h.broadcaster.Broadcast(msg)
	}
}

// decodeJSONBody decodes a size-capped JSON body into dst. On failure it writes
// 413 for an oversize body, 400 otherwise, and reports false.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	err := json.NewDecoder(r.Body).Decode(dst)
	if err == nil {
		return true
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return false
	}
	writeError(w, http.StatusBadRequest, "invalid request body")
	return false
}
