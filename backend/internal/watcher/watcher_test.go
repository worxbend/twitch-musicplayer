package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	id3 "github.com/bogem/id3v2/v2"
	"github.com/google/uuid"
	"lofi-radio-backend/internal/models"
	"lofi-radio-backend/internal/played"
	"lofi-radio-backend/internal/queue"
	"lofi-radio-backend/internal/repository"
)

type harness struct {
	dir      string
	songs    *repository.MemSongRepo
	queue    *repository.MemQueueRepo
	mgr      *queue.Manager
	w        *Watcher
	removals atomic.Int32
	updates  atomic.Int32
}

// startWatcher indexes dir, then runs Start until the test ends.
func startWatcher(t *testing.T, dir string) *harness {
	t.Helper()
	return startWatcherWith(t, dir, nil)
}

// startWatcherWith is startWatcher with configure applied to the Watcher
// before it scans or starts, so per-test overrides do not race.
func startWatcherWith(t *testing.T, dir string, configure func(*Watcher)) *harness {
	t.Helper()
	h := &harness{dir: dir, songs: repository.NewMemSongRepo()}
	h.queue = repository.NewMemQueueRepo(h.songs)
	h.mgr = queue.New(h.songs, played.New(100, 0.01), h.queue, queue.ManagerConfig{
		Fill: queue.FillConfig{Strategy: queue.FillManualOnly},
	})
	w, err := New(dir, []string{".mp3"}, h.songs, h.mgr, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w.OnRemoved = func(_ []*models.Song) { h.removals.Add(1) }
	w.OnUpdated = func(_ *models.Song) { h.updates.Add(1) }
	w.settlePoll = 300 * time.Millisecond
	if configure != nil {
		configure(w)
	}
	h.w = w

	ctx, cancel := context.WithCancel(context.Background())
	if _, err := w.ScanExisting(ctx); err != nil {
		t.Fatalf("ScanExisting: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- w.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Start: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Start did not return after cancel")
		}
	})
	return h
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("not an id3 file"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func (h *harness) count(t *testing.T) int {
	t.Helper()
	n, err := h.songs.Count(context.Background())
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	return n
}

func (h *harness) queueAll(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	list, _ := h.songs.List(ctx)
	for _, s := range list {
		if _, err := h.mgr.AddToQueue(ctx, s.ID, models.QueueSourceManual); err != nil {
			t.Fatalf("AddToQueue: %v", err)
		}
	}
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWatcherDropsDeletedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.mp3")
	writeFile(t, path)
	h := startWatcher(t, dir)
	if h.count(t) != 1 {
		t.Fatalf("indexed %d songs, want 1", h.count(t))
	}
	h.queueAll(t)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 2*time.Second, "song removed", func() bool { return h.count(t) == 0 })
	waitFor(t, time.Second, "OnRemoved", func() bool { return h.removals.Load() > 0 })
	if n, _ := h.queue.Count(context.Background()); n != 0 {
		t.Fatalf("queue depth = %d, want 0 after file removal", n)
	}
}

func TestWatcherRenameReplacesEntry(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "a.mp3")
	writeFile(t, oldPath)
	h := startWatcher(t, dir)

	if err := os.Rename(oldPath, filepath.Join(dir, "b.mp3")); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 3*time.Second, "only b.mp3 indexed", func() bool {
		list, _ := h.songs.List(context.Background())
		return len(list) == 1 && strings.HasSuffix(list[0].Path, "b.mp3")
	})
	if h.removals.Load() == 0 {
		t.Fatal("OnRemoved not called for rename")
	}
}

func TestWatcherDropsRemovedDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sub, "x.mp3"))
	writeFile(t, filepath.Join(dir, "keep.mp3"))
	h := startWatcher(t, dir)
	if h.count(t) != 2 {
		t.Fatalf("indexed %d songs, want 2", h.count(t))
	}

	if err := os.RemoveAll(sub); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 2*time.Second, "sub/x.mp3 removed", func() bool { return h.count(t) == 1 })
	if ok, _ := h.songs.ExistsByPath(context.Background(), filepath.Join(dir, "keep.mp3")); !ok {
		t.Fatal("keep.mp3 was dropped with the removed directory")
	}
}

