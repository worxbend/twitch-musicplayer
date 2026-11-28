package blocklist

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestLoadMissingFileIsEmpty(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "radio.blocked.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := s.List(); len(got) != 0 {
		t.Fatalf("List = %v, want empty", got)
	}
}

func TestLoadCorruptFileFails(t *testing.T) {
	for name, body := range map[string]string{
		"not json":    "{",
		"bad version": `{"version":2,"song_ids":["a"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "radio.blocked.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("Load succeeded, want error")
			}
		})
	}
}

// TestRoundTrip loads the same path twice, as a restart would.
func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "radio.blocked.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"b", "a", "c", "a"} {
		if err := s.Add(id); err != nil {
			t.Fatalf("Add(%s): %v", id, err)
		}
	}
	removed, err := s.Remove("c")
	if err != nil || !removed {
		t.Fatalf("Remove(c) = %v, %v; want true, nil", removed, err)
	}
	if removed, err := s.Remove("zz"); err != nil || removed {
		t.Fatalf("Remove(zz) = %v, %v; want false, nil", removed, err)
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got, want := reloaded.List(), []string{"a", "b"}; !slices.Equal(got, want) {
		t.Fatalf("reloaded List = %v, want %v", got, want)
	}
	if !reloaded.Has("a") || reloaded.Has("c") {
		t.Fatalf("Has(a)=%v Has(c)=%v, want true false", reloaded.Has("a"), reloaded.Has("c"))
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "radio.blocked.json" {
		t.Fatalf("dir holds %v, want only radio.blocked.json", entries)
	}
}

func TestEmptyStoreReplacesCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "radio.blocked.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Empty(path).Add("a"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := s.List(); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("List = %v, want [a]", got)
	}
}

func TestFailedSaveRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dir", "radio.blocked.json")
	s := Empty(path)
	if err := s.Add("a"); err == nil {
		t.Fatal("Add succeeded with no directory, want error")
	}
	if s.Has("a") {
		t.Fatal("Add kept the id after a failed save")
	}
}

func TestSaveIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes")
	}
	path := filepath.Join(t.TempDir(), "radio.blocked.json")
	if err := Empty(path).Add("a"); err != nil {
		t.Fatalf("Add: %v", err)
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
	path := filepath.Join(dir, "radio.blocked.json")
	if err := os.MkdirAll(filepath.Join(path, "child"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := Empty(path).Add("a"); err == nil {
		t.Fatal("Add onto a directory succeeded, want error")
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, ".blocked-*"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}
