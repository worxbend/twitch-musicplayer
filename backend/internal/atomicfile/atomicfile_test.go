package atomicfile

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeString(s string) func(io.Writer) error {
	return func(w io.Writer) error {
		_, err := io.WriteString(w, s)
		return err
	}
}

func assertNoTemp(t *testing.T, dir string) {
	t.Helper()
	leftovers, err := filepath.Glob(filepath.Join(dir, ".tmp-*"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}

func assertContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestWriteCreatesFileWithMode(t *testing.T) {
	for _, perm := range []fs.FileMode{0o600, 0o644} {
		t.Run(perm.String(), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "out.txt")
			if err := Write(path, ".tmp-*", perm, writeString("hello")); err != nil {
				t.Fatalf("Write: %v", err)
			}
			assertContent(t, path, "hello")
			assertNoTemp(t, dir)
			if runtime.GOOS == "windows" {
				return
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			if got := info.Mode().Perm(); got != perm {
				t.Errorf("mode = %o, want %o", got, perm)
			}
		})
	}
}

func TestWriteReplacesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(path, []byte("old and longer"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := Write(path, ".tmp-*", 0o600, writeString("new")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	assertContent(t, path, "new")
	assertNoTemp(t, dir)
}

func TestWriteCallbackErrorKeepsOldFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	errBoom := errors.New("boom")
	err := Write(path, ".tmp-*", 0o600, func(w io.Writer) error {
		_, _ = io.WriteString(w, "partial")
		return errBoom
	})
	if !errors.Is(err, errBoom) || !strings.HasPrefix(err.Error(), "write temp: ") {
		t.Fatalf("Write error = %v, want write temp wrapping errBoom", err)
	}
	assertContent(t, path, "old")
	assertNoTemp(t, dir)
}

func TestWriteMissingDirFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "out.txt")
	err := Write(path, ".tmp-*", 0o600, writeString("x"))
	if err == nil || !strings.HasPrefix(err.Error(), "create temp: ") {
		t.Fatalf("Write error = %v, want prefix %q", err, "create temp: ")
	}
}

func TestWriteOntoDirectoryLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	if err := os.MkdirAll(filepath.Join(path, "child"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	err := Write(path, ".tmp-*", 0o600, writeString("x"))
	if err == nil || !strings.HasPrefix(err.Error(), "replace "+path+": ") {
		t.Fatalf("Write error = %v, want prefix %q", err, "replace "+path+": ")
	}
	assertNoTemp(t, dir)
}
