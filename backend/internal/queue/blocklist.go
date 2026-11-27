package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"lofi-radio-backend/internal/models"
)

// ErrSongBlocked is returned by AddToQueue and PlayNext when the streamer
// pulled the requested song out of rotation.
var ErrSongBlocked = errors.New("song is blocked")

// errNoBlocklist is returned by BlockSong and UnblockSong when the Manager was
// built without a Blocklist.
var errNoBlocklist = errors.New("no blocklist configured")

// Blocklist is the persisted set of song IDs the streamer pulled out of
// rotation. Implementations are safe for concurrent use.
type Blocklist interface {
	Has(songID string) bool
	Add(songID string) error
	Remove(songID string) (bool, error)
	List() []string
}

// isBlocked reports whether songID is pulled out of rotation.
func (m *Manager) isBlocked(songID string) bool {
	return m.blocklist != nil && m.blocklist.Has(songID)
}

// BlockedSong is a blocked song ID with its library entry, or a nil Song when
// the file has left the library.
type BlockedSong struct {
	ID   string
	Song *models.Song
}

// BlockedSongs lists the blocked songs, sorted by ID.
func (m *Manager) BlockedSongs(ctx context.Context) ([]BlockedSong, error) {
	if m.blocklist == nil {
		return nil, nil
	}
	ids := m.blocklist.List()
	out := make([]BlockedSong, 0, len(ids))
	for _, id := range ids {
		song, err := m.songs.GetByID(ctx, id)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("get blocked song %s: %w", id, err)
		}
		out = append(out, BlockedSong{ID: id, Song: song})
	}
	return out, nil
}

// BlockSong pulls a library song out of rotation and drops its queued
// entries. The song playing now keeps playing. Blocking a blocked song is a
// no-op apart from the purge. It returns ErrSongNotFound for an unknown song.
//
// The purge runs before the block is saved, so a failed purge leaves nothing
// blocked. A failed save after a purge returns the purged count with the
// error; the song stays unblocked. m.mu is held throughout, so no refill can
// re-queue the song between the two steps.
func (m *Manager) BlockSong(ctx context.Context, songID string) (purged int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.blocklist == nil {
		return 0, errNoBlocklist
	}
	if _, err := m.lookupSong(ctx, songID); err != nil {
		return 0, err
	}
	purged, err = m.queue.RemoveBySongID(ctx, songID)
	if err != nil {
		return 0, fmt.Errorf("purge blocked song %s: %w", songID, err)
	}
	if err := m.blocklist.Add(songID); err != nil {
		return purged, fmt.Errorf("block song %s: %w", songID, err)
	}
	return purged, nil
}

// UnblockSong puts a song back in rotation. A blocked ID is removed even when
// its song has left the library, so stale entries can be cleaned up. It
// returns ErrSongNotFound only when the ID is neither blocked nor indexed.
func (m *Manager) UnblockSong(ctx context.Context, songID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.blocklist == nil {
		return errNoBlocklist
	}
	removed, err := m.blocklist.Remove(songID)
	if err != nil {
		return fmt.Errorf("unblock song %s: %w", songID, err)
	}
	if removed {
		return nil
	}
	_, err = m.lookupSong(ctx, songID)
	return err
}
