package watcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	id3 "github.com/bogem/id3v2/v2"
	"github.com/fsnotify/fsnotify"
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
	// removedSongs sums the songs carried by every OnRemoved call.
	removedSongs atomic.Int32
	updates      atomic.Int32
}

func (h *harness) recordRemoval(songs []*models.Song) {
	h.removals.Add(1)
	h.removedSongs.Add(int32(len(songs)))
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
	w.OnRemoved = h.recordRemoval
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

func TestWatcherDropsMovedFolderNamedLikeMusicFile(t *testing.T) {
	moves := map[string]func(t *testing.T, folder string) error{
		"removed": func(_ *testing.T, folder string) error { return os.RemoveAll(folder) },
		"renamed out": func(t *testing.T, folder string) error {
			return os.Rename(folder, filepath.Join(t.TempDir(), "Beats.mp3"))
		},
	}
	for name, move := range moves {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			folder := filepath.Join(dir, "Beats.mp3")
			if err := os.Mkdir(folder, 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(folder, "a.mp3"))
			h := startWatcher(t, dir)
			if n := h.count(t); n != 1 {
				t.Fatalf("indexed %d songs, want 1", n)
			}

			if err := move(t, folder); err != nil {
				t.Fatal(err)
			}

			waitFor(t, 3*time.Second, "Beats.mp3/a.mp3 removed", func() bool { return h.count(t) == 0 })
			time.Sleep(3 * h.w.settlePoll)
			if n := h.removals.Load(); n != 1 {
				t.Fatalf("OnRemoved fired %d times, want 1", n)
			}
		})
	}
}

func TestWatcherRewatchesAfterDirectoryRemoval(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sub, "a.mp3"))
	old := filepath.Join(dir, "old")
	if err := os.Mkdir(old, 0o755); err != nil {
		t.Fatal(err)
	}
	h := startWatcher(t, dir)
	// Stands in for fsnotify dropping the watch a renamed folder shares by wd.
	if err := h.w.fsWatcher.Remove(sub); err != nil {
		t.Fatal(err)
	}

	h.w.handleEvent(t.Context(), fsnotify.Event{Name: old, Op: fsnotify.Rename})

	waitFor(t, 3*time.Second, "sub watched again", func() bool {
		return slices.Contains(h.w.fsWatcher.WatchList(), sub)
	})
	writeFile(t, filepath.Join(sub, "new.mp3"))
	waitFor(t, 3*time.Second, "sub/new.mp3 indexed", func() bool {
		return h.indexed(t, filepath.Join("sub", "new.mp3"))
	})
}

// unwatch removes folder's watch, so only a rewatch walk can put it back.
func (h *harness) unwatch(t *testing.T, folder string) {
	t.Helper()
	if err := h.w.fsWatcher.Remove(folder); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) watching(folder string) bool {
	return slices.Contains(h.w.fsWatcher.WatchList(), folder)
}

func TestWatcherDeletedNonMusicFileStartsNoRewatch(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "sentinel")
	if err := os.Mkdir(sentinel, 0o755); err != nil {
		t.Fatal(err)
	}
	cover := filepath.Join(dir, "cover.jpg")
	writeFile(t, cover)
	marker := filepath.Join(dir, "marker.mp3")
	writeFile(t, marker)
	h := startWatcher(t, dir)
	h.unwatch(t, sentinel)

	if err := os.Remove(cover); err != nil {
		t.Fatal(err)
	}
	// The marker's later Remove proves the cover's event was handled.
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "marker.mp3 removed", func() bool { return h.count(t) == 0 })
	time.Sleep(h.w.settlePoll)
	if h.watching(sentinel) {
		t.Fatal("deleting cover.jpg re-walked the library")
	}
}

func TestWatcherRewatchesAfterRenamingFolderNamedLikeMusicFile(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "sentinel")
	if err := os.Mkdir(sentinel, 0o755); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(dir, "Beats.mp3")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(folder, "a.mp3"))
	h := startWatcher(t, dir)
	h.unwatch(t, sentinel)

	renamed := filepath.Join(dir, "Beats2")
	if err := os.Rename(folder, renamed); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 3*time.Second, "rewatch after the folder rename", func() bool { return h.watching(sentinel) })
	writeFile(t, filepath.Join(renamed, "new.mp3"))
	waitFor(t, 3*time.Second, "Beats2/new.mp3 indexed", func() bool {
		return h.indexed(t, filepath.Join("Beats2", "new.mp3"))
	})
	if h.indexed(t, filepath.Join("Beats.mp3", "a.mp3")) {
		t.Fatal("Beats.mp3/a.mp3 still indexed after the folder rename")
	}
}

