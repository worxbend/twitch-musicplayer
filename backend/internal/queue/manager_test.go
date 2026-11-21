package queue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"lofi-radio-backend/internal/models"
	"lofi-radio-backend/internal/played"
	"lofi-radio-backend/internal/repository"
)

// buildManager wires a Manager over real in-memory repositories and a bloom
// tracker, seeded with the given songs.
func buildManager(t *testing.T, cfg ManagerConfig, songs []*models.Song) *Manager {
	t.Helper()
	m, _ := buildManagerWithSongs(t, cfg, songs)
	return m
}

// buildManagerWithSongs is buildManager that also returns the song repo so a
// test can change the library after queueing.
func buildManagerWithSongs(t *testing.T, cfg ManagerConfig, songs []*models.Song) (*Manager, *repository.MemSongRepo) {
	t.Helper()
	ctx := context.Background()
	songRepo := repository.NewMemSongRepo()
	for _, s := range songs {
		if err := songRepo.Create(ctx, s); err != nil {
			t.Fatalf("seed song %s: %v", s.ID, err)
		}
	}
	queueRepo := repository.NewMemQueueRepo(songRepo)
	tracker := played.New(1000, 0.01)
	return New(songRepo, tracker, queueRepo, cfg), songRepo
}

func song(id, title, artist string) *models.Song {
	return &models.Song{ID: id, Filename: id + ".mp3", Title: title, Artist: artist}
}

// autoRefillCfg selects auto-refill with no look-ahead so NextSong purely
// auto-selects without pre-filling the queue.
func autoRefillCfg(strategy ShuffleStrategy) ManagerConfig {
	return ManagerConfig{
		Shuffle: ShuffleConfig{Strategy: strategy},
		Fill:    FillConfig{Strategy: FillAutoRefill, MinAhead: 0},
	}
}

// TestNoSameArtistBackToBack verifies the diversity rule: with a two-song,
// two-artist library the rotation must strictly alternate artists after the
// first (random) pick, for every non-random strategy.
func TestNoSameArtistBackToBack(t *testing.T) {
	strategies := []ShuffleStrategy{ShuffleRoundRobin, ShuffleWeightedHistory, ShuffleLeastPlayed}
	for _, strat := range strategies {
		t.Run(string(strat), func(t *testing.T) {
			ctx := context.Background()
			m := buildManager(t, autoRefillCfg(strat), []*models.Song{
				song("x1", "X One", "Artist X"),
				song("y1", "Y One", "Artist Y"),
			})
			var prevArtist string
			for i := 0; i < 20; i++ {
				s, err := m.NextSong(ctx)
				if err != nil {
					t.Fatalf("NextSong[%d]: %v", i, err)
				}
				if i > 0 && s.Artist == prevArtist {
					t.Fatalf("pick %d repeated artist %q (an alternative existed)", i, s.Artist)
				}
				prevArtist = s.Artist
			}
		})
	}
}

// TestFullCycleCoverage verifies that non-random strategies play every song
// exactly once before repeating any (the bloom-filter cycle).
func TestFullCycleCoverage(t *testing.T) {
	strategies := []ShuffleStrategy{ShuffleRoundRobin, ShuffleWeightedHistory, ShuffleLeastPlayed}
	for _, strat := range strategies {
		t.Run(string(strat), func(t *testing.T) {
			ctx := context.Background()
			songs := []*models.Song{
				song("a", "A", "AA"), song("b", "B", "BB"),
				song("c", "C", "CC"), song("d", "D", "DD"),
				song("e", "E", "EE"),
			}
			m := buildManager(t, autoRefillCfg(strat), songs)
			seen := map[string]bool{}
			for i := 0; i < len(songs); i++ {
				s, err := m.NextSong(ctx)
				if err != nil {
					t.Fatalf("NextSong[%d]: %v", i, err)
				}
				if seen[s.ID] {
					t.Fatalf("song %s repeated before the cycle completed", s.ID)
				}
				seen[s.ID] = true
			}
			if len(seen) != len(songs) {
				t.Fatalf("expected %d distinct songs, got %d", len(songs), len(seen))
			}
		})
	}
}

