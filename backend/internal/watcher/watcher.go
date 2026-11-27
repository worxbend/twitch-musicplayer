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
	"sync/atomic"
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
	Update(ctx context.Context, song *models.Song) error
	DeleteByPath(ctx context.Context, path string) (*models.Song, bool, error)
	DeleteUnderDir(ctx context.Context, dir string) ([]*models.Song, error)
	List(ctx context.Context) ([]*models.Song, error)
}

// QueueManager is the subset needed by the watcher.
type QueueManager interface {
	AddToQueue(ctx context.Context, songID string, source models.QueueSource) (*models.QueueItem, error)
	RemoveSong(ctx context.Context, songID string) (int, error)
}

// OnNewSong is called after a newly discovered song is registered.
type OnNewSong func(song *models.Song)

// OnSongsRemoved is called once per filesystem event or overflow resync that
// dropped songs from the library (file deleted, renamed away, or directory
// removed).
type OnSongsRemoved func(songs []*models.Song)

// ScanSummary describes the startup folder scan.
type ScanSummary struct {
	MusicFiles     int
	NewlyIndexed   int
	AlreadyIndexed int
	WatchedDirs    int
	Skipped        int
}

// errOutsideLibrary marks a music file that is unreadable or a symlink whose
// target lies outside music_dir; streaming would refuse it with 403 anyway.
var errOutsideLibrary = errors.New("resolves outside music_dir or is unreadable")

// settleWorkers caps how many new files are polled for settling at once, so
// dropping a large folder does not start one stat loop per file.
const settleWorkers = 4

// Watcher monitors a directory tree for new audio files and registers them.
type Watcher struct {
	dir string
	// resolvedDir is dir with symlinks resolved, used only for containment
	// checks; song paths and IDs stay derived from dir.
	resolvedDir string
	extensions  map[string]struct{}
	songRepo    SongRepository
	queueMgr    QueueManager
	fsWatcher   *fsnotify.Watcher
	onNewSong   OnNewSong
	logger      *log.Logger

	// A new file is indexed once its size and modtime hold still for one
	// settlePoll, so half-copied files are not indexed or queued truncated.
	settlePoll time.Duration
	settling   sync.WaitGroup
	inflightMu sync.Mutex
	// inflight maps a claimed path to whether another pass was requested.
	inflight  map[string]bool
	settleSem chan struct{}
	// settleWait is w.waitSettled; it is a field only as a test seam.
	settleWait func(ctx context.Context, path string) error
	// resyncing collapses back-to-back event overflows into one resync.
	resyncing atomic.Bool
	// rewatchRunning and rewatchAgain coalesce re-watch requests: a request
	// that arrives during a walk causes exactly one more walk after it.
	rewatchRunning atomic.Bool
	rewatchAgain   atomic.Bool

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
	resolvedDir, err := filepath.EvalSymlinks(absDir)
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
		dir:         absDir,
		resolvedDir: resolvedDir,
		extensions:  extSet,
		songRepo:    songRepo,
		queueMgr:    queueMgr,
		fsWatcher:   fsw,
		onNewSong:   onNewSong,
		logger:      log.New(log.Writer(), "[watcher] ", log.LstdFlags),

		settlePoll: 250 * time.Millisecond,
		inflight:   make(map[string]bool),
		settleSem:  make(chan struct{}, settleWorkers),
	}
	w.settleWait = w.waitSettled
	return w, nil
}

