package repository

import (
	"context"
	"database/sql"
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
	r.items = r.items[1:]
	return item, nil
}

// List returns all items with Position set to their 1-based index and Song
// populated via the song repo.
func (r *MemQueueRepo) List(ctx context.Context) ([]*models.QueueItem, error) {
	r.mu.Lock()
	snapshot := make([]*models.QueueItem, len(r.items))
	copy(snapshot, r.items)
	r.mu.Unlock()

	out := make([]*models.QueueItem, len(snapshot))
	for i, item := range snapshot {
		cp := *item
		cp.Position = i + 1
		if cp.Song == nil {
			if s, err := r.songs.GetByID(ctx, item.SongID); err == nil {
				cp.Song = s
			}
		}
		out[i] = &cp
	}
	return out, nil
}

func (r *MemQueueRepo) Remove(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, item := range r.items {
		if item.ID == id {
			r.items = append(r.items[:i], r.items[i+1:]...)
			return nil
		}
	}
	return sql.ErrNoRows
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
	r.items = r.items[:0]
	return nil
}

func (r *MemQueueRepo) Count(_ context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.items), nil
}