// TestWeightedHistoryFavoursStaleSongs checks that weighted_history biases away
// from recently played songs. With a single artist (diversity disabled) and a
// large library, the song that has gone longest without a play should, across
// many trials, be picked far more often than the just-played one.
func TestWeightedHistoryFavoursStaleSongs(t *testing.T) {
	ctx := context.Background()
	// Single artist so artist-adjacency never fires; isolates recency weighting.
	songs := []*models.Song{
		song("s1", "S1", "One"), song("s2", "S2", "One"),
		song("s3", "S3", "One"), song("s4", "S4", "One"),
		song("s5", "S5", "One"), song("s6", "S6", "One"),
	}
	m := buildManager(t, ManagerConfig{
		Shuffle: ShuffleConfig{Strategy: ShuffleWeightedHistory, RecentWindow: 3},
		Fill:    FillConfig{Strategy: FillManualOnly},
	}, songs)

	// Prime history: s1 is the most recent, s6 has never been played.
	m.recordPlay(song("s6", "S6", "One")) // stale (oldest / outside window)
	m.recordPlay(song("s3", "S3", "One"))
	m.recordPlay(song("s2", "S2", "One"))
	m.recordPlay(song("s1", "S1", "One")) // most recent

	all, _ := m.songs.List(ctx)
	counts := map[string]int{}
	const trials = 4000
	for i := 0; i < trials; i++ {
		s := m.weightedPick(all, len(all))
		counts[s.ID]++
	}
	// s1 is most recent (heavily down-weighted); s5 was never played (full weight).
	if counts["s5"] <= counts["s1"] {
		t.Fatalf("expected stale song s5 (%d) to beat recent s1 (%d)", counts["s5"], counts["s1"])
	}
}

// TestNextSongEmptyLibrary ensures a graceful error when nothing is indexed.
func TestNextSongEmptyLibrary(t *testing.T) {
	ctx := context.Background()
	m := buildManager(t, autoRefillCfg(ShuffleRoundRobin), nil)
	if _, err := m.NextSong(ctx); err == nil {
		t.Fatal("expected error selecting from empty library")
	}
}

// TestNewKeepsFillConfigWhenShuffleEmpty guards against New replacing the whole
// config with defaults when only the shuffle strategy is unset.
func TestNewKeepsFillConfigWhenShuffleEmpty(t *testing.T) {
	ctx := context.Background()
	m := buildManager(t, ManagerConfig{Fill: FillConfig{Strategy: FillManualOnly}}, []*models.Song{
		song("a1", "A One", "Artist A"),
		song("b1", "B One", "Artist B"),
	})
	if m.cfg.Shuffle.Strategy != ShuffleRoundRobin {
		t.Errorf("shuffle = %q, want default %q", m.cfg.Shuffle.Strategy, ShuffleRoundRobin)
	}
	if got := m.InitialQueueTarget(); got != 0 {
		t.Errorf("InitialQueueTarget = %d, want 0 for manual_only", got)
	}
	added, err := m.PrepareInitialQueue(ctx)
	if err != nil || added != 0 {
		t.Fatalf("PrepareInitialQueue = %d, %v; want 0, nil", added, err)
	}
	if _, err := m.NextSong(ctx); err != ErrQueueEmpty {
		t.Errorf("NextSong err = %v, want ErrQueueEmpty (manual_only must not auto-select)", err)
	}
	if m.AutoSelects() {
		t.Error("AutoSelects = true, want false for manual_only")
	}
}

