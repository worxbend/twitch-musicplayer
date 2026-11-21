package api

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"lofi-radio-backend/internal/models"
)

type songRepo interface {
	List(ctx context.Context) ([]*models.Song, error)
	Search(ctx context.Context, query string) ([]*models.Song, error)
	GetByID(ctx context.Context, id string) (*models.Song, error)
}

// SongsHandler handles song resource endpoints.
type SongsHandler struct {
	repo     songRepo
	musicDir string
}

func NewSongsHandler(repo songRepo, musicDir string) *SongsHandler {
	return &SongsHandler{repo: repo, musicDir: musicDir}
}

// List handles GET /v1/songs and GET /v1/songs?q=<query>.
func (h *SongsHandler) List(w http.ResponseWriter, r *http.Request) {
	var songs []*models.Song
	var err error

	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		songs, err = h.repo.Search(r.Context(), q)
	} else {
		songs, err = h.repo.List(r.Context())
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list songs")
		return
	}
	if songs == nil {
		songs = []*models.Song{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"songs": songs,
		"total": len(songs),
	})
}

// Get handles GET /v1/songs/{id}.
func (h *SongsHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	song, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "song not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get song")
		return
	}
	writeJSON(w, http.StatusOK, song)
}

// StreamContent handles GET /v1/songs/{id}/content and GET /v1/songs/{id}:stream.
// http.ServeFile handles Range headers automatically, enabling streaming/seeking.
func (h *SongsHandler) StreamContent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	song, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "song not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get song")
		return
	}

	path := song.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(h.musicDir, path)
	}
	path, err = streamPathUnderMusicDir(h.musicDir, path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "song file not found")
			return
		}
		writeError(w, http.StatusForbidden, "song path is outside music directory")
		return
	}

	// Each write gets a fresh stall deadline so long downloads outlive
	// WriteTimeout but a client that stops reading is dropped.
	sw := &stallWriter{ResponseWriter: w, rc: http.NewResponseController(w), songID: id}
	sw.extendDeadline()
	http.ServeFile(sw, r, path)
}

// streamStallTimeout bounds how long one write to a song stream may block.
var streamStallTimeout = 30 * time.Second

// stallWriter renews the connection write deadline before every write.
type stallWriter struct {
	http.ResponseWriter
	rc       *http.ResponseController
	songID   string
	reported bool
}

func (sw *stallWriter) Write(p []byte) (int, error) {
	sw.extendDeadline()
	return sw.ResponseWriter.Write(p)
}

func (sw *stallWriter) Unwrap() http.ResponseWriter { return sw.ResponseWriter }

func (sw *stallWriter) extendDeadline() {
	err := sw.rc.SetWriteDeadline(time.Now().Add(streamStallTimeout))
	if err == nil || errors.Is(err, http.ErrNotSupported) || sw.reported {
		return
	}
	sw.reported = true
	log.Printf("stream %s: set write deadline: %v", sw.songID, err)
}

func streamPathUnderMusicDir(musicDir, songPath string) (string, error) {
	root, err := filepath.Abs(musicDir)
	if err != nil {
		return "", err
	}
	path, err := filepath.Abs(songPath)
	if err != nil {
		return "", err
	}

	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}

	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("song path outside music directory")
	}
	return path, nil
}