// chdirMusic switches into a fresh temp dir holding an empty "music" folder,
// matching the default relative config dir.
func chdirMusic(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	if err := os.Mkdir("music", 0o755); err != nil {
		t.Fatal(err)
	}
}

func assertAbsoluteSong(t *testing.T, h *harness, rel string) {
	t.Helper()
	want, err := filepath.Abs(rel)
	if err != nil {
		t.Fatal(err)
	}
	wantID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(want)).String()
	song, err := h.songs.GetByID(context.Background(), wantID)
	if err != nil {
		list, _ := h.songs.List(context.Background())
		t.Fatalf("no song with UUIDv5 of %s: %v (indexed: %d)", want, err, len(list))
	}
	if song.Path != want || !filepath.IsAbs(song.Path) {
		t.Fatalf("Path = %q, want absolute %q", song.Path, want)
	}
}

func TestWatcherStoresAbsolutePaths(t *testing.T) {
	chdirMusic(t)
	writeFile(t, filepath.Join("music", "a.mp3"))

	h := startWatcher(t, "./music")

	assertAbsoluteSong(t, h, filepath.Join("music", "a.mp3"))
}

func TestWatcherStoresAbsolutePathsForNewFiles(t *testing.T) {
	chdirMusic(t)
	h := startWatcher(t, "./music")

	writeFile(t, filepath.Join("music", "b.mp3"))

	waitFor(t, 2*time.Second, "b.mp3 indexed", func() bool { return h.count(t) == 1 })
	assertAbsoluteSong(t, h, filepath.Join("music", "b.mp3"))
}

func TestWatcherIndexesMovedNestedDirectory(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	nested := filepath.Join(outside, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(nested, "c.mp3"))
	h := startWatcher(t, dir)

	if err := os.Rename(filepath.Join(outside, "a"), filepath.Join(dir, "a")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "a/b/c.mp3 indexed", func() bool { return h.count(t) == 1 })

	writeFile(t, filepath.Join(dir, "a", "b", "d.mp3"))
	waitFor(t, 3*time.Second, "a/b/d.mp3 indexed", func() bool { return h.count(t) == 2 })
}

// lockedDir creates parent/name holding one music file, then removes every
// permission on it. Root ignores permissions, so the test is skipped as root.
func lockedDir(t *testing.T, parent, name string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	locked := filepath.Join(parent, name)
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(locked, "hidden.mp3"))
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
}

func TestScanExistingSkipsUnreadableDir(t *testing.T) {
	dir := t.TempDir()
	lockedDir(t, dir, "a_locked")
	if err := os.Mkdir(filepath.Join(dir, "z"), 0o755); err != nil {
		t.Fatal(err)
	}
	song := filepath.Join(dir, "z", "song.mp3")
	writeFile(t, song)

	h := startWatcher(t, dir)

	if n := h.count(t); n != 1 {
		t.Fatalf("indexed %d songs, want 1", n)
	}
	if ok, _ := h.songs.ExistsByPath(context.Background(), song); !ok {
		t.Fatal("z/song.mp3 not indexed")
	}
}

func TestScanExistingFailsWhenRootIsMissing(t *testing.T) {
	dir := t.TempDir()
	songs := repository.NewMemSongRepo()
	w, err := New(dir, nil, songs, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { w.fsWatcher.Close() })
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}

	if _, err := w.ScanExisting(context.Background()); err == nil {
		t.Fatal("ScanExisting on a missing root returned nil error")
	}
}

