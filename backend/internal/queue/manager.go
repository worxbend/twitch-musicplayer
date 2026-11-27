package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"maps"
	"math/rand"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"lofi-radio-backend/internal/models"
)

// ErrQueueEmpty is returned by NextSong when using manual_only strategy and no songs are queued.
var ErrQueueEmpty = errors.New("queue is empty")

// ErrLibraryEmpty is returned by NextSong when an auto strategy has no songs to pick from.
var ErrLibraryEmpty = errors.New("no songs available")

// ErrSongNotFound is returned by AddToQueue when the requested song does not exist.
var ErrSongNotFound = errors.New("song not found")

// ErrSongBlocked is returned by AddToQueue and PlayNext when the streamer
// pulled the requested song out of rotation.
var ErrSongBlocked = errors.New("song is blocked")

// errNoBlocklist is returned by BlockSong and UnblockSong when the Manager was
// built without a Blocklist.
var errNoBlocklist = errors.New("no blocklist configured")

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

// Blocklist is the persisted set of song IDs the streamer pulled out of
// rotation. Implementations are safe for concurrent use.
type Blocklist interface {
	Has(songID string) bool
	Add(songID string) error
	Remove(songID string) (bool, error)
	List() []string
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

	song, err := m.dequeueLive(ctx)
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
			return nil, fmt.Errorf("preload queue: %w", err)
		}
	case FillAutoRefill:
		if err := m.fillQueue(ctx, m.cfg.Fill.MinAhead, song.Artist, song.ID); err != nil {
			return nil, fmt.Errorf("auto-refill queue: %w", err)
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
// It returns (nil, nil) when the queue runs dry. Must be called with m.mu held.
func (m *Manager) dequeueLive(ctx context.Context) (*models.Song, error) {
	for {
		item, err := m.queue.Dequeue(ctx)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && item == nil) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("dequeue: %w", err)
		}
		song, err := m.songs.GetByID(ctx, item.SongID)
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("queue: skipping entry %s, song %s is no longer in the library", item.ID, item.SongID)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get queued song: %w", err)
		}
		if m.isBlocked(song.ID) {
			log.Printf("queue: skipping entry %s, song %s is blocked", item.ID, item.SongID)
			continue
		}
		return song, nil
	}
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

// recentWindow returns how many recent plays the weighted strategy considers,
// clamped so it never covers the entire library (which would flatten weights).
func (m *Manager) recentWindow(total int) int {
	w := m.cfg.Shuffle.RecentWindow
	if w <= 0 {
		w = total / 2
		if w > 20 {
			w = 20
		}
	}
	if w >= total {
		w = total - 1
	}
	if w < 0 {
		w = 0
	}
	return w
}

// recentDistance returns how many plays ago songID was last played within the
// window (0 = most recent), or -1 if it is outside the window.
// Must be called with m.mu held.
func (m *Manager) recentDistance(songID string, window int) int {
	for k := 0; k < window && k < len(m.recent); k++ {
		if m.recent[len(m.recent)-1-k] == songID {
			return k
		}
	}
	return -1
}

// pick chooses one song from candidates, avoiding the given artist when an
// alternative exists and applying recency weighting for weighted_history.
// Must be called with m.mu held.
func (m *Manager) pick(candidates []*models.Song, avoidArtist string, total int) *models.Song {
	if len(candidates) == 0 {
		return nil
	}

	// Artist adjacency: drop same-artist candidates when alternatives remain.
	if a := strings.TrimSpace(avoidArtist); a != "" {
		filtered := make([]*models.Song, 0, len(candidates))
		for _, s := range candidates {
			if !strings.EqualFold(strings.TrimSpace(s.Artist), a) {
				filtered = append(filtered, s)
			}
		}
		if len(filtered) > 0 {
			candidates = filtered
		}
	}

	if m.cfg.Shuffle.Strategy == ShuffleWeightedHistory {
		return m.weightedPick(candidates, total)
	}
	return candidates[rand.Intn(len(candidates))] //nolint:gosec
}