// ScanExisting walks the entire music directory tree and inserts any audio files
// not yet tracked in the DB. Does NOT add songs to the queue. A file that fails
// to register is logged and counted in Skipped, not MusicFiles, so one bad
// entry cannot leave the rest of the library unindexed.
func (w *Watcher) ScanExisting(ctx context.Context) (*ScanSummary, error) {
	summary := &ScanSummary{}
	watched, err := w.walkTree(ctx, w.dir, func(path string) error {
		_, created, err := w.registerFile(ctx, path, false)
		if err != nil {
			if ctx.Err() != nil {
				return err
			}
			w.logger.Printf("skip %s: %v", path, err)
			summary.Skipped++
			return nil
		}
		summary.MusicFiles++
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
	walkRoot := root
	if root == w.dir {
		// WalkDir does not follow a symlinked root; the trailing separator
		// makes it follow music_dir when that is a link to the real folder.
		walkRoot = root + string(filepath.Separator)
	}
	watched := 0
	err := filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == walkRoot {
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
			if filepath.Clean(path) != w.dir {
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
			w.handleEvent(ctx, event)

		case err, ok := <-w.fsWatcher.Errors:
			if !ok {
				return nil
			}
			w.handleError(ctx, err)

		case <-ctx.Done():
			return nil
		}
	}
}

// handleEvent applies one filesystem event to the library.
func (w *Watcher) handleEvent(ctx context.Context, event fsnotify.Event) {
	if event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
		// A rename's new path arrives separately as a Create event.
		w.unregisterPath(ctx, event.Name)
		if !w.isMusicFile(event.Name) {
			// inotify gives a moved folder its old watch descriptor, and
			// fsnotify then removes that shared watch for the old path, so
			// the folder's new path can silently stop reporting files.
			w.requestRewatch(ctx, "removed "+event.Name)
		}
		return
	}
	if event.Has(fsnotify.Create) {
		w.handleCreate(ctx, event.Name)
	} else if event.Has(fsnotify.Write) && w.isMusicFile(event.Name) {
		// An in-place tag save; claim folds a burst of Writes that lands
		// mid-flight into exactly one more pass.
		w.registerWhenSettled(ctx, event.Name)
	}
}

// requestRewatch re-walks the library in the background to re-add every
// folder watch and index files a lost watch missed. It runs in w.settling so
// Start waits for it. A request during a walk triggers one more walk, since
// the watch removal it answers can land after the running walk re-added it.
func (w *Watcher) requestRewatch(ctx context.Context, reason string) {
	w.rewatchAgain.Store(true)
	if !w.rewatchRunning.CompareAndSwap(false, true) {
		return
	}
	w.settling.Go(func() {
		for {
			for w.rewatchAgain.Swap(false) {
				w.rewatch(ctx, reason)
			}
			w.rewatchRunning.Store(false)
			// A request between the last Swap and Store saw the walk running
			// and left; pick it up unless a new walker already did.
			if !w.rewatchAgain.Load() || !w.rewatchRunning.CompareAndSwap(false, true) {
				return
			}
		}
	})
}

func (w *Watcher) rewatch(ctx context.Context, reason string) {
	watched, err := w.rescanTree(ctx)
	if err != nil {
		if ctx.Err() == nil {
			w.logger.Printf("rewatch after %s: %v", reason, err)
		}
		return
	}
	w.logger.Printf("rewatch after %s: %d folders", reason, watched)
}

// handleError logs a watcher error. After an event-queue overflow, Create,
// Remove and Write events were lost, so the library is resynced with the disk.
func (w *Watcher) handleError(ctx context.Context, err error) {
	w.logger.Printf("fsnotify error: %v", err)
	if errors.Is(err, fsnotify.ErrEventOverflow) {
		w.resync(ctx)
	}
}

// resync re-walks the tree in the background to watch every folder and index
// new files, then drops songs whose files are gone. It runs in w.settling so
// Start waits for it, and an overflow during a running resync is ignored.
// Retags lost in the overflow are not re-read.
func (w *Watcher) resync(ctx context.Context) {
	if !w.resyncing.CompareAndSwap(false, true) {
		return
	}
	w.settling.Go(func() {
		defer w.resyncing.Store(false)
		if _, err := w.rescanTree(ctx); err != nil {
			w.logger.Printf("resync scan: %v", err)
		}
		removed, err := w.sweepMissing(ctx)
		if err != nil {
			w.logger.Printf("resync sweep: %v", err)
		}
		w.logger.Printf("resync after event overflow: removed %d", removed)
	})
}

// rescanTree watches every folder in the library and queues each music file
// that is not indexed yet for registration. It returns the watched folders.
func (w *Watcher) rescanTree(ctx context.Context) (int, error) {
	return w.walkTree(ctx, w.dir, func(path string) error {
		indexed, err := w.songRepo.ExistsByPath(ctx, path)
		if err != nil {
			w.logger.Printf("rescan %s: %v", path, err)
			return nil
		}
		if !indexed {
			w.registerWhenSettled(ctx, path)
		}
		return nil
	})
}

// sweepMissing unregisters every indexed song whose file no longer exists,
// with one OnRemoved call for the whole sweep, and returns how many it dropped.
func (w *Watcher) sweepMissing(ctx context.Context) (int, error) {
	songs, err := w.songRepo.List(ctx)
	if err != nil {
		return 0, err
	}
	stale, err := w.deleteMissing(ctx, songs)
	w.dropSongs(ctx, stale)
	return len(stale), err
}

// deleteMissing deletes every song in songs whose file is gone from the
// repository and returns the deleted songs, even when ctx ends midway.
func (w *Watcher) deleteMissing(ctx context.Context, songs []*models.Song) ([]*models.Song, error) {
	var stale []*models.Song
	for _, song := range songs {
		if err := ctx.Err(); err != nil {
			return stale, err
		}
		if _, err := os.Stat(song.Path); !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		deleted, found, err := w.songRepo.DeleteByPath(ctx, song.Path)
		if err != nil {
			w.logger.Printf("unregister %s: %v", song.Path, err)
			continue
		}
		if found {
			stale = append(stale, deleted)
		}
	}
	return stale, nil
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
// changing. An event for a path already in flight schedules exactly one more
// pass, so a tag saved after the metadata read is not lost. That pass finds
// the song indexed and only refreshes it, so a directory scan racing the
// file's own Create event still queues it once.
func (w *Watcher) registerWhenSettled(ctx context.Context, path string) {
	if !w.claim(path) {
		return
	}
	w.settling.Go(func() {
		for {
			if err := w.awaitSettled(ctx, path); err != nil {
				if ctx.Err() == nil {
					w.logger.Printf("skip %s: %v", path, err)
				}
				w.release(path)
				return
			}
			if _, _, err := w.registerFile(ctx, path, true); err != nil {
				w.logger.Printf("register %s: %v", path, err)
			}
			if !w.releaseOrRerun(path) {
				return
			}
		}
	})
}

// awaitSettled waits for a free settle slot, then for path to settle. The slot
// is released as soon as the wait ends: the cap bounds stat polling, and
// registerFile is short.
func (w *Watcher) awaitSettled(ctx context.Context, path string) error {
	select {
	case w.settleSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-w.settleSem }()
	return w.settleWait(ctx, path)
}

// claim reports whether the caller now owns path. If path is already in
// flight, it asks the owner for one more pass instead.
func (w *Watcher) claim(path string) bool {
	w.inflightMu.Lock()
	defer w.inflightMu.Unlock()
	if _, busy := w.inflight[path]; busy {
		w.inflight[path] = true
		return false
	}
	w.inflight[path] = false
	return true
}

// releaseOrRerun drops the claim on path and returns false, unless another
// pass was requested: then it keeps the claim and returns true.
func (w *Watcher) releaseOrRerun(path string) bool {
	w.inflightMu.Lock()
	defer w.inflightMu.Unlock()
	if w.inflight[path] {
		w.inflight[path] = false
		return true
	}
	delete(w.inflight, path)
	return false
}

// release drops the claim on path, discarding any requested pass.
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
