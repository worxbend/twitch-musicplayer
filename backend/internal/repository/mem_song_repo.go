package repository

import (
	"cmp"
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"lofi-radio-backend/internal/models"
)

// MemSongRepo is a thread-safe in-memory implementation of SongRepository.
// Songs are indexed at startup from the filesystem and held for the lifetime
// of the process. No SQLite required.
type MemSongRepo struct {
	mu     sync.RWMutex
	byID   map[string]*models.Song
	byPath map[string]string // path → id
	order  []string          // insertion-order ids (for stable List output)
}

func NewMemSongRepo() *MemSongRepo {
	return &MemSongRepo{
		byID:   make(map[string]*models.Song),
		byPath: make(map[string]string),
	}
}

func (r *MemSongRepo) Create(_ context.Context, song *models.Song) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byID[song.ID]; !exists {
		r.order = append(r.order, song.ID)
	}
	cp := *song
	r.byID[song.ID] = &cp
	r.byPath[song.Path] = song.ID
	return nil
}

// Update replaces an existing song's record. It returns sql.ErrNoRows when the
// ID is not in the library and never inserts, so a stale write cannot revive a
// deleted song. List order and the path index are unchanged.
func (r *MemSongRepo) Update(_ context.Context, song *models.Song) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[song.ID]; !ok {
		return sql.ErrNoRows
	}
	cp := *song
	r.byID[song.ID] = &cp
	return nil
}

func (r *MemSongRepo) GetByID(_ context.Context, id string) (*models.Song, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.byID[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	cp := *s
	return &cp, nil
}

func (r *MemSongRepo) List(_ context.Context) ([]*models.Song, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.Song, 0, len(r.order))
	for _, id := range r.order {
		if s, ok := r.byID[id]; ok {
			cp := *s
			out = append(out, &cp)
		}
	}
	sortSongs(out)
	return out, nil
}

func (r *MemSongRepo) Search(_ context.Context, query string) ([]*models.Song, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	q := strings.ToLower(query)
	var out []*models.Song
	for _, id := range r.order {
		s, ok := r.byID[id]
		if !ok {
			continue
		}
		if strings.Contains(strings.ToLower(s.Title), q) ||
			strings.Contains(strings.ToLower(s.Artist), q) ||
			strings.Contains(strings.ToLower(s.Album), q) ||
			strings.Contains(strings.ToLower(s.Filename), q) {
			cp := *s
			out = append(out, &cp)
		}
	}
	sortSongs(out)
	return out, nil
}

func (r *MemSongRepo) ExistsByPath(_ context.Context, path string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.byPath[path]
	return ok, nil
}

func (r *MemSongRepo) Count(_ context.Context) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byID), nil
}

// DeleteByPath removes the song indexed at path and returns it.
// found is false when no song has that path.
func (r *MemSongRepo) DeleteByPath(_ context.Context, path string) (*models.Song, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byPath[path]
	if !ok {
		return nil, false, nil
	}
	return r.deleteLocked(id), true, nil
}

// DeleteUnderDir removes every song whose path lies inside dir (at any depth)
// and returns the removed songs. A sibling such as dir+"b" is not matched.
func (r *MemSongRepo) DeleteUnderDir(_ context.Context, dir string) ([]*models.Song, error) {
	prefix := filepath.Clean(dir) + string(filepath.Separator)
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []string
	for path, id := range r.byPath {
		if strings.HasPrefix(path, prefix) {
			ids = append(ids, id)
		}
	}
	removed := make([]*models.Song, 0, len(ids))
	for _, id := range ids {
		removed = append(removed, r.deleteLocked(id))
	}
	return removed, nil
}

// deleteLocked drops id from every index and returns the removed song.
// Must be called with r.mu held for writing and id present in byID.
func (r *MemSongRepo) deleteLocked(id string) *models.Song {
	song := r.byID[id]
	delete(r.byID, id)
	delete(r.byPath, song.Path)
	for i, oid := range r.order {
		if oid == id {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	return song
}

// sortSongs orders by Title, then Artist, then Path. Path is unique per song, so the order is total.
func sortSongs(songs []*models.Song) {
	slices.SortFunc(songs, func(a, b *models.Song) int {
		return cmp.Or(cmp.Compare(a.Title, b.Title), cmp.Compare(a.Artist, b.Artist), cmp.Compare(a.Path, b.Path))
	})
}
