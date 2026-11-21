package played

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestTracker() *Tracker {
	return New(1000, 0.01)
}

func TestMarkPlayedThenReset(t *testing.T) {
	tr := newTestTracker()
	tr.MarkPlayed("a")
	if !tr.HasPlayed("a") {
		t.Fatal("HasPlayed(a) = false after MarkPlayed")
	}
	tr.Reset()
	if tr.HasPlayed("a") {
		t.Fatal("HasPlayed(a) = true after Reset")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.bloom")
	src := newTestTracker()
	src.MarkPlayed("a")
	src.MarkPlayed("b")
	if err := src.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dst := newTestTracker()
	if err := dst.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, id := range []string{"a", "b"} {
		if !dst.HasPlayed(id) {
			t.Errorf("HasPlayed(%q) = false after Load", id)
		}
	}
	if dst.HasPlayed("zzz-control") {
		t.Error("HasPlayed(zzz-control) = true for an unmarked ID")
	}
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.bloom")
	if err := newTestTracker().Load(path); err != nil {
		t.Fatalf("Load of missing file: %v", err)
	}
}

func TestLoadTruncatedFileKeepsPreviousState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.bloom")
	src := newTestTracker()
	src.MarkPlayed("a")
	if err := src.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if err := os.WriteFile(path, data[:len(data)/2], 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	dst := newTestTracker()
	dst.MarkPlayed("keep")
	if err := dst.Load(path); err == nil {
		t.Fatal("Load of truncated file returned nil error")
	}
	if !dst.HasPlayed("keep") {
		t.Fatal("failed Load overwrote the in-memory filter")
	}
}

func TestSaveIntoMissingDirFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "x.bloom")
	if err := newTestTracker().Save(path); err == nil {
		t.Fatal("Save into nonexistent directory returned nil error")
	}
}

func TestSaveReplacesFileWithoutLeavingTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.bloom")
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	src := newTestTracker()
	src.MarkPlayed("fresh")
	if err := src.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dst := newTestTracker()
	if err := dst.Load(path); err != nil {
		t.Fatalf("Load after overwrite: %v", err)
	}
	if !dst.HasPlayed("fresh") {
		t.Error("HasPlayed(fresh) = false; Save did not replace the file")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".bloom-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestSaveIfDirtyOnFreshTrackerWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.bloom")
	saved, err := newTestTracker().SaveIfDirty(path)
	if saved || err != nil {
		t.Fatalf("SaveIfDirty = %v, %v; want false, nil", saved, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Stat after clean SaveIfDirty: %v; want not-exist", err)
	}
}

func TestSaveIfDirtySavesOnceAfterMarkPlayed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.bloom")
	src := newTestTracker()
	src.MarkPlayed("a")
	if saved, err := src.SaveIfDirty(path); !saved || err != nil {
		t.Fatalf("first SaveIfDirty = %v, %v; want true, nil", saved, err)
	}
	if saved, err := src.SaveIfDirty(path); saved || err != nil {
		t.Fatalf("second SaveIfDirty = %v, %v; want false, nil", saved, err)
	}

	dst := newTestTracker()
	if err := dst.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !dst.HasPlayed("a") {
		t.Error("HasPlayed(a) = false after SaveIfDirty and Load")
	}
}

func TestSaveIfDirtyStaysDirtyAfterError(t *testing.T) {
	tr := newTestTracker()
	tr.MarkPlayed("a")
	bad := filepath.Join(t.TempDir(), "missing", "x.bloom")
	if saved, err := tr.SaveIfDirty(bad); saved || err == nil {
		t.Fatalf("SaveIfDirty(bad) = %v, %v; want false, error", saved, err)
	}
	good := filepath.Join(t.TempDir(), "x.bloom")
	if saved, err := tr.SaveIfDirty(good); !saved || err != nil {
		t.Fatalf("SaveIfDirty(good) = %v, %v; want true, nil", saved, err)
	}
}

func TestResetMarksDirty(t *testing.T) {
	tr := newTestTracker()
	tr.Reset()
	path := filepath.Join(t.TempDir(), "x.bloom")
	if saved, err := tr.SaveIfDirty(path); !saved || err != nil {
		t.Fatalf("SaveIfDirty after Reset = %v, %v; want true, nil", saved, err)
	}
}

func TestRunAutosaveWritesAndStopsOnCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.bloom")
	tr := newTestTracker()
	tr.MarkPlayed("a")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		tr.RunAutosave(ctx, path, 10*time.Millisecond, t.Logf)
		close(done)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("RunAutosave did not write the file within 1s")
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunAutosave did not return within 1s of cancel")
	}
}
