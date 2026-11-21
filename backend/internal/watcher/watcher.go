package watcher

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
	"lofi-radio-backend/internal/meta"
	"lofi-radio-backend/internal/models"
)

// SongRepository is the subset needed by the watcher.
type SongRepository interface {
	ExistsByPath(ctx context.Context, path string) (bool, error)
	GetByID(ctx context.Context, id string) (*models.Song, error)
	Create(ctx context.Context, song *models.Song) error
	DeleteByPath(ctx context.Context, path string) (*models.Song, bool, error)
	DeleteUnderDir(ctx context.Context, dir string) ([]*models.Song, error)
}

// QueueManager is the subset needed by the watcher.
type QueueManager interface {
	AddToQueue(ctx context.Context, songID string, source models.QueueSource) (*models.QueueItem, error)
	RemoveSong(ctx context.Context, songID string) (int, error)
}

// OnNewSong is called after a newly discovered song is registered.
type OnNewSong func(song *models.Song)

// OnSongsRemoved is called once per filesystem event that dropped songs
// from the library (file deleted, renamed away, or directory removed).
type OnSongsRemoved func(songs []*models.Song)

// ScanSummary describes the startup folder scan.
type ScanSummary struct {
	MusicFiles     int
	NewlyIndexed   int
	AlreadyIndexed int
	WatchedDirs    int
}

// settleWorkers caps how many new files are polled for settling at once, so
// dropping a large folder does not start one stat loop per file.
const settleWorkers = 4

// Watcher monitors a directory tree for new audio files and registers them.
type Watcher struct {
	dir        string
	extensions map[string]struct{}
	songRepo   SongRepository
	queueMgr   QueueManager
	fsWatcher  *fsnotify.Watcher
	onNewSong  OnNewSong
	logger     *log.Logger

	// A new file is indexed once its size and modtime hold still for one
	// settlePoll, so half-copied files are not indexed or queued truncated.
	settlePoll time.Duration
	settling   sync.WaitGroup
	inflightMu sync.Mutex
	inflight   map[string]struct{}
	settleSem  chan struct{}
	// settleWait is w.waitSettled; it is a field only as a test seam.
	settleWait func(ctx context.Context, path string) error

	// OnRemoved, when set before Start, is notified after songs are dropped.
	OnRemoved OnSongsRemoved
	// OnUpdated, when set before Start, is notified after an indexed file is
	// rewritten or replaced. It fires only when the song's metadata changed.
	OnUpdated func(*models.Song)
}

// New creates a Watcher. extensions is the list of file suffixes to index (e.g. [".mp3"]).
// dir is made absolute so song paths and IDs do not depend on the working directory.
func New(dir string, extensions []string, songRepo SongRepository, queueMgr QueueManager, onNewSong OnNewSong) (*Watcher, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := fsw.Add(absDir); err != nil {
		fsw.Close()
		return nil, err
	}

	extSet := make(map[string]struct{}, len(extensions))
	for _, e := range extensions {
		extSet[strings.ToLower(e)] = struct{}{}
	}
	if len(extSet) == 0 {
		extSet[".mp3"] = struct{}{}
	}

	w := &Watcher{
		dir:        absDir,
		extensions: extSet,
		songRepo:   songRepo,
		queueMgr:   queueMgr,
		fsWatcher:  fsw,
		onNewSong:  onNewSong,
		logger:     log.New(log.Writer(), "[watcher] ", log.LstdFlags),

		settlePoll: 250 * time.Millisecond,
		inflight:   make(map[string]struct{}),
		settleSem:  make(chan struct{}, settleWorkers),
	}
	w.settleWait = w.waitSettled
	return w, nil
}

// ScanExisting walks the entire music directory tree and inserts any audio files
// not yet tracked in the DB. Does NOT add songs to the queue.
func (w *Watcher) ScanExisting(ctx context.Context) (*ScanSummary, error) {
	summary := &ScanSummary{}
	watched, err := w.walkTree(ctx, w.dir, func(path string) error {
		summary.MusicFiles++
		_, created, err := w.registerFile(ctx, path, false)
		if err != nil {
			return err
		}
		if created {
			summary.NewlyIndexed++
		} else {
			summary.AlreadyIndexed++
		}
		return nil
	})
	summary.WatchedDirs = watched
	return summary, err
}

// walkTree watches every directory under root and calls onMusicFile for each
// music file. The library root is skipped because New already watches it.
// An unreadable entry below root (lost+found, NAS #recycle) is logged and
// skipped so it cannot leave the library half indexed; root itself still fails.
func (w *Watcher) walkTree(ctx context.Context, root string, onMusicFile func(path string) error) (int, error) {
	watched := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			w.logger.Printf("skip %s: %v", path, err)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if path != w.dir {
				if err := w.fsWatcher.Add(path); err == nil {
					watched++
				}
			}
			return nil
		}
		if !w.isMusicFile(path) {
			return nil
		}
		return onMusicFile(path)
	})
	return watched, err
}