func TestWatcherIndexesFileInRepeatedlyRenamedFolder(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test")
	}
	dir := t.TempDir()
	folder := filepath.Join(dir, "r0")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	h := startWatcher(t, dir)

	for i := 1; i <= 10; i++ {
		next := filepath.Join(dir, fmt.Sprintf("r%d", i))
		if err := os.Rename(folder, next); err != nil {
			t.Fatal(err)
		}
		folder = next
	}
	writeFile(t, filepath.Join(folder, "new.mp3"))

	waitFor(t, 5*time.Second, "new.mp3 indexed", func() bool {
		return h.indexed(t, filepath.Join("r10", "new.mp3"))
	})
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
	return taggedMP3WithCover(t, title, nil)
}

// taggedMP3WithCover is taggedMP3 that also embeds picture as the front
// cover when it is non-nil.
func taggedMP3WithCover(t *testing.T, title string, picture []byte) []byte {
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
	if picture != nil {
		tag.AddAttachedPicture(id3.PictureFrame{
			Encoding:    id3.EncodingUTF8,
			MimeType:    "image/png",
			PictureType: id3.PTFrontCover,
			Picture:     picture,
		})
	}
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

// The song repo is in memory, so a restart re-creates every song: AddedAt
// must come from the file, not the boot time.
func TestScanExistingTakesAddedAtFromModTime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.mp3")
	writeFile(t, path)
	mtime := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	h := startWatcher(t, dir)

	if got := h.song(t, path).AddedAt.Truncate(time.Second); !got.Equal(mtime) {
		t.Fatalf("AddedAt = %v, want modtime %v", got, mtime)
	}
}

func TestScanExistingCapsFutureModTime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "future.mp3")
	writeFile(t, path)
	mtime := time.Now().Add(48 * time.Hour)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	h := startWatcher(t, dir)

	if got := h.song(t, path).AddedAt; got.After(time.Now()) {
		t.Fatalf("AddedAt = %v, want capped at now", got)
	}
}

