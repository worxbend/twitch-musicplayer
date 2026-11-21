package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"lofi-radio-backend/internal/models"
)

// fakeSongRepo is an in-memory songRepo. GetByID reports sql.ErrNoRows for an
// unknown id, matching the real repository.
type fakeSongRepo struct {
	songs     []*models.Song
	err       error
	lastQuery string
}

func (f *fakeSongRepo) List(context.Context) ([]*models.Song, error) {
	return f.songs, f.err
}

func (f *fakeSongRepo) Search(_ context.Context, query string) ([]*models.Song, error) {
	f.lastQuery = query
	return f.songs, f.err
}

func (f *fakeSongRepo) GetByID(_ context.Context, id string) (*models.Song, error) {
	if f.err != nil {
		return nil, f.err
	}
	for _, s := range f.songs {
		if s.ID == id {
			return s, nil
		}
	}
	return nil, sql.ErrNoRows
}

// serve sends one request through the real route table and returns the
// recorded response.
func serve(t *testing.T, h http.Handler, method, target, body string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func songsRouter(repo songRepo, musicDir string) http.Handler {
	return RegisterRoutesHTTP(NewSongsHandler(repo, musicDir), nil, nil)
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
}

func TestSongsList(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		repo      *fakeSongRepo
		wantCode  int
		wantTotal int
		wantQuery string
	}{
		{
			name:      "list all",
			target:    "/v1/songs",
			repo:      &fakeSongRepo{songs: []*models.Song{{ID: "a"}, {ID: "b"}}},
			wantCode:  http.StatusOK,
			wantTotal: 2,
		},
		{
			name:      "search passes trimmed query",
			target:    "/v1/songs?q=+rain+",
			repo:      &fakeSongRepo{songs: []*models.Song{{ID: "a"}}},
			wantCode:  http.StatusOK,
			wantTotal: 1,
			wantQuery: "rain",
		},
		{
			name:      "nil result renders empty list",
			target:    "/v1/songs",
			repo:      &fakeSongRepo{},
			wantCode:  http.StatusOK,
			wantTotal: 0,
		},
		{
			name:     "repo error",
			target:   "/v1/songs",
			repo:     &fakeSongRepo{err: errors.New("boom")},
			wantCode: http.StatusInternalServerError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, songsRouter(tt.repo, t.TempDir()), http.MethodGet, tt.target, "", nil)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if tt.repo.lastQuery != tt.wantQuery {
				t.Fatalf("search query = %q, want %q", tt.repo.lastQuery, tt.wantQuery)
			}
			if tt.wantCode != http.StatusOK {
				return
			}
			var got struct {
				Songs []models.Song `json:"songs"`
				Total int           `json:"total"`
			}
			decodeBody(t, rec, &got)
			if got.Total != tt.wantTotal || len(got.Songs) != tt.wantTotal || got.Songs == nil {
				t.Fatalf("got %d songs, total %d, want %d", len(got.Songs), got.Total, tt.wantTotal)
			}
		})
	}
}

