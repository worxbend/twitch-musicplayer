package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	id3 "github.com/bogem/id3v2/v2"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"lofi-radio-backend/internal/meta"
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
		{name: "two-byte range", path: "a.mp3", target: "/v1/songs/s/content", rangeHdr: "bytes=0-1", wantCode: http.StatusPartialContent, wantBody: "01"},
		{name: "range past end", path: "a.mp3", target: "/v1/songs/s/content", rangeHdr: "bytes=999999-", wantCode: http.StatusRequestedRangeNotSatisfiable},
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

// Only a path that escapes the music directory is 403. A missing or
// non-file target is 404, and an I/O or permission fault is 500.
func TestSongsStreamContentFileErrors(t *testing.T) {
	musicDir := t.TempDir()
	writeFile(t, filepath.Join(musicDir, "file.mp3"), "x")
	if err := os.Mkdir(filepath.Join(musicDir, "album"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(musicDir, "album", "inner.mp3"), "x")
	writeFile(t, filepath.Join(musicDir, "locked.mp3"), "x")
	lockedDir := filepath.Join(musicDir, "locked")
	if err := os.Mkdir(lockedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(lockedDir, "song.mp3"), "x")

	tests := []struct {
		name     string
		path     string
		chmod    string
		wantCode int
	}{
		{name: "file as parent component", path: "file.mp3/child.mp3", wantCode: http.StatusNotFound},
		{name: "directory", path: "album", wantCode: http.StatusNotFound},
		{name: "unreadable file", path: "locked.mp3", chmod: "locked.mp3", wantCode: http.StatusInternalServerError},
		{name: "unsearchable parent dir", path: "locked/song.mp3", chmod: "locked", wantCode: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.chmod != "" {
				lockForTest(t, filepath.Join(musicDir, tt.chmod))
			}
			repo := &fakeSongRepo{songs: []*models.Song{{ID: "s", Path: tt.path}}}
			rec := serve(t, songsRouter(repo, musicDir), http.MethodGet, "/v1/songs/s/content", "", nil)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantCode, rec.Body.String())
			}
			var body struct {
				Error string `json:"error"`
			}
			decodeBody(t, rec, &body)
			if body.Error == "" {
				t.Fatalf("body %q has no JSON error", rec.Body.String())
			}
		})
	}
}

// lockForTest removes every permission bit from path until the test ends.
func lockForTest(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits do not block this user")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, info.Mode().Perm()); err != nil {
			t.Errorf("restore %s: %v", path, err)
		}
	})
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

// A client that requests a song and then stops reading must not pin the
// handler, its open file and the socket forever.
func TestStreamContentDropsStalledClient(t *testing.T) {
	prev := streamStallTimeout
	streamStallTimeout = 200 * time.Millisecond
	t.Cleanup(func() { streamStallTimeout = prev })

	musicDir := t.TempDir()
	songPath := filepath.Join(musicDir, "song.mp3")
	f, err := os.Create(songPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(64 << 20); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	repo := &fakeSongRepo{songs: []*models.Song{{ID: "s", Path: songPath}}}
	router := chi.NewRouter()
	router.Use(middleware.Logger)
	RegisterRoutes(router, NewSongsHandler(repo, musicDir), nil, nil)
	done := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r)
		done <- struct{}{}
	}))
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() // runs before srv.Close, which waits for the handler
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetReadBuffer(4 << 10)
	}
	if _, err := io.WriteString(conn, "GET /v1/songs/s/content HTTP/1.1\r\nHost: test\r\n\r\n"); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream handler still writing to a client that stopped reading")
	}
}

// The song payload always carries has_cover so clients can skip cover
// requests for songs without art.
func TestSongsGetHasCover(t *testing.T) {
	for _, hasCover := range []bool{true, false} {
		repo := &fakeSongRepo{songs: []*models.Song{{ID: "a", HasCover: hasCover}}}
		rec := serve(t, songsRouter(repo, t.TempDir()), http.MethodGet, "/v1/songs/a", "", nil)
		var got map[string]any
		decodeBody(t, rec, &got)
		if v, ok := got["has_cover"]; !ok || v != hasCover {
			t.Fatalf("has_cover = %v (present %v), want %v", v, ok, hasCover)
		}
	}
}