func TestWatcherStampsDiscoveryTimeOnNewFile(t *testing.T) {
	dir := t.TempDir()
	h := startWatcher(t, dir)
	path := filepath.Join(dir, "new.mp3")
	writeFile(t, path)
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 5*time.Second, "new file indexed", func() bool { return h.indexed(t, "new.mp3") })
	if age := time.Since(h.song(t, path).AddedAt); age < 0 || age > 10*time.Second {
		t.Fatalf("AddedAt age = %v, want within the last 10s", age)
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

// A retag that swaps only the embedded picture keeps every other field,
// size included, so only cover_version tells the songs apart.
func TestWatcherOnUpdatedFiresForCoverOnlyRetag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.mp3")
	pngA := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR\x00\x00\x00\x01\x00\x00\x00\x01")
	pngB := bytes.Clone(pngA)
	pngB[len(pngB)-1] = 0x02
	if err := os.WriteFile(path, taggedMP3WithCover(t, "Same", pngA), 0o644); err != nil {
		t.Fatal(err)
	}
	h := startWatcher(t, dir)
	before := h.song(t, path)
	if before.CoverVersion == "" {
		t.Fatal("indexed song has no cover_version")
	}

	retagged := taggedMP3WithCover(t, "Same", pngB)
	if err := os.WriteFile(path, retagged, 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "OnUpdated", func() bool { return h.updates.Load() > 0 })
	time.Sleep(3 * h.w.settlePoll)
	if n := h.updates.Load(); n != 1 {
		t.Fatalf("OnUpdated fired %d times, want 1", n)
	}
	after := h.song(t, path)
	if after.CoverVersion == before.CoverVersion {
		t.Fatalf("cover_version = %q, want it changed", after.CoverVersion)
	}
	if after.Title != before.Title || after.SizeBytes != before.SizeBytes {
		t.Fatalf("title/size = %q/%d, want unchanged %q/%d", after.Title, after.SizeBytes, before.Title, before.SizeBytes)
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

// removeOnCreateRepo deletes the file right before the song is inserted,
// putting a watcher Remove event deterministically between Stat and Create.
type removeOnCreateRepo struct {
	*repository.MemSongRepo
}

func (r removeOnCreateRepo) Create(ctx context.Context, song *models.Song) error {
	if err := os.Remove(song.Path); err != nil {
		return err
	}
	return r.MemSongRepo.Create(ctx, song)
}

// registerHarness writes a.mp3 and builds an unstarted watcher over repo that
// counts OnNewSong calls.
func registerHarness(t *testing.T, songs *repository.MemSongRepo, repo SongRepository) (*Watcher, *queue.Manager, string, *atomic.Int32) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.mp3")
	if err := os.WriteFile(path, taggedMP3(t, "A"), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := queue.New(songs, played.New(100, 0.01), repository.NewMemQueueRepo(songs), queue.ManagerConfig{
		Fill: queue.FillConfig{Strategy: queue.FillManualOnly},
	})
	var newSongs atomic.Int32
	w, err := New(dir, []string{".mp3"}, repo, mgr, func(_ *models.Song) { newSongs.Add(1) })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { w.fsWatcher.Close() }) // Start, which owns the close, never runs.
	return w, mgr, path, &newSongs
}

func queueLen(t *testing.T, mgr *queue.Manager) int {
	t.Helper()
	items, err := mgr.ListQueue(context.Background())
	if err != nil {
		t.Fatalf("ListQueue: %v", err)
	}
	return len(items)
}

func TestWatcherRegisterFileDropsSongDeletedBeforeCreate(t *testing.T) {
	ctx := context.Background()
	songs := repository.NewMemSongRepo()
	w, mgr, path, newSongs := registerHarness(t, songs, removeOnCreateRepo{songs})

	song, created, err := w.registerFile(ctx, path, true)
	if song != nil || created || err != nil {
		t.Fatalf("registerFile = (%v, %v, %v), want (nil, false, nil)", song, created, err)
	}
	if ok, _ := songs.ExistsByPath(ctx, path); ok {
		t.Fatal("ExistsByPath = true, want false (ghost song indexed)")
	}
	if n := queueLen(t, mgr); n != 0 {
		t.Fatalf("queue length = %d, want 0 (ghost song queued)", n)
	}
	if n := newSongs.Load(); n != 0 {
		t.Fatalf("OnNewSong fired %d times, want 0", n)
	}
}

func TestWatcherRegisterFileQueuesNewSongOnce(t *testing.T) {
	ctx := context.Background()
	songs := repository.NewMemSongRepo()
	w, mgr, path, newSongs := registerHarness(t, songs, songs)

	song, created, err := w.registerFile(ctx, path, true)
	if song == nil || !created || err != nil {
		t.Fatalf("registerFile = (%v, %v, %v), want (song, true, nil)", song, created, err)
	}
	if ok, _ := songs.ExistsByPath(ctx, path); !ok {
		t.Fatal("ExistsByPath = false, want true")
	}
	if n := queueLen(t, mgr); n != 1 {
		t.Fatalf("queue length = %d, want 1", n)
	}
	if n := newSongs.Load(); n != 1 {
		t.Fatalf("OnNewSong fired %d times, want 1", n)
	}
}

// overflowGone lists the files overflowHarness deletes behind the watcher.
var overflowGone = []string{"b.mp3", "e.mp3", "f.mp3"}

// overflowHarness indexes and queues a.mp3 and overflowGone with a watcher
// that is never started, so no fsnotify event is consumed, then deletes
// overflowGone, adds c.mp3 and adds sub/d.mp3 behind its back.
func overflowHarness(t *testing.T) *harness {
	t.Helper()
	return overflowHarnessWith(t, newGatedListRepo())
}

// overflowHarnessWith is overflowHarness with songs as the watcher's and the
// queue's song repository. songs is armed by the caller, after the setup.
func overflowHarnessWith(t *testing.T, songs *gatedListRepo) *harness {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.mp3"))
	for _, rel := range overflowGone {
		writeFile(t, filepath.Join(dir, rel))
	}
	h := &harness{dir: dir, songs: songs.MemSongRepo}
	h.queue = repository.NewMemQueueRepo(h.songs)
	h.mgr = queue.New(songs, played.New(100, 0.01), h.queue, queue.ManagerConfig{
		Fill: queue.FillConfig{Strategy: queue.FillManualOnly},
	})
	w, err := New(dir, []string{".mp3"}, songs, h.mgr, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { w.fsWatcher.Close() }) // Start, which owns the close, never runs.
	w.OnRemoved = h.recordRemoval
	w.settleWait = func(context.Context, string) error { return nil }
	h.w = w
	if _, err := w.ScanExisting(context.Background()); err != nil {
		t.Fatalf("ScanExisting: %v", err)
	}
	h.queueAll(t)

	for _, rel := range overflowGone {
		if err := os.Remove(filepath.Join(dir, rel)); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(dir, "c.mp3"))
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "sub", "d.mp3"))
	return h
}

