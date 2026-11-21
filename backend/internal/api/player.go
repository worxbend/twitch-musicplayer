package api

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"lofi-radio-backend/internal/models"
	"lofi-radio-backend/internal/player"
	"lofi-radio-backend/internal/websocket"
)

// defaultHistoryLimit is the page size of GET /v1/history without ?limit.
const defaultHistoryLimit = 20

// maxIdleTextRunes caps the ?idle= fallback text of GET /v1/now-playing.txt.
const maxIdleTextRunes = 200

type playerSongRepo interface {
	Count(ctx context.Context) (int, error)
}

type playerQueueRepo interface {
	Count(ctx context.Context) (int, error)
}

// playerSkipSource tells Skip whether a skip would leave anything to play.
type playerSkipSource interface {
	ListQueue(ctx context.Context) ([]*models.QueueItem, error)
	AutoSelects() bool
}

type playerHub interface {
	ClientCount() int
	Broadcast(msg websocket.Message)
}

// PlayerHandler handles player state endpoints.
type PlayerHandler struct {
	songRepo     playerSongRepo
	queueRepo    playerQueueRepo
	skipSource   playerSkipSource
	hub          playerHub
	stateTracker *player.StateTracker
	// nowPlayingFormat is obs.now_playing_format, used by NowPlayingText.
	nowPlayingFormat string
}

func NewPlayerHandler(
	s playerSongRepo,
	q playerQueueRepo,
	skip playerSkipSource,
	hub playerHub,
	st *player.StateTracker,
	nowPlayingFormat string,
) *PlayerHandler {
	return &PlayerHandler{songRepo: s, queueRepo: q, skipSource: skip, hub: hub, stateTracker: st, nowPlayingFormat: nowPlayingFormat}
}

// Skip handles POST /v1/player:skip.
// It broadcasts skip_now over WebSocket so all connected audio clients
// (main scene, overlay) immediately abandon the current track and request the
// next one. No server-side queue manipulation happens here — the natural
// need_song → NextSong flow handles the dequeue. Like queue:skip, it returns
// 409 and broadcasts nothing under manual_only when no queued entry is
// playable, so a skip cannot drop the stream into silence.
func (h *PlayerHandler) Skip(w http.ResponseWriter, r *http.Request) {
	items, err := h.skipSource.ListQueue(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to skip current song")
		return
	}
	if nothingToSkipTo(items, h.skipSource.AutoSelects()) {
		writeError(w, http.StatusConflict, "queue is empty")
		return
	}
	if msg, err := websocket.Encode(websocket.MsgSkipNow, nil); err == nil {
		h.hub.Broadcast(msg)
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "skip broadcast"})
}

// NowPlayingText handles GET /v1/now-playing.txt?idle=TEXT: the current song
// as one plain-text line for chat bots (Nightbot, StreamElements). While idle
// it returns the optional idle text (control characters flattened to spaces, trimmed), else an empty
// body. idle is validated on every request, playing or not: over
// maxIdleTextRunes is 400. no-store keeps proxies from serving a stale title.
func (h *PlayerHandler) NowPlayingText(w http.ResponseWriter, r *http.Request) {
	idle := strings.TrimSpace(player.OneLine(r.URL.Query().Get("idle")))
	if utf8.RuneCountInString(idle) > maxIdleTextRunes {
		writeError(w, http.StatusBadRequest, "idle must be at most "+strconv.Itoa(maxIdleTextRunes)+" characters")
		return
	}

	line := player.FormatNowPlaying(h.nowPlayingFormat, h.stateTracker.GetCurrentSong())
	if line == "" {
		line = idle
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, line)
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