// weightedPick performs a recency-weighted random selection: songs played more
// recently receive a smaller weight, so the rotation feels less repetitive.
// Must be called with m.mu held.
func (m *Manager) weightedPick(candidates []*models.Song, total int) *models.Song {
	window := m.recentWindow(total)
	weights := make([]float64, len(candidates))
	var sum float64
	for i, s := range candidates {
		w := 1.0
		if window > 0 {
			if d := m.recentDistance(s.ID, window); d >= 0 {
				// most-recent (d=0) → smallest weight; older → closer to 1.
				w = (float64(d) + 1) / (float64(window) + 1)
			}
		}
		weights[i] = w
		sum += w
	}
	if sum <= 0 {
		return candidates[rand.Intn(len(candidates))] //nolint:gosec
	}
	r := rand.Float64() * sum //nolint:gosec
	for i, w := range weights {
		r -= w
		if r <= 0 {
			return candidates[i]
		}
	}
	return candidates[len(candidates)-1]
}

// autoSelect picks the next song according to the shuffle strategy.
// Must be called with m.mu held.
func (m *Manager) autoSelect(ctx context.Context) (*models.Song, error) {
	allSongs, err := m.rotation(ctx)
	if err != nil {
		return nil, err
	}
	if len(allSongs) == 0 {
		return nil, ErrLibraryEmpty
	}

	m.pruneCounts(allSongs)
	m.resetCycleIfComplete(allSongs, nil)
	return m.pickByStrategy(allSongs, m.lastArtist, len(allSongs))
}

// rotation lists the songs auto-select may pick: the library minus blocked
// songs. Blocked songs must not count toward the rotation cycle, or it would
// never complete.
func (m *Manager) rotation(ctx context.Context) ([]*models.Song, error) {
	allSongs, err := m.songs.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list songs: %w", err)
	}
	if m.blocklist == nil {
		return allSongs, nil
	}
	return slices.DeleteFunc(allSongs, func(s *models.Song) bool { return m.blocklist.Has(s.ID) }), nil
}

// isBlocked reports whether songID is pulled out of rotation.
func (m *Manager) isBlocked(songID string) bool {
	return m.blocklist != nil && m.blocklist.Has(songID)
}

// resetCycleIfComplete starts a new rotation cycle once every song is either
// played or in flight (queued, or playing now). In-flight songs were picked in
// the finished cycle but are marked played only later, so they are carried
// over instead of counting against the new cycle. Must be called with m.mu held.
func (m *Manager) resetCycleIfComplete(allSongs []*models.Song, inFlight map[string]struct{}) {
	if m.cfg.Shuffle.Strategy == ShuffleRandom {
		return
	}
	for _, s := range allSongs {
		if _, queued := inFlight[s.ID]; !queued && !m.tracker.HasPlayed(s.ID) {
			return
		}
	}
	// Size the next cycle for songs the watcher added since startup.
	m.tracker.EnsureCapacity(uint(len(allSongs)))
	m.tracker.Reset()
	m.carryover = maps.Clone(inFlight)
}

// pickByStrategy narrows songs to the strategy's candidate pool, then delegates
// to pick() for artist-diversity and recency weighting. total is the full
// library size (used to size the recency window).
func (m *Manager) pickByStrategy(songs []*models.Song, avoidArtist string, total int) (*models.Song, error) {
	if len(songs) == 0 {
		return nil, fmt.Errorf("no candidate songs available")
	}

	switch m.cfg.Shuffle.Strategy {
	case ShuffleRandom:
		// Pure random honours its contract — no diversity shaping.
		return songs[rand.Intn(len(songs))], nil //nolint:gosec

	case ShuffleRoundRobin, ShuffleWeightedHistory:
		// Play every song before repeating any (bloom filter cycle).
		unplayed := filterUnplayed(songs, m.tracker)
		if len(unplayed) == 0 {
			unplayed = songs
		}
		return m.pick(unplayed, avoidArtist, total), nil

	case ShuffleLeastPlayed:
		// Fewest plays since startup first; ties prefer songs not yet played
		// this cycle. Counts live in memory and reset on restart, so after a
		// restart this behaves like round_robin over the persisted Bloom cycle.
		// New imports start at zero and jump ahead until they catch up.
		pool := m.leastPlayed(songs)
		if unplayed := filterUnplayed(pool, m.tracker); len(unplayed) > 0 {
			pool = unplayed
		}
		return m.pick(pool, avoidArtist, total), nil

	default:
		return songs[rand.Intn(len(songs))], nil //nolint:gosec
	}
}

// leastPlayed returns the songs with the lowest play count (missing = 0).
// Must be called with m.mu held.
func (m *Manager) leastPlayed(songs []*models.Song) []*models.Song {
	lowest := m.playCounts[songs[0].ID]
	for _, s := range songs[1:] {
		lowest = min(lowest, m.playCounts[s.ID])
	}
	out := make([]*models.Song, 0, len(songs))
	for _, s := range songs {
		if m.playCounts[s.ID] == lowest {
			out = append(out, s)
		}
	}
	return out
}