func (h *harness) indexed(t *testing.T, rel string) bool {
	t.Helper()
	ok, err := h.songs.ExistsByPath(context.Background(), filepath.Join(h.dir, rel))
	if err != nil {
		t.Fatalf("ExistsByPath %s: %v", rel, err)
	}
	return ok
}

func TestOverflowResync(t *testing.T) {
	h := overflowHarness(t)
	ctx := context.Background()

	// A second overflow while the first resync runs must not double-remove.
	h.w.handleError(ctx, fsnotify.ErrEventOverflow)
	h.w.handleError(ctx, fsnotify.ErrEventOverflow)
	h.w.settling.Wait()

	for _, rel := range []string{"a.mp3", "c.mp3", filepath.Join("sub", "d.mp3")} {
		if !h.indexed(t, rel) {
			t.Errorf("%s not indexed after resync", rel)
		}
	}
	for _, rel := range overflowGone {
		if h.indexed(t, rel) {
			t.Errorf("deleted %s still indexed after resync", rel)
		}
	}
	if n := h.removals.Load(); n != 1 {
		t.Errorf("OnRemoved fired %d times, want 1 for the whole sweep", n)
	}
	if n := h.removedSongs.Load(); n != int32(len(overflowGone)) {
		t.Errorf("OnRemoved carried %d songs, want %d", n, len(overflowGone))
	}
	h.assertNotQueued(t, overflowGone...)
	if sub := filepath.Join(h.dir, "sub"); !slices.Contains(h.w.fsWatcher.WatchList(), sub) {
		t.Errorf("%s not watched after resync", sub)
	}
}

// gatedListRepo is a MemSongRepo whose List, once armed, reports entry on
// entered and blocks until release is closed. cur and peak count the List
// calls in progress, so a test can catch two resyncs sweeping at once.
type gatedListRepo struct {
	*repository.MemSongRepo
	armed     atomic.Bool
	entered   chan struct{}
	release   chan struct{}
	cur, peak atomic.Int32
}

func newGatedListRepo() *gatedListRepo {
	return &gatedListRepo{
		MemSongRepo: repository.NewMemSongRepo(),
		entered:     make(chan struct{}, 4),
		release:     make(chan struct{}),
	}
}

func (r *gatedListRepo) List(ctx context.Context) ([]*models.Song, error) {
	if r.armed.Load() {
		n := r.cur.Add(1)
		for {
			p := r.peak.Load()
			if n <= p || r.peak.CompareAndSwap(p, n) {
				break
			}
		}
		select {
		case r.entered <- struct{}{}:
		default:
		}
		<-r.release
		r.cur.Add(-1)
	}
	return r.MemSongRepo.List(ctx)
}

// TestOverflowDuringResyncQueuesAnother holds the first resync in its sweep,
// after its walk, while a file appears and a second overflow lands. The
// second overflow must run one more resync, after the first, to index it.
func TestOverflowDuringResyncQueuesAnother(t *testing.T) {
	songs := newGatedListRepo()
	h := overflowHarnessWith(t, songs)
	ctx := context.Background()
	songs.armed.Store(true)

	h.w.handleError(ctx, fsnotify.ErrEventOverflow)
	select {
	case <-songs.entered:
	case <-time.After(5 * time.Second):
		close(songs.release)
		t.Fatal("first resync never reached its sweep")
	}
	writeFile(t, filepath.Join(h.dir, "g.mp3"))
	h.w.handleError(ctx, fsnotify.ErrEventOverflow)
	select {
	case <-songs.entered:
	case <-time.After(500 * time.Millisecond):
	}
	close(songs.release)
	h.w.settling.Wait()

	if !h.indexed(t, "g.mp3") {
		t.Error("g.mp3, added during the first resync, not indexed")
	}
	if n := songs.peak.Load(); n != 1 {
		t.Errorf("%d resync sweeps ran at once, want 1", n)
	}
	for _, rel := range overflowGone {
		if h.indexed(t, rel) {
			t.Errorf("deleted %s still indexed after resync", rel)
		}
	}
	if n := h.removals.Load(); n != 1 {
		t.Errorf("OnRemoved fired %d times, want 1 for the whole sweep", n)
	}
	if n := h.removedSongs.Load(); n != int32(len(overflowGone)) {
		t.Errorf("OnRemoved carried %d songs, want %d", n, len(overflowGone))
	}
}

