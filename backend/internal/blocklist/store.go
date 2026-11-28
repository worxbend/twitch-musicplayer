// Package blocklist persists the song IDs the streamer pulled out of rotation.
package blocklist

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"sync"

	"lofi-radio-backend/internal/atomicfile"
)

const fileVersion = 1

// fileFormat is the on-disk JSON shape.
type fileFormat struct {
	Version int      `json:"version"`
	SongIDs []string `json:"song_ids"`
}

// Store is a set of blocked song IDs saved to a JSON file on every change.
// It is safe for concurrent use.
type Store struct {
	mu   sync.RWMutex
	path string
	ids  map[string]struct{}
}

// Load reads the blocklist at path. A missing file gives an empty store that
// saves to path. A file that cannot be read or decoded returns an error.
func Load(path string) (*Store, error) {
	s := Empty(path)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var f fileFormat
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("blocklist %s: %w", path, err)
	}
	if f.Version != fileVersion {
		return nil, fmt.Errorf("blocklist %s: unsupported version %d", path, f.Version)
	}
	for _, id := range f.SongIDs {
		s.ids[id] = struct{}{}
	}
	return s, nil
}

// Empty returns an empty store that saves to path, for when Load failed and
// the file should be replaced on the next change.
func Empty(path string) *Store {
	return &Store{path: path, ids: map[string]struct{}{}}
}

// Has reports whether id is blocked.
func (s *Store) Has(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.ids[id]
	return ok
}

// Add blocks id and saves. Adding a blocked id is a no-op. When the save
// fails the id stays unblocked and the error is returned.
func (s *Store) Add(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ids[id]; ok {
		return nil
	}
	s.ids[id] = struct{}{}
	if err := s.saveLocked(); err != nil {
		delete(s.ids, id)
		return err
	}
	return nil
}

// Remove unblocks id, saves, and reports whether it was blocked. When the
// save fails the id stays blocked and the error is returned.
func (s *Store) Remove(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ids[id]; !ok {
		return false, nil
	}
	delete(s.ids, id)
	if err := s.saveLocked(); err != nil {
		s.ids[id] = struct{}{}
		return false, err
	}
	return true, nil
}

// List returns the blocked IDs, sorted.
func (s *Store) List() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sortedLocked()
}

func (s *Store) sortedLocked() []string {
	ids := make([]string, 0, len(s.ids))
	for id := range s.ids {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// saveLocked writes the set to s.path atomically: a temp file in the same
// directory is synced, then renamed over the old file. The caller holds s.mu.
func (s *Store) saveLocked() error {
	data, err := json.Marshal(fileFormat{Version: fileVersion, SongIDs: s.sortedLocked()})
	if err != nil {
		return err
	}
	return atomicfile.Write(s.path, ".blocked-*", 0o600, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}
