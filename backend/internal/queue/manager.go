package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"lofi-radio-backend/internal/models"
)

// ErrQueueEmpty is returned by NextSong when using manual_only strategy and no songs are queued.
var ErrQueueEmpty = errors.New("queue is empty")

// ErrLibraryEmpty is returned by NextSong when an auto strategy has no songs to pick from.
var ErrLibraryEmpty = errors.New("no songs available")

// ErrSongNotFound is returned by AddToQueue when the requested song does not exist.
var ErrSongNotFound = errors.New("song not found")

// ShuffleStrategy controls how songs are selected for auto-play.
type ShuffleStrategy string

const (
	ShuffleRandom          ShuffleStrategy = "random"
	ShuffleWeightedHistory ShuffleStrategy = "weighted_history"
	ShuffleRoundRobin      ShuffleStrategy = "round_robin"
	ShuffleLeastPlayed     ShuffleStrategy = "least_played"
)

// FillStrategy controls when and how the queue is auto-populated.
type FillStrategy string

const (
	FillManualOnly FillStrategy = "manual_only"
	FillAutoRefill FillStrategy = "auto_refill"
	FillPreload    FillStrategy = "preload"
)

// ShuffleConfig holds per-strategy shuffle parameters.
type ShuffleConfig struct {
	Strategy ShuffleStrategy
	// RecentWindow is how many of the most-recently-played songs the
	// weighted_history strategy down-weights. 0 means automatic:
	// min(totalSongs/2, 20).
	RecentWindow int
}

// FillConfig holds per-strategy queue-fill parameters.
type FillConfig struct {
	Strategy    FillStrategy
	MinAhead    int
	PreloadSize int
}

// ManagerConfig bundles all tunable parameters for the Manager.
type ManagerConfig struct {
	Shuffle ShuffleConfig
	Fill    FillConfig
	// Blocklist holds the songs pulled out of rotation. nil blocks nothing.
	Blocklist Blocklist
}

func defaultConfig() ManagerConfig {
	return ManagerConfig{
		Shuffle: ShuffleConfig{Strategy: ShuffleRoundRobin},
		Fill:    FillConfig{Strategy: FillAutoRefill, MinAhead: 1, PreloadSize: 3},
	}
}

// --- local repository interfaces (avoids circular import) ---

type SongRepository interface {
	List(ctx context.Context) ([]*models.Song, error)
	GetByID(ctx context.Context, id string) (*models.Song, error)
	Count(ctx context.Context) (int, error)
}

// PlayedTracker is the Bloom filter interface for tracking played songs.
type PlayedTracker interface {
	MarkPlayed(songID string)
	HasPlayed(songID string) bool
	Reset()
	// EnsureCapacity sizes the tracker for a library of n songs.
	EnsureCapacity(n uint)
}

type QueueRepository interface {
	Enqueue(ctx context.Context, item *models.QueueItem) error
	EnqueueFront(ctx context.Context, item *models.QueueItem) error
	Dequeue(ctx context.Context) (*models.QueueItem, error)
	List(ctx context.Context) ([]*models.QueueItem, error)
	Remove(ctx context.Context, id string) error
	Move(ctx context.Context, id string, delta int) (position int, moved bool, err error)
	Clear(ctx context.Context) error
	Count(ctx context.Context) (int, error)
	RemoveBySongID(ctx context.Context, songID string) (int, error)
}

// Manager is the brain of the service — decides which song plays next.
type Manager struct {
	songs     SongRepository
	tracker   PlayedTracker
	queue     QueueRepository
	blocklist Blocklist
	cfg       ManagerConfig
	mu        sync.Mutex

	// recent holds the IDs of recently-played songs, oldest first, newest last.
	// Used for recency weighting (weighted_history) and to avoid immediate
	// repeats. Trimmed to a bounded length; the effective window is derived
	// per-selection from the library size.
	recent []string
	// lastArtist is the artist of the most recently played song, used to
	// avoid playing two songs by the same artist back-to-back.
	lastArtist string
	// carryover holds songs that were already queued or playing when the
	// previous rotation cycle completed. They belong to that cycle, so playing
	// them must not mark them played in the new one.
	carryover map[string]struct{}
	// playCounts counts plays per song ID since startup, for least_played
	// only. Bounded by pruneCounts and RemoveSong.
	playCounts map[string]uint32
}

// maxRecentHistory bounds the recent-play ring buffer regardless of window size.
const maxRecentHistory = 256

// New constructs a Manager. Empty strategies fall back to their defaults
// individually; a zero-value ManagerConfig yields the full default config.
func New(songs SongRepository, tracker PlayedTracker, queue QueueRepository, cfg ManagerConfig) *Manager {
	def := defaultConfig()
	if cfg.Shuffle.Strategy == "" {
		cfg.Shuffle.Strategy = def.Shuffle.Strategy
	}
	if cfg.Fill.Strategy == "" {
		cfg.Fill.Strategy = def.Fill.Strategy
		if cfg.Fill.MinAhead == 0 {
			cfg.Fill.MinAhead = def.Fill.MinAhead
		}
		if cfg.Fill.PreloadSize == 0 {
			cfg.Fill.PreloadSize = def.Fill.PreloadSize
		}
	}
	return &Manager{songs: songs, tracker: tracker, queue: queue, blocklist: cfg.Blocklist, cfg: cfg, carryover: map[string]struct{}{}, playCounts: map[string]uint32{}}
}

// InitialQueueTarget returns the configured queue depth to prepare before playback starts.
func (m *Manager) InitialQueueTarget() int {
	return m.initialQueueTarget()
}