// Start watches the directory tree for new, rewritten and removed files.
// Blocks until ctx is done.
func (w *Watcher) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer w.fsWatcher.Close()
	defer w.settling.Wait()
	defer cancel()

	for {
		select {
		case event, ok := <-w.fsWatcher.Events:
			if !ok {
				return nil
			}
			if event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
				// A rename's new path arrives separately as a Create event.
				w.unregisterPath(ctx, event.Name)
				continue
			}
			if event.Has(fsnotify.Create) {
				w.handleCreate(ctx, event.Name)
			} else if event.Has(fsnotify.Write) && w.isMusicFile(event.Name) {
				// An in-place tag save; claim collapses the burst of Writes.
				w.registerWhenSettled(ctx, event.Name)
			}

		case err, ok := <-w.fsWatcher.Errors:
			if !ok {
				return nil
			}
			w.logger.Printf("fsnotify error: %v", err)

		case <-ctx.Done():
			return nil
		}
	}
}

// handleCreate queues a new music file for indexing, or, for a new directory
// (possibly a whole moved tree), watches it and every folder inside.
func (w *Watcher) handleCreate(ctx context.Context, path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	if !info.IsDir() {
		if w.isMusicFile(path) {
			w.registerWhenSettled(ctx, path)
		}
		return
	}
	_, err = w.walkTree(ctx, path, func(file string) error {
		w.registerWhenSettled(ctx, file)
		return nil
	})
	if err != nil {
		w.logger.Printf("scan %s: %v", path, err)
	}
}

// registerWhenSettled indexes and queues path in the background once it stops
// changing. A path already settling is skipped, so a directory scan racing the
// file's own Create event registers it once.
func (w *Watcher) registerWhenSettled(ctx context.Context, path string) {
	if !w.claim(path) {
		return
	}
	w.settling.Go(func() {
		defer w.release(path)
		if err := w.awaitSettled(ctx, path); err != nil {
			if ctx.Err() == nil {
				w.logger.Printf("skip %s: %v", path, err)
			}
			return
		}
		if _, _, err := w.registerFile(ctx, path, true); err != nil {
			w.logger.Printf("register %s: %v", path, err)
		}
	})
}

// awaitSettled waits for a free settle slot, then for path to settle. The slot
// is released as soon as the wait ends: the cap bounds stat polling, and
// registerFile is short and runs once per file anyway.
func (w *Watcher) awaitSettled(ctx context.Context, path string) error {
	select {
	case w.settleSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-w.settleSem }()
	return w.settleWait(ctx, path)
}

func (w *Watcher) claim(path string) bool {
	w.inflightMu.Lock()
	defer w.inflightMu.Unlock()
	if _, busy := w.inflight[path]; busy {
		return false
	}
	w.inflight[path] = struct{}{}
	return true
}

func (w *Watcher) release(path string) {
	w.inflightMu.Lock()
	defer w.inflightMu.Unlock()
	delete(w.inflight, path)
}

// waitSettled returns nil once path's size and modtime are unchanged across
// two consecutive polls. It fails if path disappears or ctx ends. There is no
// deadline: a copy over a slow link can take minutes, and no Write event
// would ever restart the wait once it gave up.
func (w *Watcher) waitSettled(ctx context.Context, path string) error {
	prev, err := os.Stat(path)
	if err != nil {
		return err
	}
	poll := time.NewTicker(w.settlePoll)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-poll.C:
		}
		cur, err := os.Stat(path)
		if err != nil {
			return err
		}
		if cur.Size() == prev.Size() && cur.ModTime().Equal(prev.ModTime()) {
			return nil
		}
		prev = cur
	}
}

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
	song.SizeBytes = stat.Size()
	return song, nil
}

// registerFile inserts a new song record and optionally queues it. A path
// already indexed is refreshed instead and reported as not created.
func (w *Watcher) registerFile(ctx context.Context, path string, addToQueue bool) (*models.Song, bool, error) {
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
// (nil, nil) when the song left the library meanwhile, so it is not revived.
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
	if err := w.songRepo.Create(ctx, &updated); err != nil {
		return nil, err
	}
	w.logger.Printf("updated: %s", updated.Title)
	if w.OnUpdated != nil {
		w.OnUpdated(&updated)
	}
	return &updated, nil
}

// unregisterPath drops songs for a path that no longer exists, purges them
// from the queue so they are never handed to clients, and notifies OnRemoved.
func (w *Watcher) unregisterPath(ctx context.Context, path string) {
	removed, err := w.removeFromLibrary(ctx, path)
	if err != nil {
		w.logger.Printf("unregister %s: %v", path, err)
		return
	}
	if len(removed) == 0 {
		return
	}
	for _, song := range removed {
		if _, err := w.queueMgr.RemoveSong(ctx, song.ID); err != nil {
			w.logger.Printf("dequeue %s: %v", song.Filename, err)
		}
		w.logger.Printf("removed: %s", song.Path)
	}
	if w.OnRemoved != nil {
		w.OnRemoved(removed)
	}
}

// removeFromLibrary deletes the song at path, or every song under path when it
// is not a music file (a removed directory). The path is gone, so no Stat.
func (w *Watcher) removeFromLibrary(ctx context.Context, path string) ([]*models.Song, error) {
	if !w.isMusicFile(path) {
		return w.songRepo.DeleteUnderDir(ctx, path)
	}
	song, found, err := w.songRepo.DeleteByPath(ctx, path)
	if err != nil || !found {
		return nil, err
	}
	return []*models.Song{song}, nil
}

func (w *Watcher) isMusicFile(path string) bool {
	_, ok := w.extensions[strings.ToLower(filepath.Ext(path))]
	return ok
}
