package api

import (
	"context"
	"net/http"
	"strconv"

	"lofi-radio-backend/internal/models"
	"lofi-radio-backend/internal/player"
	"lofi-radio-backend/internal/websocket"
)

// defaultHistoryLimit is the page size of GET /v1/history without ?limit.
const defaultHistoryLimit = 20

type playerSongRepo interface {
	Count(ctx context.Context) (int, error)
}

type playerQueueRepo interface {
	Count(ctx context.Context) (int, error)
}

type playerHub interface {
	ClientCount() int
	Broadcast(msg websocket.Message)
}

// PlayerHandler handles player state endpoints.
type PlayerHandler struct {
	songRepo     playerSongRepo
	queueRepo    playerQueueRepo
	hub          playerHub
	stateTracker *player.StateTracker
}

func NewPlayerHandler(
	s playerSongRepo,
	q playerQueueRepo,
	hub playerHub,
	st *player.StateTracker,
) *PlayerHandler {
	return &PlayerHandler{songRepo: s, queueRepo: q, hub: hub, stateTracker: st}
}

// Skip handles POST /v1/player:skip.
// It broadcasts skip_now over WebSocket so all connected audio clients
// (main scene, overlay) immediately abandon the current track and request the
// next one. No server-side queue manipulation happens here — the natural
// need_song → NextSong flow handles the dequeue.
func (h *PlayerHandler) Skip(w http.ResponseWriter, r *http.Request) {
	if msg, err := websocket.Encode(websocket.MsgSkipNow, nil); err == nil {
		h.hub.Broadcast(msg)
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "skip broadcast"})
}

// State handles GET /v1/player.
func (h *PlayerHandler) State(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	totalSongs, err := h.songRepo.Count(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count songs")
		return
	}

	queueLength, err := h.queueRepo.Count(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count queue")
		return
	}

	state := models.PlayerState{
		CurrentSong: h.stateTracker.GetCurrentSong(),
		TotalSongs:  totalSongs,
		QueueLength: queueLength,
	}

	writeJSON(w, http.StatusOK, state)
}

// History handles GET /v1/history?limit=N. It returns the most recent song
// starts, newest first. limit defaults to 20 and is clamped to player.MaxHistory.
func (h *PlayerHandler) History(w http.ResponseWriter, r *http.Request) {
	limit := defaultHistoryLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = min(n, player.MaxHistory)
	}

	items := h.stateTracker.History(limit)
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"total": len(items),
	})
}