func TestSongsGet(t *testing.T) {
	tests := []struct {
		name     string
		repo     *fakeSongRepo
		id       string
		wantCode int
	}{
		{name: "found", repo: &fakeSongRepo{songs: []*models.Song{{ID: "a", Title: "Rain"}}}, id: "a", wantCode: http.StatusOK},
		{name: "missing", repo: &fakeSongRepo{}, id: "nope", wantCode: http.StatusNotFound},
		{name: "repo error", repo: &fakeSongRepo{err: errors.New("boom")}, id: "a", wantCode: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, songsRouter(tt.repo, t.TempDir()), http.MethodGet, "/v1/songs/"+tt.id, "", nil)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if tt.wantCode == http.StatusOK {
				var got models.Song
				decodeBody(t, rec, &got)
				if got.ID != tt.id || got.Title != "Rain" {
					t.Fatalf("got song %+v", got)
				}
			}
		})
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSongsStreamContent(t *testing.T) {
	base := t.TempDir()
	musicDir := filepath.Join(base, "music")
	if err := os.Mkdir(musicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const content = "0123456789"
	writeFile(t, filepath.Join(musicDir, "a.mp3"), content)
	writeFile(t, filepath.Join(base, "secret.mp3"), "secret")

	tests := []struct {
		name     string
		path     string
		target   string
		rangeHdr string
		wantCode int
		wantBody string
	}{
		{name: "absolute path", path: filepath.Join(musicDir, "a.mp3"), target: "/v1/songs/s/content", wantCode: http.StatusOK, wantBody: content},
		{name: "relative path", path: "a.mp3", target: "/v1/songs/s/content", wantCode: http.StatusOK, wantBody: content},
		{name: "stream alias", path: "a.mp3", target: "/v1/songs/s:stream", wantCode: http.StatusOK, wantBody: content},
		{name: "range request", path: "a.mp3", target: "/v1/songs/s/content", rangeHdr: "bytes=0-3", wantCode: http.StatusPartialContent, wantBody: "0123"},
		{name: "traversal outside music dir", path: musicDir + "/../secret.mp3", target: "/v1/songs/s/content", wantCode: http.StatusForbidden},
		{name: "missing file", path: filepath.Join(musicDir, "missing.mp3"), target: "/v1/songs/s/content", wantCode: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeSongRepo{songs: []*models.Song{{ID: "s", Path: tt.path}}}
			header := http.Header{}
			if tt.rangeHdr != "" {
				header.Set("Range", tt.rangeHdr)
			}
			rec := serve(t, songsRouter(repo, musicDir), http.MethodGet, tt.target, "", header)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantCode, rec.Body.String())
			}
			if tt.wantBody != "" && rec.Body.String() != tt.wantBody {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tt.wantBody)
			}
			if tt.wantCode == http.StatusForbidden && strings.Contains(rec.Body.String(), "secret") {
				t.Fatal("forbidden response leaked file content")
			}
		})
	}
}

// A browser <audio> element reads a song progressively, so the response lives
// far longer than the server-wide WriteTimeout that guards the JSON endpoints.
func TestStreamContentOutlivesServerWriteTimeout(t *testing.T) {
	musicDir := t.TempDir()
	songPath := filepath.Join(musicDir, "song.mp3")
	const size = 16 << 20
	f, err := os.Create(songPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	repo := &fakeSongRepo{songs: []*models.Song{{ID: "s", Path: songPath}}}
	router := chi.NewRouter()
	router.Use(middleware.Logger) // production wraps the writer; the fix must reach through it
	RegisterRoutes(router, NewSongsHandler(repo, musicDir), nil, nil)

	srv := httptest.NewUnstartedServer(router)
	srv.Config.WriteTimeout = 250 * time.Millisecond
	srv.Start()
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/v1/songs/s/content")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	buf := make([]byte, 64<<10)
	total := 0
	for {
		n, err := resp.Body.Read(buf)
		total += n
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read after %d of %d bytes: %v", total, size, err)
		}
		time.Sleep(15 * time.Millisecond)
	}
	if total != size {
		t.Fatalf("read %d bytes, want %d", total, size)
	}
}

func TestSongsStreamContentRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevated rights on Windows")
	}
	base := t.TempDir()
	musicDir := filepath.Join(base, "music")
	if err := os.Mkdir(musicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(base, "secret.mp3")
	writeFile(t, secret, "secret")
	link := filepath.Join(musicDir, "link.mp3")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	repo := &fakeSongRepo{songs: []*models.Song{{ID: "s", Path: link}}}
	rec := serve(t, songsRouter(repo, musicDir), http.MethodGet, "/v1/songs/s/content", "", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestSongsStreamContentLookupErrors(t *testing.T) {
	tests := []struct {
		name     string
		repo     *fakeSongRepo
		wantCode int
	}{
		{name: "unknown song", repo: &fakeSongRepo{}, wantCode: http.StatusNotFound},
		{name: "repo error", repo: &fakeSongRepo{err: errors.New("boom")}, wantCode: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, songsRouter(tt.repo, t.TempDir()), http.MethodGet, "/v1/songs/s/content", "", nil)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
		})
	}
}