func TestWatcherIndexesReadableFilesInDroppedTreeWithUnreadableDir(t *testing.T) {
	dir := t.TempDir()
	tree := filepath.Join(t.TempDir(), "drop")
	if err := os.MkdirAll(filepath.Join(tree, "ok"), 0o755); err != nil {
		t.Fatal(err)
	}
	lockedDir(t, tree, "locked")
	writeFile(t, filepath.Join(tree, "ok", "song.mp3"))
	h := startWatcher(t, dir)

	if err := os.Rename(tree, filepath.Join(dir, "drop")); err != nil {
		t.Fatal(err)
	}
	// lockedDir restores the old path; TempDir cleanup needs the moved one open too.
	t.Cleanup(func() { os.Chmod(filepath.Join(dir, "drop", "locked"), 0o755) })
	song := filepath.Join(dir, "drop", "ok", "song.mp3")
	waitFor(t, 3*time.Second, "drop/ok/song.mp3 indexed", func() bool {
		ok, _ := h.songs.ExistsByPath(context.Background(), song)
		return ok
	})
}

func TestWatcherWaitsForFileToSettle(t *testing.T) {
	const chunk = 64 << 10
	dir := t.TempDir()
	// Writes 100ms apart against a 600ms poll: a 6x margin for loaded runners.
	h := startWatcherWith(t, dir, func(w *Watcher) { w.settlePoll = 600 * time.Millisecond })
	path := filepath.Join(dir, "slow.mp3")

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, chunk)
	for i := range 4 {
		if i > 0 {
			time.Sleep(100 * time.Millisecond)
		}
		if _, err := f.Write(buf); err != nil {
			f.Close()
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 5*time.Second, "slow.mp3 indexed", func() bool { return h.count(t) == 1 })
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte(path)).String()
	song, err := h.songs.GetByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if song.SizeBytes != 4*chunk {
		t.Fatalf("SizeBytes = %d, want %d", song.SizeBytes, 4*chunk)
	}
}

func TestWatcherCapsConcurrentSettling(t *testing.T) {
	const files = 10
	dir := t.TempDir()
	var active, peak atomic.Int32
	h := startWatcherWith(t, dir, func(w *Watcher) {
		w.settleSem = make(chan struct{}, 2)
		w.settleWait = func(ctx context.Context, path string) error {
			n := active.Add(1)
			defer active.Add(-1)
			for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
			}
			time.Sleep(50 * time.Millisecond)
			return w.waitSettled(ctx, path)
		}
	})

	burst := filepath.Join(dir, "burst")
	if err := os.Mkdir(burst, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range files {
		writeFile(t, filepath.Join(burst, fmt.Sprintf("%02d.mp3", i)))
	}

	waitFor(t, 10*time.Second, "burst indexed", func() bool { return h.count(t) == files })
	if p := peak.Load(); p < 1 || p > 2 {
		t.Fatalf("peak concurrent settle waits = %d, want 1..2", p)
	}
}

