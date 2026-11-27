package api

import (
	"context"
	"io"
	"log"
	"net/http"
	"path/filepath"
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

type playerQueueRepo interface {
	Count(ctx context.Context) (int, error)
}

type playerHub interface {
	ClientCount() int
	Broadcast(msg websocket.Message)
}

// PlayerHandler handles player state endpoints.
type PlayerHandler struct {
	queueRepo    playerQueueRepo
	skipSource   skipSource
	hub          playerHub
	stateTracker *player.StateTracker
	// nowPlayingFormat is obs.now_playing_format, used by NowPlayingText.
	nowPlayingFormat string
}

func NewPlayerHandler(
	q playerQueueRepo,
	skip skipSource,
	hub playerHub,
	st *player.StateTracker,
	nowPlayingFormat string,
) *PlayerHandler {
	return &PlayerHandler{queueRepo: q, skipSource: skip, hub: hub, stateTracker: st, nowPlayingFormat: nowPlayingFormat}
}

// Skip handles POST /v1/player:skip.
// It broadcasts skip_now over WebSocket so all connected audio clients
// (main scene, overlay) immediately abandon the current track and request the
// next one. No server-side queue manipulation happens here — the natural
// need_song → NextSong flow handles the dequeue. Like queue:skip, it returns
// 409 and broadcasts nothing when nothing would play next: no playable queued
// entry under manual_only, or an auto strategy with an empty library. So a
// skip cannot drop the stream into silence.
func (h *PlayerHandler) Skip(w http.ResponseWriter, r *http.Request) {
	items, err := h.skipSource.ListQueue(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to skip current song")
		return
	}
	reason, err := skipConflict(r.Context(), h.skipSource, items)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to skip current song")
		return
	}
	if reason != "" {
		writeError(w, http.StatusConflict, reason)
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

	totalSongs, err := h.skipSource.TotalSongs(ctx)
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
	// next_song is optional: a failed queue read omits it instead of failing.
	if items, err := h.skipSource.ListQueue(ctx); err == nil {
		state.NextSong = models.NextPlayableSong(items)
	} else {
		log.Printf("player state: list queue: %v", err)
	}

	writeJSON(w, http.StatusOK, state)
}

// History handles GET /v1/history?limit=N&format=json|text. It returns the
// most recent song starts. limit defaults to 20 and is clamped to
// player.MaxHistory. format=json (the default) is {items,total}, newest first;
// format=text is a plain-text setlist, oldest first, for VOD music credits.
func (h *PlayerHandler) History(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := q.Get("format")
	if format != "" && format != "json" && format != "text" {
		writeError(w, http.StatusBadRequest, "format must be json or text")
		return
	}
	limit := defaultHistoryLimit
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = min(n, player.MaxHistory)
	}

	items := h.stateTracker.History(limit)
	if format == "text" {
		writeSetlist(w, items)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"total": len(items),
	})
}

// writeSetlist writes newest-first entries as a plain-text setlist, oldest
// first. no-store keeps proxies from serving a stale list.
func writeSetlist(w http.ResponseWriter, newestFirst []player.PlayEntry) {
	var b strings.Builder
	for i := len(newestFirst) - 1; i >= 0; i-- {
		b.WriteString(setlistLine(newestFirst[i]))
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, b.String())
}

// setlistLine renders one entry as "2006-01-02 15:04 UTC  Artist - Title\n".
// An empty title falls back to the filename without its extension; a song
// without an artist renders the title alone. Control characters become spaces
// so a tag cannot split the line.
func setlistLine(e player.PlayEntry) string {
	label := e.Song.Title
	if label == "" {
		label = strings.TrimSuffix(e.Song.Filename, filepath.Ext(e.Song.Filename))
	}
	text := label
	if e.Song.Artist != "" {
		text = e.Song.Artist + " - " + label
	}
	return e.StartedAt.UTC().Format("2006-01-02 15:04 UTC") + "  " + player.OneLine(text) + "\n"
}