// writeTaggedSong writes a small file at path with an ID3v2 tag that embeds
// picture as the front cover, or no picture when it is nil.
func writeTaggedSong(t *testing.T, path string, picture []byte) {
	t.Helper()
	writeFile(t, path, strings.Repeat("\x00", 1_000))
	tag, err := id3.Open(path, id3.Options{Parse: false})
	if err != nil {
		t.Fatal(err)
	}
	defer tag.Close()
	tag.SetTitle("Rain Walk")
	if picture != nil {
		tag.AddAttachedPicture(id3.PictureFrame{
			Encoding:    id3.EncodingUTF8,
			MimeType:    "image/png",
			PictureType: id3.PTFrontCover,
			Picture:     picture,
		})
	}
	if err := tag.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestSongsCover(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevated rights on Windows")
	}
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR\x00\x00\x00\x01\x00\x00\x00\x01")
	sum := sha256.Sum256(png)
	wantETag := `"` + hex.EncodeToString(sum[:])[:16] + `"`

	base := t.TempDir()
	musicDir := filepath.Join(base, "music")
	if err := os.Mkdir(musicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTaggedSong(t, filepath.Join(musicDir, "art.mp3"), png)
	writeTaggedSong(t, filepath.Join(musicDir, "plain.mp3"), nil)
	writeTaggedSong(t, filepath.Join(base, "secret.mp3"), png)
	if err := os.Symlink(filepath.Join(base, "secret.mp3"), filepath.Join(musicDir, "link.mp3")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	repo := &fakeSongRepo{songs: []*models.Song{
		{ID: "art", Path: "art.mp3"},
		{ID: "plain", Path: "plain.mp3"},
		{ID: "link", Path: "link.mp3"},
		{ID: "gone", Path: "gone.mp3"},
		{ID: "retag", Path: "retag.mp3"},
	}}
	router := songsRouter(repo, musicDir)

	t.Run("serves embedded image", func(t *testing.T) {
		rec := serve(t, router, http.MethodGet, "/v1/songs/art/cover", "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
		}
		want := map[string]string{
			"Content-Type":           "image/png",
			"ETag":                   wantETag,
			"Cache-Control":          "public, no-cache",
			"X-Content-Type-Options": "nosniff",
		}
		for name, value := range want {
			if got := rec.Header().Get(name); got != value {
				t.Errorf("%s = %q, want %q", name, got, value)
			}
		}
		if !bytes.Equal(rec.Body.Bytes(), png) {
			t.Errorf("body = %q, want the embedded picture", rec.Body.Bytes())
		}
	})

	t.Run("ETag is the indexed cover_version", func(t *testing.T) {
		info := meta.Extract(filepath.Join(musicDir, "art.mp3"))
		rec := serve(t, router, http.MethodGet, "/v1/songs/art/cover", "", nil)
		if got, want := rec.Header().Get("ETag"), `"`+info.CoverVersion+`"`; info.CoverVersion == "" || got != want {
			t.Fatalf("ETag = %q, want %q", got, want)
		}
	})

	t.Run("matching If-None-Match is 304", func(t *testing.T) {
		header := http.Header{"If-None-Match": {wantETag}}
		rec := serve(t, router, http.MethodGet, "/v1/songs/art/cover", "", header)
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
			t.Fatalf("status = %d body %q, want 304 with no body", rec.Code, rec.Body.String())
		}
	})

	t.Run("retagged cover is not served stale", func(t *testing.T) {
		retagPath := filepath.Join(musicDir, "retag.mp3")
		writeTaggedSong(t, retagPath, png)
		first := serve(t, router, http.MethodGet, "/v1/songs/retag/cover", "", nil)
		oldETag := first.Header().Get("ETag")
		if first.Code != http.StatusOK || oldETag != wantETag {
			t.Fatalf("first fetch: status = %d ETag %q, want 200 %q", first.Code, oldETag, wantETag)
		}

		pngB := append(bytes.Clone(png), "\x00\x00\x00\x02"...)
		sumB := sha256.Sum256(pngB)
		wantETagB := `"` + hex.EncodeToString(sumB[:])[:16] + `"`
		writeTaggedSong(t, retagPath, pngB)

		header := http.Header{"If-None-Match": {oldETag}}
		rec := serve(t, router, http.MethodGet, "/v1/songs/retag/cover", "", header)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 after retag", rec.Code)
		}
		if got := rec.Header().Get("ETag"); got != wantETagB {
			t.Errorf("ETag = %q, want %q (old %q)", got, wantETagB, oldETag)
		}
		if !bytes.Equal(rec.Body.Bytes(), pngB) {
			t.Errorf("body = %q, want the retagged picture", rec.Body.Bytes())
		}
	})

	errorCases := []struct {
		name     string
		id       string
		wantCode int
	}{
		{name: "unknown song", id: "nope", wantCode: http.StatusNotFound},
		{name: "song without art", id: "plain", wantCode: http.StatusNotFound},
		{name: "missing file", id: "gone", wantCode: http.StatusNotFound},
		{name: "symlink outside music dir", id: "link", wantCode: http.StatusForbidden},
	}
	for _, tt := range errorCases {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, router, http.MethodGet, "/v1/songs/"+tt.id+"/cover", "", nil)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantCode, rec.Body.String())
			}
			if rec.Header().Get("Content-Type") != "application/json" || bytes.Contains(rec.Body.Bytes(), []byte("PNG")) {
				t.Fatalf("error response leaked image: %q %q", rec.Header().Get("Content-Type"), rec.Body.String())
			}
		})
	}
}
