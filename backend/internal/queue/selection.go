package queue

import (
	"context"
	"fmt"
	"maps"
	"math/rand"
	"slices"
	"strings"

	"lofi-radio-backend/internal/models"
)

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
