package repository

import (
	"context"
	"database/sql"
	"slices"
	"sync"

	"lofi-radio-backend/internal/models"
)

// MemQueueRepo is a thread-safe in-memory implementation of QueueRepository.
// The queue is a slice ordered front-to-back; index 0 is the next track to play.
type MemQueueRepo struct {
	mu    sync.Mutex
	items []*models.QueueItem
	songs *MemSongRepo // used to enrich List results with song metadata
}

func NewMemQueueRepo(songs *MemSongRepo) *MemQueueRepo {
	return &MemQueueRepo{songs: songs}
}

// Enqueue appends a copy of item and writes its position back to the caller.
// Storing a copy keeps later caller writes from racing with List.
func (r *MemQueueRepo) Enqueue(_ context.Context, item *models.QueueItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	item.Position = len(r.items) + 1
	cp := *item
	r.items = append(r.items, &cp)
	return nil
}

// EnqueueFront inserts a copy of item at position 0 so Dequeue picks it up next.
func (r *MemQueueRepo) EnqueueFront(_ context.Context, item *models.QueueItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	item.Position = 0
	cp := *item
	r.items = append([]*models.QueueItem{&cp}, r.items...)
	return nil
}

func (r *MemQueueRepo) Dequeue(_ context.Context) (*models.QueueItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.items) == 0 {
		return nil, sql.ErrNoRows
	}
	item := r.items[0]
	r.items[0] = nil // drop the backing array's reference to the popped item
	r.items = r.items[1:]
	return item, nil
}

// List returns all items with Position set to their 1-based index. Song is
// reloaded from the song repo, so it reflects the current library (a retagged
// file shows its new tags) and is nil when the song has left the library.
func (r *MemQueueRepo) List(ctx context.Context) ([]*models.QueueItem, error) {
	r.mu.Lock()
	snapshot := make([]*models.QueueItem, len(r.items))
	copy(snapshot, r.items)
	r.mu.Unlock()

	out := make([]*models.QueueItem, len(snapshot))
	for i, item := range snapshot {
		cp := *item
		cp.Position = i + 1
		cp.Song = nil
		if s, err := r.songs.GetByID(ctx, item.SongID); err == nil {
			cp.Song = s
		}
		out[i] = &cp
	}
	return out, nil
}

func (r *MemQueueRepo) Remove(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := slices.IndexFunc(r.items, func(item *models.QueueItem) bool { return item.ID == id })
	if i < 0 {
		return sql.ErrNoRows
	}
	r.items = slices.Delete(r.items, i, i+1) // zeroes the vacated tail slot
	return nil
}

// Move shifts the item with id by delta places, clamped to the queue bounds.
// It returns the item's new 1-based position and whether it changed place.
func (r *MemQueueRepo) Move(_ context.Context, id string, delta int) (position int, moved bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	from := slices.IndexFunc(r.items, func(item *models.QueueItem) bool { return item.ID == id })
	if from < 0 {
		return 0, false, sql.ErrNoRows
	}
	to := min(max(from+delta, 0), len(r.items)-1)
	if to == from {
		return from + 1, false, nil
	}
	item := r.items[from]
	r.items = slices.Insert(slices.Delete(r.items, from, from+1), to, item)
	return to + 1, true, nil
}

// RemoveBySongID drops every queued entry for songID and reports how many
// were removed.
func (r *MemQueueRepo) RemoveBySongID(_ context.Context, songID string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := r.items[:0]
	for _, item := range r.items {
		if item.SongID != songID {
			kept = append(kept, item)
		}
	}
	removed := len(r.items) - len(kept)
	clear(r.items[len(kept):])
	r.items = kept
	return removed, nil
}

func (r *MemQueueRepo) Clear(_ context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	clear(r.items) // drop references so cleared items can be collected
	r.items = r.items[:0]
	return nil
}

func (r *MemQueueRepo) Count(_ context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.items), nil
}