// assertNotQueued fails if any song at a path relative to h.dir is queued.
func (h *harness) assertNotQueued(t *testing.T, rels ...string) {
	t.Helper()
	items, err := h.mgr.ListQueue(context.Background())
	if err != nil {
		t.Fatalf("ListQueue: %v", err)
	}
	for _, rel := range rels {
		id := uuid.NewSHA1(uuid.NameSpaceURL, []byte(filepath.Join(h.dir, rel))).String()
		if slices.ContainsFunc(items, func(it *models.QueueItem) bool { return it.SongID == id }) {
			t.Errorf("%s still queued", rel)
		}
	}
}

func TestNonOverflowErrorNoResync(t *testing.T) {
	h := overflowHarness(t)

	h.w.handleError(context.Background(), errors.New("boom"))
	h.w.settling.Wait()

	if !h.indexed(t, "b.mp3") {
		t.Error("b.mp3 dropped without an overflow")
	}
	if h.indexed(t, "c.mp3") {
		t.Error("c.mp3 indexed without an overflow")
	}
	if n := h.removals.Load(); n != 0 {
		t.Errorf("OnRemoved fired %d times, want 0", n)
	}
}

// symlinkOrSkip creates link -> target, skipping where symlinks are unavailable.
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need extra privileges on windows")
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
}

// scanWatcher builds an unstarted watcher over dir and runs ScanExisting.
func scanWatcher(t *testing.T, dir string) (*harness, *ScanSummary) {
	t.Helper()
	h := &harness{dir: dir, songs: repository.NewMemSongRepo()}
	w, err := New(dir, []string{".mp3"}, h.songs, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { w.fsWatcher.Close() }) // Start, which owns the close, never runs.
	h.w = w
	summary, err := w.ScanExisting(context.Background())
	if err != nil {
		t.Fatalf("ScanExisting: %v", err)
	}
	return h, summary
}

func TestScanExistingSkipsDanglingSymlinkAndKeepsScanning(t *testing.T) {
	dir := t.TempDir()
	symlinkOrSkip(t, filepath.Join(dir, "missing.mp3"), filepath.Join(dir, "a.mp3"))
	writeFile(t, filepath.Join(dir, "b.mp3"))

	h, summary := scanWatcher(t, dir)

	if !h.indexed(t, "b.mp3") {
		t.Error("b.mp3 after a dangling symlink was not indexed")
	}
	if h.indexed(t, "a.mp3") {
		t.Error("dangling a.mp3 was indexed")
	}
	if summary.Skipped != 1 || summary.MusicFiles != 1 {
		t.Errorf("Skipped=%d MusicFiles=%d, want 1 and 1", summary.Skipped, summary.MusicFiles)
	}
}

func TestScanExistingSkipsSymlinkOutsideLibrary(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "x.mp3")
	writeFile(t, outside)
	symlinkOrSkip(t, outside, filepath.Join(dir, "out.mp3"))

	h, summary := scanWatcher(t, dir)

	if h.indexed(t, "out.mp3") {
		t.Error("out.mp3 pointing outside the library was indexed")
	}
	if summary.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", summary.Skipped)
	}
}

func TestScanExistingIndexesSymlinkInsideLibrary(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "sub", "real.mp3")
	writeFile(t, target)
	symlinkOrSkip(t, target, filepath.Join(dir, "in.mp3"))

	h, summary := scanWatcher(t, dir)

	if !h.indexed(t, "in.mp3") {
		t.Error("in.mp3 pointing inside the library was not indexed")
	}
	if summary.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0", summary.Skipped)
	}
}

func TestScanExistingIndexesSymlinkedRoot(t *testing.T) {
	realDir := t.TempDir()
	writeFile(t, filepath.Join(realDir, "a.mp3"))
	link := filepath.Join(t.TempDir(), "music")
	symlinkOrSkip(t, realDir, link)

	h, _ := scanWatcher(t, link)

	if !h.indexed(t, "a.mp3") {
		t.Error("a.mp3 under a symlinked music root was not indexed at its link path")
	}
}