// TestAddToQueueListQueueNoRace guards against AddToQueue writing to a queue
// entry the repo already shares with concurrent ListQueue readers. Run with -race.
func TestAddToQueueListQueueNoRace(t *testing.T) {
	ctx := context.Background()
	m := buildManager(t, ManagerConfig{Fill: FillConfig{Strategy: FillManualOnly}}, []*models.Song{
		song("a1", "A One", "Artist A"),
	})

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer close(done)
		for i := 0; i < 5000; i++ {
			if _, err := m.AddToQueue(ctx, "a1", models.QueueSourceManual); err != nil {
				t.Errorf("AddToQueue: %v", err)
				return
			}
			if i%8 == 0 {
				if err := m.ClearQueue(ctx); err != nil {
					t.Errorf("ClearQueue: %v", err)
					return
				}
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			if _, err := m.ListQueue(ctx); err != nil {
				t.Errorf("ListQueue: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}

// library returns n songs with distinct IDs, artists and paths.
func library(n int) []*models.Song {
	out := make([]*models.Song, n)
	for i := range out {
		id := fmt.Sprintf("s%d", i)
		out[i] = song(id, "Title "+id, "Artist "+id)
		out[i].Path = "/music/" + id + ".mp3"
	}
	return out
}

// queueStale queues every song, then deletes stale from the library so its
// queue entry points at nothing.
func queueStale(t *testing.T, m *Manager, songs *repository.MemSongRepo, stale *models.Song, queued ...*models.Song) {
	t.Helper()
	ctx := context.Background()
	for _, s := range queued {
		if _, err := m.AddToQueue(ctx, s.ID, models.QueueSourceManual); err != nil {
			t.Fatalf("AddToQueue %s: %v", s.ID, err)
		}
	}
	if _, found, err := songs.DeleteByPath(ctx, stale.Path); err != nil || !found {
		t.Fatalf("DeleteByPath %s = (%v, %v), want (true, nil)", stale.Path, found, err)
	}
}

func listQueue(t *testing.T, m *Manager) []*models.QueueItem {
	t.Helper()
	items, err := m.ListQueue(context.Background())
	if err != nil {
		t.Fatalf("ListQueue: %v", err)
	}
	return items
}

func TestNextSongManualOnlySkipsStaleEntry(t *testing.T) {
	lib := library(2)
	m, songs := buildManagerWithSongs(t, ManagerConfig{Fill: FillConfig{Strategy: FillManualOnly}}, lib)
	queueStale(t, m, songs, lib[0], lib[0], lib[1])

	got, err := m.NextSong(context.Background())
	if err != nil || got.ID != lib[1].ID {
		t.Fatalf("NextSong = (%v, %v), want (%s, nil)", got, err, lib[1].ID)
	}
}

func TestNextSongManualOnlyOnlyStaleEntryIsEmpty(t *testing.T) {
	lib := library(2)
	m, songs := buildManagerWithSongs(t, ManagerConfig{Fill: FillConfig{Strategy: FillManualOnly}}, lib)
	queueStale(t, m, songs, lib[0], lib[0])

	if _, err := m.NextSong(context.Background()); !errors.Is(err, ErrQueueEmpty) {
		t.Fatalf("NextSong err = %v, want ErrQueueEmpty", err)
	}
}

func TestNextSongAutoRefillStaleEntryFallsBackToAutoSelect(t *testing.T) {
	lib := library(2)
	m, songs := buildManagerWithSongs(t, autoRefillCfg(ShuffleRoundRobin), lib)
	queueStale(t, m, songs, lib[0], lib[0])

	got, err := m.NextSong(context.Background())
	if err != nil || got.ID != lib[1].ID {
		t.Fatalf("NextSong = (%v, %v), want live song %s", got, err, lib[1].ID)
	}
}

func TestNextSongPreloadFillsQueueWithoutCurrentSong(t *testing.T) {
	cfg := ManagerConfig{Fill: FillConfig{Strategy: FillPreload, PreloadSize: 3}}
	m := buildManager(t, cfg, library(5))

	got, err := m.NextSong(context.Background())
	if err != nil {
		t.Fatalf("NextSong: %v", err)
	}
	items := listQueue(t, m)
	if len(items) != 3 {
		t.Fatalf("queue length = %d, want PreloadSize 3", len(items))
	}
	for _, item := range items {
		if item.SongID == got.ID {
			t.Fatalf("queue contains just-returned song %s", got.ID)
		}
	}
}

func TestNextSongAutoRefillKeepsMinAhead(t *testing.T) {
	cfg := ManagerConfig{Fill: FillConfig{Strategy: FillAutoRefill, MinAhead: 2}}
	for _, tc := range []struct{ librarySize, want int }{{5, 2}, {2, 1}} {
		t.Run(fmt.Sprintf("library_%d", tc.librarySize), func(t *testing.T) {
			m := buildManager(t, cfg, library(tc.librarySize))
			if _, err := m.NextSong(context.Background()); err != nil {
				t.Fatalf("NextSong: %v", err)
			}
			if got := len(listQueue(t, m)); got != tc.want {
				t.Fatalf("queue length = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestPrepareInitialQueue(t *testing.T) {
	ctx := context.Background()
	t.Run("preload", func(t *testing.T) {
		m := buildManager(t, ManagerConfig{Fill: FillConfig{Strategy: FillPreload, PreloadSize: 3}}, library(5))
		added, err := m.PrepareInitialQueue(ctx)
		if err != nil || added != 3 {
			t.Fatalf("PrepareInitialQueue = (%d, %v), want (3, nil)", added, err)
		}
		if got := len(listQueue(t, m)); got != 3 {
			t.Fatalf("queue length = %d, want 3", got)
		}
	})
	t.Run("manual_only", func(t *testing.T) {
		m := buildManager(t, ManagerConfig{Fill: FillConfig{Strategy: FillManualOnly}}, library(5))
		added, err := m.PrepareInitialQueue(ctx)
		if err != nil || added != 0 {
			t.Fatalf("PrepareInitialQueue = (%d, %v), want (0, nil)", added, err)
		}
		if got := len(listQueue(t, m)); got != 0 {
			t.Fatalf("queue length = %d, want 0", got)
		}
	})
}

// TestFullCycleCoverageWithLookahead verifies that the rotation cycle keeps
// resetting when the queue always holds look-ahead entries, so every block of
// n plays covers the whole library exactly once.
func TestFullCycleCoverageWithLookahead(t *testing.T) {
	fills := map[string]FillConfig{
		"auto_refill": {Strategy: FillAutoRefill, MinAhead: 1},
		"preload":     {Strategy: FillPreload, PreloadSize: 3},
	}
	strategies := []ShuffleStrategy{ShuffleRoundRobin, ShuffleWeightedHistory, ShuffleLeastPlayed}
	for fillName, fill := range fills {
		for _, strat := range strategies {
			t.Run(fillName+"/"+string(strat), func(t *testing.T) {
				for run := 0; run < 20; run++ {
					assertCyclesCoverLibrary(t, ManagerConfig{Shuffle: ShuffleConfig{Strategy: strat}, Fill: fill}, 7, 4)
				}
			})
		}
	}
}

// assertCyclesCoverLibrary plays cycles*n songs and fails unless each block of
// n consecutive plays contains every song ID exactly once.
func assertCyclesCoverLibrary(t *testing.T, cfg ManagerConfig, n, cycles int) {
	t.Helper()
	ctx := context.Background()
	m := buildManager(t, cfg, library(n))
	if _, err := m.PrepareInitialQueue(ctx); err != nil {
		t.Fatalf("PrepareInitialQueue: %v", err)
	}
	for cycle := 0; cycle < cycles; cycle++ {
		seen := make(map[string]bool, n)
		for i := 0; i < n; i++ {
			s, err := m.NextSong(ctx)
			if err != nil {
				t.Fatalf("NextSong[cycle %d, %d]: %v", cycle, i, err)
			}
			if seen[s.ID] {
				t.Fatalf("cycle %d repeated song %s before covering the library", cycle, s.ID)
			}
			seen[s.ID] = true
		}
	}
}

// autoQueuedManager builds a 3-song auto_refill manager with one auto-queued
// look-ahead entry and returns that entry's song ID.
func autoQueuedManager(t *testing.T) (*Manager, string) {
	t.Helper()
	cfg := ManagerConfig{Fill: FillConfig{Strategy: FillAutoRefill, MinAhead: 1}}
	m := buildManager(t, cfg, library(3))
	if _, err := m.PrepareInitialQueue(context.Background()); err != nil {
		t.Fatalf("PrepareInitialQueue: %v", err)
	}
	items := listQueue(t, m)
	if len(items) != 1 || items[0].Source != models.QueueSourceAuto {
		t.Fatalf("queue = %+v, want one auto entry", items)
	}
	return m, items[0].SongID
}

func entriesForSong(items []*models.QueueItem, songID string) []*models.QueueItem {
	var out []*models.QueueItem
	for _, item := range items {
		if item.SongID == songID {
			out = append(out, item)
		}
	}
	return out
}

func TestPlayNextDropsAutoDuplicate(t *testing.T) {
	ctx := context.Background()
	m, songID := autoQueuedManager(t)

	if err := m.PlayNext(ctx, songID); err != nil {
		t.Fatalf("PlayNext: %v", err)
	}
	if got := entriesForSong(listQueue(t, m), songID); len(got) != 1 {
		t.Fatalf("queue holds %d entries for %s, want 1", len(got), songID)
	}
	first, err := m.NextSong(ctx)
	if err != nil {
		t.Fatalf("NextSong[0]: %v", err)
	}
	second, err := m.NextSong(ctx)
	if err != nil {
		t.Fatalf("NextSong[1]: %v", err)
	}
	if first.ID != songID || second.ID == first.ID {
		t.Fatalf("plays = %s, %s; want %s then a different song", first.ID, second.ID, songID)
	}
}

func TestPlayNextKeepsManualDuplicate(t *testing.T) {
	ctx := context.Background()
	m, songID := autoQueuedManager(t)
	manual, err := m.AddToQueue(ctx, songID, models.QueueSourceManual)
	if err != nil {
		t.Fatalf("AddToQueue: %v", err)
	}

	if err := m.PlayNext(ctx, songID); err != nil {
		t.Fatalf("PlayNext: %v", err)
	}
	got := entriesForSong(listQueue(t, m), songID)
	if len(got) != 2 {
		t.Fatalf("queue holds %d entries for %s, want 2 (play-next + manual)", len(got), songID)
	}
	for _, item := range got {
		if item.Source != models.QueueSourceManual {
			t.Fatalf("auto entry %s survived PlayNext", item.ID)
		}
	}
	if got[1].ID != manual.ID {
		t.Fatalf("manual entry %s missing after PlayNext, queue = %+v", manual.ID, got)
	}
}

func TestRequeuePutsSongAtFront(t *testing.T) {
	ctx := context.Background()
	lib := library(2)
	m := buildManager(t, ManagerConfig{Fill: FillConfig{Strategy: FillManualOnly}}, lib)
	for _, s := range lib {
		if _, err := m.AddToQueue(ctx, s.ID, models.QueueSourceManual); err != nil {
			t.Fatalf("AddToQueue %s: %v", s.ID, err)
		}
	}
	handedOut, err := m.NextSong(ctx)
	if err != nil || handedOut.ID != lib[0].ID {
		t.Fatalf("NextSong = (%v, %v), want (%s, nil)", handedOut, err, lib[0].ID)
	}

	if err := m.Requeue(ctx, handedOut.ID); err != nil {
		t.Fatalf("Requeue: %v", err)
	}
	items := listQueue(t, m)
	if len(items) != 2 || items[0].SongID != handedOut.ID || items[0].Source != models.QueueSourceManual {
		t.Fatalf("queue = %+v, want manual %s at the front of 2 entries", items, handedOut.ID)
	}
	again, err := m.NextSong(ctx)
	if err != nil || again.ID != handedOut.ID {
		t.Fatalf("NextSong after Requeue = (%v, %v), want (%s, nil)", again, err, handedOut.ID)
	}
}