// pruneCounts drops play counts for songs that left the rotation, keeping
// blocked songs' counts so an unblocked song does not jump the line. Counts
// exist only for played songs, so the pass runs only once the map outgrows the
// rotation. Must be called with m.mu held.
func (m *Manager) pruneCounts(rotation []*models.Song) {
	if len(m.playCounts) <= len(rotation) {
		return
	}
	live := make(map[string]struct{}, len(rotation))
	for _, s := range rotation {
		live[s.ID] = struct{}{}
	}
	for id := range m.playCounts {
		if _, ok := live[id]; !ok && !m.isBlocked(id) {
			delete(m.playCounts, id)
		}
	}
}

// filterUnplayed returns the subset of songs the tracker has not seen.
func filterUnplayed(songs []*models.Song, t PlayedTracker) []*models.Song {
	out := make([]*models.Song, 0, len(songs))
	for _, s := range songs {
		if !t.HasPlayed(s.ID) {
			out = append(out, s)
		}
	}
	return out
}

// fillQueue refills the queue up to targetDepth using the shuffle strategy.
// prevArtist is the artist of the song that plays just before the queue, used
// when the queue is empty. Must be called with m.mu held.
func (m *Manager) fillQueue(ctx context.Context, targetDepth int, prevArtist string, excludeSongIDs ...string) error {
	if targetDepth <= 0 {
		return nil
	}

	items, err := m.queue.List(ctx)
	if err != nil {
		return fmt.Errorf("list queue: %w", err)
	}

	queued := make(map[string]struct{}, len(items))
	for _, item := range items {
		queued[item.SongID] = struct{}{}
	}
	for _, id := range excludeSongIDs {
		if id != "" {
			queued[id] = struct{}{}
		}
	}
	// A carried-over song whose entry left the queue (removed by an admin or
	// purged with its file) belongs to the new cycle once it is picked again.
	for id := range m.carryover {
		if _, ok := queued[id]; !ok {
			delete(m.carryover, id)
		}
	}

	// Seed adjacency avoidance with the song that plays just before the first
	// new entry (the queue tail, else prevArtist), then chain through the batch.
	avoidArtist := m.tailArtist(ctx, items, prevArtist)
	for len(items) < targetDepth {
		song, err := m.autoSelectExcluding(ctx, queued, avoidArtist)
		if err != nil {
			return err
		}
		if song == nil {
			break
		}
		item := newQueueItem(song.ID, models.QueueSourceAuto)
		if err := m.queue.Enqueue(ctx, item); err != nil {
			return fmt.Errorf("enqueue auto song: %w", err)
		}
		items = append(items, item)
		queued[song.ID] = struct{}{}
		avoidArtist = song.Artist
	}
	return nil
}

// tailArtist returns the artist of the last queued song, or fallback when the
// queue is empty or its tail no longer resolves to a song.
func (m *Manager) tailArtist(ctx context.Context, items []*models.QueueItem, fallback string) string {
	if len(items) == 0 {
		return fallback
	}
	tail, err := m.songs.GetByID(ctx, items[len(items)-1].SongID)
	if err != nil || tail == nil {
		return fallback
	}
	return tail.Artist
}

func (m *Manager) autoSelectExcluding(ctx context.Context, queued map[string]struct{}, avoidArtist string) (*models.Song, error) {
	allSongs, err := m.rotation(ctx)
	if err != nil {
		return nil, err
	}
	if len(allSongs) == 0 {
		return nil, ErrLibraryEmpty
	}
	m.pruneCounts(allSongs)
	m.resetCycleIfComplete(allSongs, queued)

	filtered := make([]*models.Song, 0, len(allSongs))
	for _, song := range allSongs {
		if _, exists := queued[song.ID]; !exists {
			filtered = append(filtered, song)
		}
	}
	if len(filtered) == 0 {
		return nil, nil
	}

	return m.pickByStrategy(filtered, avoidArtist, len(allSongs))
}

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

// TotalSongs reports how many indexed songs are in rotation (not blocked).
func (m *Manager) TotalSongs(ctx context.Context) (int, error) {
	rotation, err := m.rotation(ctx)
	return len(rotation), err
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