func TestWatcherIgnoresNewSymlinkOutsideLibrary(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "x.mp3")
	writeFile(t, outside)
	h := startWatcher(t, dir)

	symlinkOrSkip(t, outside, filepath.Join(dir, "out.mp3"))
	writeFile(t, filepath.Join(dir, "z.mp3"))

	waitFor(t, 3*time.Second, "z.mp3 indexed", func() bool { return h.indexed(t, "z.mp3") })
	time.Sleep(3 * h.w.settlePoll)
	if h.indexed(t, "out.mp3") {
		t.Error("new out.mp3 pointing outside the library was indexed")
	}
	if n := h.queueDepth(t); n != 1 {
		t.Errorf("queue depth = %d, want 1 (only z.mp3)", n)
	}
}

// retagHarness indexes a.mp3 (titled "A") with an unstarted watcher whose
// settle wait is settle, so a test drives handleEvent itself.
func retagHarness(t *testing.T, settle func(context.Context, string) error) (*Watcher, *repository.MemSongRepo, string) {
	t.Helper()
	songs := repository.NewMemSongRepo()
	w, _, path, _ := registerHarness(t, songs, songs)
	w.settleWait = settle
	if _, err := w.ScanExisting(context.Background()); err != nil {
		t.Fatalf("ScanExisting: %v", err)
	}
	return w, songs, path
}

func retag(t *testing.T, path, title string) {
	t.Helper()
	if err := os.WriteFile(path, taggedMP3(t, title), 0o644); err != nil {
		t.Fatal(err)
	}
}

func titleOf(t *testing.T, songs *repository.MemSongRepo, path string) string {
	t.Helper()
	song, err := songs.GetByID(context.Background(), songID(path))
	if err != nil {
		t.Fatalf("GetByID(%s): %v", path, err)
	}
	return song.Title
}

func TestWatcherRerunsRegistrationForWriteWhileInFlight(t *testing.T) {
	ctx := context.Background()
	w, songs, path := retagHarness(t, func(context.Context, string) error { return nil })
	v3 := taggedMP3(t, "v3")
	var updates atomic.Int32
	w.OnUpdated = func(_ *models.Song) {
		if updates.Add(1) == 1 {
			// The path is still claimed here: this Write lands mid-flight.
			if err := os.WriteFile(path, v3, 0o644); err != nil {
				t.Errorf("write v3: %v", err)
			}
			w.handleEvent(ctx, fsnotify.Event{Name: path, Op: fsnotify.Write})
		}
	}

	retag(t, path, "v2")
	w.handleEvent(ctx, fsnotify.Event{Name: path, Op: fsnotify.Write})
	w.settling.Wait()

	if got := titleOf(t, songs, path); got != "v3" {
		t.Fatalf("title = %q, want v3 (Write during refresh dropped)", got)
	}
	if n := updates.Load(); n != 2 {
		t.Fatalf("OnUpdated fired %d times, want 2", n)
	}
}

func TestWatcherReleasesPathWhenSettleFailsAfterMidFlightWrite(t *testing.T) {
	ctx := context.Background()
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var calls atomic.Int32
	w, songs, path := retagHarness(t, func(context.Context, string) error {
		if calls.Add(1) == 1 {
			close(entered)
			<-proceed
			return errors.New("vanished")
		}
		return nil
	})

	w.handleEvent(ctx, fsnotify.Event{Name: path, Op: fsnotify.Write})
	<-entered
	w.handleEvent(ctx, fsnotify.Event{Name: path, Op: fsnotify.Write})
	close(proceed)
	w.settling.Wait()

	w.inflightMu.Lock()
	left := len(w.inflight)
	w.inflightMu.Unlock()
	if left != 0 {
		t.Fatalf("inflight holds %d paths after a failed settle, want 0", left)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("settle ran %d times, want 1 (a failed settle must not rerun)", n)
	}

	retag(t, path, "v2")
	w.handleEvent(ctx, fsnotify.Event{Name: path, Op: fsnotify.Write})
	w.settling.Wait()
	if got := titleOf(t, songs, path); got != "v2" {
		t.Fatalf("title = %q, want v2 (later Write not processed)", got)
	}
}
