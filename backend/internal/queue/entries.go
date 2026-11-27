package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"lofi-radio-backend/internal/models"
)

// AddToQueue validates that a song exists and appends it to the playback queue,
// returning the entry it created.
func (m *Manager) AddToQueue(ctx context.Context, songID string, source models.QueueSource) (*models.QueueItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	song, err := m.requireSong(ctx, songID)
	if err != nil {
		return nil, err
	}
	item := newQueueItem(songID, source)
	item.Song = song
	if err := m.queue.Enqueue(ctx, item); err != nil {
		return nil, fmt.Errorf("enqueue song %s: %w", songID, err)
	}
	return item, nil
}

// requireSong loads a song that may be queued: it fails with ErrSongNotFound
// or ErrSongBlocked so callers can map either onto a status code without
// knowing about the storage layer. Must be called with m.mu held.
func (m *Manager) requireSong(ctx context.Context, songID string) (*models.Song, error) {
	song, err := m.lookupSong(ctx, songID)
	if err != nil {
		return nil, err
	}
	if m.isBlocked(songID) {
		return nil, fmt.Errorf("%w: %s", ErrSongBlocked, songID)
	}
	return song, nil
}

// lookupSong loads a song by ID, translating "not found" into ErrSongNotFound.
func (m *Manager) lookupSong(ctx context.Context, songID string) (*models.Song, error) {
	song, err := m.songs.GetByID(ctx, songID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ErrSongNotFound, songID)
		}
		return nil, fmt.Errorf("get song %s: %w", songID, err)
	}
	return song, nil
}

// newQueueItem builds a queue entry with a fresh identity and timestamp.
func newQueueItem(songID string, source models.QueueSource) *models.QueueItem {
	return &models.QueueItem{
		ID:      uuid.New().String(),
		SongID:  songID,
		Source:  source,
		AddedAt: time.Now(),
	}
}

// ListQueue returns the current contents of the playback queue.
func (m *Manager) ListQueue(ctx context.Context) ([]*models.QueueItem, error) {
	return m.queue.List(ctx)
}

// ClearQueue removes all pending queue entries.
func (m *Manager) ClearQueue(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.queue.Clear(ctx); err != nil {
		return err
	}
	clear(m.carryover)
	return nil
}

// RemoveSong purges every queued entry and the play count for songID, e.g.
// after its file was deleted from disk, and reports how many entries were
// removed.
func (m *Manager) RemoveSong(ctx context.Context, songID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.playCounts, songID)
	return m.queue.RemoveBySongID(ctx, songID)
}

// RemoveEntry drops the queue entry with id. It returns sql.ErrNoRows when no
// such entry is queued. Holding m.mu keeps it from racing PlayNext's
// list-then-remove of auto entries.
func (m *Manager) RemoveEntry(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.queue.Remove(ctx, id)
}

// MoveEntry shifts the queue entry with id by delta places, clamped to the
// queue bounds, and reports its new 1-based position and whether it moved. It
// returns sql.ErrNoRows when no such entry is queued.
func (m *Manager) MoveEntry(ctx context.Context, id string, delta int) (int, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.queue.Move(ctx, id, delta)
}

// PlayNext inserts the given song at the front of the queue so it plays
// immediately after the current track ends (or when a skip_now is broadcast).
// Auto-queued entries for the same song are dropped so it does not play twice
// in a row; manual entries are kept.
func (m *Manager) PlayNext(ctx context.Context, songID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, err := m.requireSong(ctx, songID); err != nil {
		return err
	}
	if err := m.removeAutoEntries(ctx, songID); err != nil {
		return err
	}
	return m.queue.EnqueueFront(ctx, newQueueItem(songID, models.QueueSourceManual))
}

// Requeue puts a song NextSong already handed out back at the front of the
// queue, for when the play message never reached its client. The entry is
// manual so PlayNext cannot drop it as an auto duplicate. The least_played
// count is rolled back because the play was not delivered. The played mark and
// recent history are left alone: marking again is idempotent and a stale
// recent entry only nudges the shuffle.
func (m *Manager) Requeue(ctx context.Context, songID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.queue.EnqueueFront(ctx, newQueueItem(songID, models.QueueSourceManual)); err != nil {
		return fmt.Errorf("requeue song %s: %w", songID, err)
	}
	// Drop a count that reaches zero so pruneCounts' size check stays honest.
	if m.cfg.Shuffle.Strategy == ShuffleLeastPlayed && m.playCounts[songID] > 0 {
		m.playCounts[songID]--
		if m.playCounts[songID] == 0 {
			delete(m.playCounts, songID)
		}
	}
	return nil
}

// removeAutoEntries drops every auto-queued entry for songID. An entry that is
// already gone counts as removed. Must be called with m.mu held.
func (m *Manager) removeAutoEntries(ctx context.Context, songID string) error {
	items, err := m.queue.List(ctx)
	if err != nil {
		return fmt.Errorf("list queue: %w", err)
	}
	for _, item := range items {
		if item.SongID != songID || item.Source != models.QueueSourceAuto {
			continue
		}
		if err := m.queue.Remove(ctx, item.ID); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("remove auto entry %s: %w", item.ID, err)
		}
	}
	return nil
}
