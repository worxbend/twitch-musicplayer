package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"lofi-radio-backend/internal/meta"
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
// http.ServeContent handles Range headers, enabling streaming/seeking.
func (h *SongsHandler) StreamContent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	file, ok := h.resolveSongFile(r.Context(), w, id)
	if !ok {
		return
	}
	f, err := os.Open(file.path)
	if err != nil {
		writeStreamOpenError(w, id, err)
		return
	}
	defer f.Close()

	// Each write gets a fresh stall deadline so long downloads outlive
	// WriteTimeout but a client that stops reading is dropped.
	sw := &stallWriter{ResponseWriter: w, rc: http.NewResponseController(w), songID: id}
	sw.extendDeadline()
	http.ServeContent(sw, r, filepath.Base(file.path), file.info.ModTime(), f)
}

// Cover handles GET /v1/songs/{id}/cover: the embedded ID3v2 cover image.
// Clients must revalidate every use with the ETag over its bytes, so a
// retagged cover shows on the next request.
func (h *SongsHandler) Cover(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	file, ok := h.resolveSongFile(r.Context(), w, id)
	if !ok {
		return
	}
	mime, img, err := meta.Cover(file.path)
	switch {
	case errors.Is(err, meta.ErrCoverTooLarge):
		log.Printf("cover %s: %v", id, err)
		fallthrough
	case errors.Is(err, meta.ErrNoCover):
		writeError(w, http.StatusNotFound, "song has no cover art")
		return
	case err != nil:
		writeStreamOpenError(w, id, err)
		return
	}

	sum := sha256.Sum256(img)
	header := w.Header()
	header.Set("Content-Type", mime)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Cache-Control", "public, no-cache")
	header.Set("ETag", `"`+hex.EncodeToString(sum[:8])+`"`)
	http.ServeContent(w, r, "", file.info.ModTime(), bytes.NewReader(img))
}

// songFile is a song's regular file, resolved inside the music directory.
type songFile struct {
	path string
	info os.FileInfo
}

// resolveSongFile looks up song id and confines its file to the music
// directory. On failure it writes the error response and reports false.
func (h *SongsHandler) resolveSongFile(ctx context.Context, w http.ResponseWriter, id string) (songFile, bool) {
	song, err := h.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "song not found")
			return songFile{}, false
		}
		writeError(w, http.StatusInternalServerError, "failed to get song")
		return songFile{}, false
	}

	path := song.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(h.musicDir, path)
	}
	path, err = streamPathUnderMusicDir(h.musicDir, path)
	if err != nil {
		writeStreamOpenError(w, id, err)
		return songFile{}, false
	}
	info, err := os.Stat(path)
	if err != nil {
		writeStreamOpenError(w, id, err)
		return songFile{}, false
	}
	if info.IsDir() {
		writeError(w, http.StatusNotFound, "song file not found")
		return songFile{}, false
	}
	return songFile{path: path, info: info}, true
}

// writeStreamOpenError maps a failure to resolve or open a song file to a
// status: only the containment check is 403; I/O and permission faults are 500.
func writeStreamOpenError(w http.ResponseWriter, id string, err error) {
	switch {
	case errors.Is(err, errOutsideMusicDir):
		writeError(w, http.StatusForbidden, "song path is outside music directory")
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		writeError(w, http.StatusNotFound, "song file not found")
	default:
		log.Printf("stream %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to open song file")
	}
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

var errOutsideMusicDir = errors.New("song path outside music directory")

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
		return "", errOutsideMusicDir
	}
	return path, nil
}