// PrepareInitialQueue fills the queue to the configured startup depth.
func (m *Manager) PrepareInitialQueue(ctx context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	targetDepth := m.initialQueueTarget()
	if targetDepth <= 0 {
		return 0, nil
	}

	rotation, err := m.rotation(ctx)
	if err != nil {
		return 0, err
	}
	if len(rotation) == 0 {
		return 0, nil
	}

	before, err := m.queue.Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("count queue before prepare: %w", err)
	}
	if before >= targetDepth {
		return 0, nil
	}

	if err := m.fillQueue(ctx, targetDepth, m.lastArtist); err != nil {
		return 0, err
	}

	after, err := m.queue.Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("count queue after prepare: %w", err)
	}
	return after - before, nil
}

func (m *Manager) initialQueueTarget() int {
	switch m.cfg.Fill.Strategy {
	case FillManualOnly:
		return 0
	case FillAutoRefill:
		return m.cfg.Fill.MinAhead
	case FillPreload:
		return m.cfg.Fill.PreloadSize
	default:
		return 0
	}
}

// AutoSelects reports whether NextSong picks a song itself when the queue is
// empty, which every fill strategy except manual_only does.
func (m *Manager) AutoSelects() bool {
	return m.cfg.Fill.Strategy != FillManualOnly
}

// NextSong selects the next song according to the configured fill and shuffle strategies.
// It marks the song as played in the tracker and returns it.
func (m *Manager) NextSong(ctx context.Context) (*models.Song, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	item, song, err := m.dequeueLive(ctx)
	if err != nil {
		return nil, err
	}

	if song == nil {
		// manual_only: only dequeue; never auto-select.
		if !m.AutoSelects() {
			return nil, ErrQueueEmpty
		}
		song, err = m.autoSelect(ctx)
		if err != nil {
			return nil, fmt.Errorf("auto-select: %w", err)
		}
	}

	switch m.cfg.Fill.Strategy {
	case FillPreload:
		if err := m.fillQueue(ctx, m.cfg.Fill.PreloadSize, song.Artist, song.ID); err != nil {
			return nil, errors.Join(fmt.Errorf("preload queue: %w", err), m.restoreDequeued(ctx, item))
		}
	case FillAutoRefill:
		if err := m.fillQueue(ctx, m.cfg.Fill.MinAhead, song.Artist, song.ID); err != nil {
			return nil, errors.Join(fmt.Errorf("auto-refill queue: %w", err), m.restoreDequeued(ctx, item))
		}
	}

	m.markPlayed(song.ID)
	m.recordPlay(song)
	return song, nil
}

// markPlayed records songID in the current rotation cycle, unless it was
// picked in the previous cycle and carried over. Must be called with m.mu held.
func (m *Manager) markPlayed(songID string) {
	if _, carried := m.carryover[songID]; carried {
		delete(m.carryover, songID)
		return
	}
	m.tracker.MarkPlayed(songID)
}

// dequeueLive pops queue entries until one still points at an indexed song
// that is not blocked. Entries whose song left the library (a dropped fsnotify
// event, or a delete racing a dequeue) or was blocked are logged and dropped.
// It returns the popped entry with its song, so a caller can put the entry
// back unchanged, and (nil, nil, nil) when the queue runs dry. Must be called
// with m.mu held.
func (m *Manager) dequeueLive(ctx context.Context) (*models.QueueItem, *models.Song, error) {
	for {
		item, err := m.queue.Dequeue(ctx)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && item == nil) {
			return nil, nil, nil
		}
		if err != nil {
			return nil, nil, fmt.Errorf("dequeue: %w", err)
		}
		song, err := m.songs.GetByID(ctx, item.SongID)
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("queue: skipping entry %s, song %s is no longer in the library", item.ID, item.SongID)
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("get queued song: %w", err)
		}
		if m.isBlocked(song.ID) {
			log.Printf("queue: skipping entry %s, song %s is blocked", item.ID, item.SongID)
			continue
		}
		return item, song, nil
	}
}

// restoreDequeued puts an entry NextSong popped back at the front of the queue
// when NextSong fails before handing its song out, so a manual request is not
// lost. A nil item (the song was auto-selected) needs nothing. Must be called
// with m.mu held.
func (m *Manager) restoreDequeued(ctx context.Context, item *models.QueueItem) error {
	if item == nil {
		return nil
	}
	if err := m.queue.EnqueueFront(ctx, item); err != nil {
		return fmt.Errorf("requeue entry %s: %w", item.ID, err)
	}
	return nil
}

// recordPlay updates the recency history, last-artist tracking and, for
// least_played, the play count.
// Must be called with m.mu held.
func (m *Manager) recordPlay(song *models.Song) {
	if song == nil {
		return
	}
	m.lastArtist = strings.TrimSpace(song.Artist)
	if m.cfg.Shuffle.Strategy == ShuffleLeastPlayed {
		m.playCounts[song.ID]++
	}
	m.recent = append(m.recent, song.ID)
	if len(m.recent) > maxRecentHistory {
		m.recent = m.recent[len(m.recent)-maxRecentHistory:]
	}
}

// TotalSongs reports how many indexed songs are in rotation (not blocked).
// It counts without copying the library: it runs on every player poll, WS
// snapshot and skip check. Reads are not atomic with library changes, so a
// concurrent scan can skew the count by the songs it touches.
func (m *Manager) TotalSongs(ctx context.Context) (int, error) {
	total, err := m.songs.Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("count songs: %w", err)
	}
	if m.blocklist == nil {
		return total, nil
	}
	for _, id := range m.blocklist.List() {
		_, err := m.songs.GetByID(ctx, id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// Blocked file has left the library; it was never counted.
		case err != nil:
			return 0, fmt.Errorf("get blocked song %s: %w", id, err)
		default:
			total--
		}
	}
	return max(total, 0), nil
}
