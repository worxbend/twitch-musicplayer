package watcher

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
}

// startWatcher indexes dir, then runs Start until the test ends.
func startWatcher(t *testing.T, dir string) *harness {
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