// taggedMP3 returns audio-ish bytes behind an ID3v2 tag titled title. The tag
// is built outside the library: id3v2's Save renames a temp file over its target.
func taggedMP3(t *testing.T, title string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tagged.mp3")
	if err := os.WriteFile(path, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	tag, err := id3.Open(path, id3.Options{Parse: false})
	if err != nil {
		t.Fatalf("id3 open: %v", err)
	}
	tag.SetTitle(title)
	if err := tag.Save(); err != nil {
		tag.Close()
		t.Fatalf("id3 save: %v", err)
	}
	if err := tag.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (h *harness) song(t *testing.T, path string) *models.Song {
	t.Helper()
	song, err := h.songs.GetByID(context.Background(), uuid.NewSHA1(uuid.NameSpaceURL, []byte(path)).String())
	if err != nil {
		t.Fatalf("GetByID(%s): %v", path, err)
	}
	return song
}

func (h *harness) queueDepth(t *testing.T) int {
	t.Helper()
	n, err := h.queue.Count(context.Background())
	if err != nil {
		t.Fatalf("queue Count: %v", err)
	}
	return n
}

func TestWatcherRefreshesRetaggedFileInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.mp3")
	if err := os.WriteFile(path, taggedMP3(t, "Old"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := startWatcher(t, dir)
	h.queueAll(t)
	before := h.song(t, path)

	if err := os.WriteFile(path, taggedMP3(t, "New"), 0o644); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 3*time.Second, "title refreshed", func() bool { return h.song(t, path).Title == "New" })
	if got := h.song(t, path).AddedAt; !got.Equal(before.AddedAt) {
		t.Fatalf("AddedAt = %v, want unchanged %v", got, before.AddedAt)
	}
	if n := h.queueDepth(t); n != 1 {
		t.Fatalf("queue depth = %d, want 1 (refresh must not re-queue)", n)
	}
}

func TestWatcherRefreshesFileReplacedByRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.mp3")
	if err := os.WriteFile(path, taggedMP3(t, "Old"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := startWatcher(t, dir)
	h.queueAll(t)

	tmp := filepath.Join(dir, ".a.mp3.tmp")
	if err := os.WriteFile(tmp, taggedMP3(t, "New"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 3*time.Second, "title refreshed", func() bool { return h.song(t, path).Title == "New" })
	if n := h.count(t); n != 1 {
		t.Fatalf("indexed %d songs, want 1", n)
	}
	if n := h.queueDepth(t); n != 1 {
		t.Fatalf("queue depth = %d, want 1 (refresh must not re-queue)", n)
	}
}

func TestWatcherOnUpdatedFiresOnlyWhenMetadataChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.mp3")
	old := taggedMP3(t, "Old")
	if err := os.WriteFile(path, old, 0o644); err != nil {
		t.Fatal(err)
	}
	h := startWatcher(t, dir)

	if err := os.WriteFile(path, old, 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * h.w.settlePoll)
	if n := h.updates.Load(); n != 0 {
		t.Fatalf("OnUpdated fired %d times for identical bytes, want 0", n)
	}

	if err := os.WriteFile(path, taggedMP3(t, "New"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "OnUpdated", func() bool { return h.updates.Load() > 0 })
	time.Sleep(3 * h.w.settlePoll)
	if n := h.updates.Load(); n != 1 {
		t.Fatalf("OnUpdated fired %d times, want 1", n)
	}
}

// deleteOnGetRepo deletes the song right after GetByID returns it, putting a
// watcher Remove event deterministically inside refreshFile's tag re-read.
type deleteOnGetRepo struct {
	*repository.MemSongRepo
}

func (r deleteOnGetRepo) GetByID(ctx context.Context, id string) (*models.Song, error) {
	song, err := r.MemSongRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, _, err := r.DeleteByPath(ctx, song.Path); err != nil {
		return nil, err
	}
	return song, nil
}

func TestWatcherRefreshDoesNotReviveSongDeletedMidRefresh(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.mp3")
	if err := os.WriteFile(path, taggedMP3(t, "Old"), 0o644); err != nil {
		t.Fatal(err)
	}
	songs := repository.NewMemSongRepo()
	mgr := queue.New(songs, played.New(100, 0.01), repository.NewMemQueueRepo(songs), queue.ManagerConfig{
		Fill: queue.FillConfig{Strategy: queue.FillManualOnly},
	})
	w, err := New(dir, []string{".mp3"}, deleteOnGetRepo{songs}, mgr, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { w.fsWatcher.Close() }) // Start, which owns the close, never runs.
	var updates atomic.Int32
	w.OnUpdated = func(_ *models.Song) { updates.Add(1) }
	if _, err := w.ScanExisting(ctx); err != nil {
		t.Fatalf("ScanExisting: %v", err)
	}
	if err := os.WriteFile(path, taggedMP3(t, "New"), 0o644); err != nil {
		t.Fatal(err)
	}

	song, err := w.refreshFile(ctx, path)
	if song != nil || err != nil {
		t.Fatalf("refreshFile = (%v, %v), want (nil, nil)", song, err)
	}
	if n, _ := songs.Count(ctx); n != 0 {
		t.Fatalf("Count = %d, want 0 (deleted song revived)", n)
	}
	if ok, _ := songs.ExistsByPath(ctx, path); ok {
		t.Fatal("ExistsByPath = true, want false (deleted song revived)")
	}
	if n := updates.Load(); n != 0 {
		t.Fatalf("OnUpdated fired %d times, want 0", n)
	}
}
