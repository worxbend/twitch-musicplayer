package played

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

// bloomFile builds a serialized filter: big-endian m, k, bitset length, then
// the given number of zero words.
func bloomFile(m, k, length uint64, words int) []byte {
	data := make([]byte, 24+8*words)
	binary.BigEndian.PutUint64(data[0:], m)
	binary.BigEndian.PutUint64(data[8:], k)
	binary.BigEndian.PutUint64(data[16:], length)
	return data
}

func TestLoadRejectsDegenerateOrCorruptFile(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"short header", bloomFile(64, 3, 64, 0)[:23]},
		{"m is zero", bloomFile(0, 3, 0, 0)},
		{"k is zero", bloomFile(64, 0, 64, 1)},
		{"k above max", bloomFile(64, maxBloomK+1, 64, 1)},
		{"huge k", bloomFile(64, 1<<40, 64, 1)},
		{"bitset length differs from m", bloomFile(64, 3, 128, 2)},
		{"huge length header", bloomFile(1<<62, 3, 1<<62, 0)},
		{"missing words", bloomFile(128, 3, 128, 1)},
		{"extra words", bloomFile(64, 3, 64, 2)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "x.bloom")
			if err := os.WriteFile(path, tc.data, 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			tr := newTestTracker()
			tr.MarkPlayed("keep")
			err := tr.Load(path)
			if err == nil {
				t.Fatal("Load returned nil error")
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error %q does not name the file", err)
			}
			if !tr.HasPlayed("keep") {
				t.Fatal("failed Load replaced the in-memory filter")
			}
			if tr.HasPlayed("zzz-control") {
				t.Error("HasPlayed(zzz-control) = true; filter looks degenerate")
			}
			tr.MarkPlayed("after") // must not panic
		})
	}
}

func TestLoadAcceptsMaxHashCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.bloom")
	if err := os.WriteFile(path, bloomFile(64, maxBloomK, 64, 1), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	tr := newTestTracker()
	if err := tr.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if tr.HasPlayed("a") {
		t.Error("HasPlayed(a) = true on an empty filter")
	}
}

func TestLoadAcceptsFileWithPartialLastWord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.bloom")
	if err := os.WriteFile(path, bloomFile(65, 3, 65, 2), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	tr := newTestTracker()
	if err := tr.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if tr.HasPlayed("a") {
		t.Error("HasPlayed(a) = true on an empty filter")
	}
}

func markIDs(tr *Tracker, prefix string, n int) {
	for i := range n {
		tr.MarkPlayed(fmt.Sprintf("%s-%d", prefix, i))
	}
}

func playedCount(tr *Tracker, prefix string, n int) int {
	count := 0
	for i := range n {
		if tr.HasPlayed(fmt.Sprintf("%s-%d", prefix, i)) {
			count++
		}
	}
	return count
}

func TestEnsureCapacityKeepsHistoryUntilReset(t *testing.T) {
	tr := New(minCapacity, 0.01)
	markIDs(tr, "old", 100)
	tr.EnsureCapacity(30_000)
	if tr.Capacity() != 60_000 {
		t.Fatalf("Capacity = %d, want 60000", tr.Capacity())
	}
	if got := playedCount(tr, "old", 100); got != 100 {
		t.Fatalf("after grow, %d of 100 marks still played, want 100", got)
	}
	tr.Reset()
	if tr.legacy != nil {
		t.Fatal("legacy filter survives Reset")
	}
	if got := playedCount(tr, "old", 100); got != 0 {
		t.Fatalf("after Reset, %d of 100 marks still played, want 0", got)
	}
}

func TestEnsureCapacityBelowCurrentIsNoOp(t *testing.T) {
	tr := New(minCapacity, 0.01)
	tr.MarkPlayed("a")
	before := tr.f
	tr.EnsureCapacity(100)
	tr.EnsureCapacity(minCapacity / 2)
	if tr.f != before || tr.legacy != nil || tr.Capacity() != minCapacity {
		t.Fatalf("EnsureCapacity below capacity changed the tracker (cap %d)", tr.Capacity())
	}
	if !tr.HasPlayed("a") {
		t.Fatal("HasPlayed(a) = false after a no-op EnsureCapacity")
	}
}

// fpRateAfterFill loads a saved 10k filter, optionally grows it for 30k songs,
// marks 30k songs, and returns the false-positive rate over 10k fresh IDs.
func fpRateAfterFill(t *testing.T, path string, grow bool) float64 {
	t.Helper()
	tr := New(minCapacity, 0.01)
	if err := tr.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if grow {
		tr.EnsureCapacity(30_000)
	}
	markIDs(tr, "song", 30_000)
	return float64(playedCount(tr, "fresh", 10_000)) / 10_000
}

func TestEnsureCapacityKeepsFalsePositivesLowPast10k(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.bloom")
	if err := New(minCapacity, 0.01).Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if rate := fpRateAfterFill(t, path, true); rate >= 0.02 {
		t.Errorf("grown filter FP rate = %.3f, want < 0.02", rate)
	}
	// Control: without growing, a 10k filter holding 30k songs hides many
	// unplayed songs. This is the bug EnsureCapacity fixes.
	if rate := fpRateAfterFill(t, path, false); rate <= 0.05 {
		t.Errorf("ungrown filter FP rate = %.3f, want > 0.05", rate)
	}
}

func TestSaveAfterGrowKeepsNewMarks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.bloom")
	src := New(minCapacity, 0.01)
	src.EnsureCapacity(30_000)
	markIDs(src, "new", 100)
	if err := src.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	dst := New(minCapacity, 0.01)
	if err := dst.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := playedCount(dst, "new", 100); got != 100 {
		t.Fatalf("after Save/Load, %d of 100 marks played, want 100", got)
	}
}

func TestSaveIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes")
	}
	path := filepath.Join(t.TempDir(), "x.bloom")
	if err := newTestTracker().Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 600", got)
	}
}

func TestSaveOntoDirectoryLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.bloom")
	if err := os.MkdirAll(filepath.Join(path, "child"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := newTestTracker().Save(path); err == nil {
		t.Fatal("Save onto a directory returned nil error")
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, ".bloom-*"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}
