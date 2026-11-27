package watcher

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"lofi-radio-backend/internal/meta"
	"lofi-radio-backend/internal/models"
)

// songID is a UUIDv5 derived from the absolute path — stable across restarts
// so the Bloom filter stays valid without a persistent database.
func songID(path string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(path)).String()
}

// withFileMetadata returns song with the fields read from the file at path.
func withFileMetadata(song models.Song, path string) (models.Song, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return song, err
	}
	m := meta.Extract(path)
	song.Title = m.Title
	song.Artist = m.Artist
	song.Album = m.Album
	song.DurationSecs = m.DurationSecs
	song.HasCover = m.HasCover
	song.SizeBytes = stat.Size()
	return song, nil
}

// registerFile inserts a new song record and optionally queues it. A path
// already indexed is refreshed instead and reported as not created.
func (w *Watcher) registerFile(ctx context.Context, path string, addToQueue bool) (*models.Song, bool, error) {
	if !w.insideLibrary(path) {
		return nil, false, errOutsideLibrary
	}
	exists, err := w.songRepo.ExistsByPath(ctx, path)
	if err != nil {
		return nil, false, err
	}
	if exists {
		song, err := w.refreshFile(ctx, path)
		return song, false, err
	}

	fresh, err := withFileMetadata(models.Song{
		ID:       songID(path),
		Filename: filepath.Base(path),
		Path:     path,
		AddedAt:  time.Now(),
	}, path)
	if err != nil {
		return nil, false, err
	}
	song := &fresh

	if err := w.songRepo.Create(ctx, song); err != nil {
		return nil, false, err
	}
	// A Remove event handled before Create found no song to delete, so
	// re-check the file. A delete after this Stat is caught by its Remove
	// event, as the song now exists; AddToQueue and RemoveSong share the
	// manager mutex, so that event either fails the queue add or dequeues it.
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		w.unregisterPath(ctx, path)
		return nil, false, nil
	}
	w.logger.Printf("indexed: %s (%.0fs)", song.Title, song.DurationSecs)

	if addToQueue {
		if _, err := w.queueMgr.AddToQueue(ctx, song.ID, models.QueueSourceAuto); err != nil {
			w.logger.Printf("queue %s: %v", song.Filename, err)
		}
		if w.onNewSong != nil {
			w.onNewSong(song)
		}
	}

	return song, true, nil
}

// refreshFile re-reads the metadata of an indexed file that was rewritten or
// replaced, so a retag shows up without a restart. It never re-queues, and it
// writes and notifies OnUpdated only when the metadata changed. It returns
// (nil, nil) when the song left the library, even mid-refresh: the write is
// update-only, so a delete racing the tag re-read cannot revive the song.
func (w *Watcher) refreshFile(ctx context.Context, path string) (*models.Song, error) {
	current, err := w.songRepo.GetByID(ctx, songID(path))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	updated, err := withFileMetadata(*current, path)
	if err != nil {
		return nil, err
	}
	// Only file-derived fields can differ; ID, Path and AddedAt are copied.
	if updated == *current {
		return current, nil
	}
	err = w.songRepo.Update(ctx, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	w.logger.Printf("updated: %s", updated.Title)
	if w.OnUpdated != nil {
		w.OnUpdated(&updated)
	}
	return &updated, nil
}

// unregisterPath drops songs for a path that no longer exists.
func (w *Watcher) unregisterPath(ctx context.Context, path string) {
	removed, err := w.removeFromLibrary(ctx, path)
	if err != nil {
		w.logger.Printf("unregister %s: %v", path, err)
		return
	}
	w.dropSongs(ctx, removed)
}

// dropSongs purges songs already deleted from the repository from the queue,
// so they are never handed to clients, then notifies OnRemoved once.
func (w *Watcher) dropSongs(ctx context.Context, songs []*models.Song) {
	if len(songs) == 0 {
		return
	}
	for _, song := range songs {
		if _, err := w.queueMgr.RemoveSong(ctx, song.ID); err != nil {
			w.logger.Printf("dequeue %s: %v", song.Filename, err)
		}
		w.logger.Printf("removed: %s", song.Path)
	}
	if w.OnRemoved != nil {
		w.OnRemoved(songs)
	}
}

// removeFromLibrary deletes the song at path, or every song under path when it
// is a removed directory. A directory can carry a music extension (a folder
// named "Beats.mp3"), so a music path that matches no song is also treated as
// one. The path is gone, so no Stat.
func (w *Watcher) removeFromLibrary(ctx context.Context, path string) ([]*models.Song, error) {
	if !w.isMusicFile(path) {
		return w.songRepo.DeleteUnderDir(ctx, path)
	}
	song, found, err := w.songRepo.DeleteByPath(ctx, path)
	if err != nil {
		return nil, err
	}
	if !found {
		return w.songRepo.DeleteUnderDir(ctx, path)
	}
	return []*models.Song{song}, nil
}

// insideLibrary reports whether path, with symlinks resolved, exists under
// music_dir. It mirrors streamPathUnderMusicDir in the songs API.
func (w *Watcher) insideLibrary(path string) bool {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(w.resolvedDir, resolved)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (w *Watcher) isMusicFile(path string) bool {
	_, ok := w.extensions[strings.ToLower(filepath.Ext(path))]
	return ok
}
