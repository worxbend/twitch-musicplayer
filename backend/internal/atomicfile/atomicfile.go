// Package atomicfile replaces a file so readers never see it half-written.
// The temp file is created in the target's directory, so the final rename
// stays on one file system and is atomic, and it is synced before the rename,
// so a crash leaves either the old file or the complete new one.
package atomicfile

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Write replaces path with what write produces. It creates a temp file named
// by pattern (see os.CreateTemp) next to path, runs write on it, sets perm,
// syncs, closes, and renames it over path. On any error the temp file is
// removed and path is left unchanged.
func Write(path, pattern string, perm fs.FileMode, write func(io.Writer) error) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), pattern)
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	if err := fill(tmp, perm, write); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write temp: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// fill writes, chmods, syncs and closes tmp, stopping at the first error.
func fill(tmp *os.File, perm fs.FileMode, write func(io.Writer) error) error {
	if err := write(tmp); err != nil {
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	return tmp.Close()
}
