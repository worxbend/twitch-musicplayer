package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"lofi-radio-backend/internal/blocklist"
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

// TestNoSameArtistBackToBackWithLookahead covers the refill path: each queued
// song must avoid the artist of the song that plays just before it, not the
// artist of the song before the current one. Four artists with two songs each
// always leave an alternative within the first seven plays of a cycle.
func TestNoSameArtistBackToBackWithLookahead(t *testing.T) {
	fills := map[string]FillConfig{
		"auto_refill": {Strategy: FillAutoRefill, MinAhead: 1},
		"preload":     {Strategy: FillPreload, PreloadSize: 3},
	}
	strategies := []ShuffleStrategy{ShuffleRoundRobin, ShuffleWeightedHistory, ShuffleLeastPlayed}
	for fillName, fill := range fills {
		for _, strat := range strategies {
			t.Run(fillName+"/"+string(strat), func(t *testing.T) {
				for run := 0; run < 100; run++ {
					assertNoArtistRepeat(t, ManagerConfig{Shuffle: ShuffleConfig{Strategy: strat}, Fill: fill}, 7)
				}
			})
		}
	}
}

func assertNoArtistRepeat(t *testing.T, cfg ManagerConfig, plays int) {
	t.Helper()
	ctx := context.Background()
	m := buildManager(t, cfg, []*models.Song{
		song("a1", "A1", "A"), song("a2", "A2", "A"),
		song("b1", "B1", "B"), song("b2", "B2", "B"),
		song("c1", "C1", "C"), song("c2", "C2", "C"),
		song("d1", "D1", "D"), song("d2", "D2", "D"),
	})
	if _, err := m.PrepareInitialQueue(ctx); err != nil {
		t.Fatalf("PrepareInitialQueue: %v", err)
	}
	var prev *models.Song
	for i := 0; i < plays; i++ {
		s, err := m.NextSong(ctx)
		if err != nil {
			t.Fatalf("NextSong[%d]: %v", i, err)
		}
		if prev != nil && s.Artist == prev.Artist {
			t.Fatalf("play %d: %s followed %s, same artist %q", i, s.ID, prev.ID, s.Artist)
		}
		prev = s
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

// TestNextSongEmptyLibrary ensures every auto strategy reports ErrLibraryEmpty
// when nothing is indexed, so callers can tell it apart from other failures.
func TestNextSongEmptyLibrary(t *testing.T) {
	tests := []struct {
		name string
		cfg  ManagerConfig
	}{
		{name: "auto_refill random", cfg: autoRefillCfg(ShuffleRandom)},
		{name: "auto_refill round_robin", cfg: autoRefillCfg(ShuffleRoundRobin)},
		{name: "preload round_robin", cfg: ManagerConfig{
			Shuffle: ShuffleConfig{Strategy: ShuffleRoundRobin},
			Fill:    FillConfig{Strategy: FillPreload, PreloadSize: 3},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := buildManager(t, tt.cfg, nil)
			if _, err := m.NextSong(context.Background()); !errors.Is(err, ErrLibraryEmpty) {
				t.Fatalf("NextSong error = %v, want ErrLibraryEmpty", err)
			}
		})
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

// staleListRepo wraps a QueueRepository and runs onList after each List has
// taken its snapshot, so a test can change the queue behind a caller's back.
type staleListRepo struct {
	QueueRepository
	onList func()
}

func (r *staleListRepo) List(ctx context.Context) ([]*models.QueueItem, error) {
	items, err := r.QueueRepository.List(ctx)
	if r.onList != nil {
		r.onList()
	}
	return items, err
}

// manualOnlyManager builds a manual_only manager over queue, seeded with lib.
func manualOnlyManager(t *testing.T, songRepo *repository.MemSongRepo, queue QueueRepository, lib []*models.Song) *Manager {
	t.Helper()
	for _, s := range lib {
		if err := songRepo.Create(context.Background(), s); err != nil {
			t.Fatalf("seed song %s: %v", s.ID, err)
		}
	}
	return New(songRepo, played.New(1000, 0.01), queue, ManagerConfig{Fill: FillConfig{Strategy: FillManualOnly}})
}

// An auto entry removed between PlayNext's List and its Remove is already
// gone, which is what PlayNext wanted, so it must not fail.
func TestPlayNextToleratesAutoEntryRemovedMidway(t *testing.T) {
	ctx := context.Background()
	songRepo := repository.NewMemSongRepo()
	inner := repository.NewMemQueueRepo(songRepo)
	stale := &staleListRepo{QueueRepository: inner}
	m := manualOnlyManager(t, songRepo, stale, library(1))
	auto, err := m.AddToQueue(ctx, "s0", models.QueueSourceAuto)
	if err != nil {
		t.Fatalf("AddToQueue: %v", err)
	}
	stale.onList = func() {
		stale.onList = nil
		if err := inner.Remove(ctx, auto.ID); err != nil {
			t.Errorf("remove behind PlayNext: %v", err)
		}
	}

	if err := m.PlayNext(ctx, "s0"); err != nil {
		t.Fatalf("PlayNext: %v", err)
	}
	items := listQueue(t, m)
	if len(items) != 1 || items[0].SongID != "s0" || items[0].Source != models.QueueSourceManual {
		t.Fatalf("queue = %+v, want one manual s0 entry", items)
	}
}

// TestPlayNextRemoveEntryNoRace runs PlayNext against an admin removal of the
// same auto entry. Run with -race.
func TestPlayNextRemoveEntryNoRace(t *testing.T) {
	ctx := context.Background()
	m := buildManager(t, ManagerConfig{Fill: FillConfig{Strategy: FillManualOnly}}, library(1))
	for i := 0; i < 200; i++ {
		auto, err := m.AddToQueue(ctx, "s0", models.QueueSourceAuto)
		if err != nil {
			t.Fatalf("AddToQueue: %v", err)
		}
		var playErr, removeErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); playErr = m.PlayNext(ctx, "s0") }()
		go func() { defer wg.Done(); removeErr = m.RemoveEntry(ctx, auto.ID) }()
		wg.Wait()
		if playErr != nil {
			t.Fatalf("iteration %d: PlayNext: %v", i, playErr)
		}
		if removeErr != nil && !errors.Is(removeErr, sql.ErrNoRows) {
			t.Fatalf("iteration %d: RemoveEntry: %v, want nil or sql.ErrNoRows", i, removeErr)
		}
		if err := m.ClearQueue(ctx); err != nil {
			t.Fatalf("ClearQueue: %v", err)
		}
	}
}

func TestRemoveEntryUnknownID(t *testing.T) {
	m := buildManager(t, ManagerConfig{Fill: FillConfig{Strategy: FillManualOnly}}, library(1))
	if err := m.RemoveEntry(context.Background(), "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("RemoveEntry = %v, want sql.ErrNoRows", err)
	}
}

func TestMoveEntry(t *testing.T) {
	ctx := context.Background()
	lib := library(2)
	m := buildManager(t, ManagerConfig{Fill: FillConfig{Strategy: FillManualOnly}}, lib)
	for _, s := range lib {
		if _, err := m.AddToQueue(ctx, s.ID, models.QueueSourceManual); err != nil {
			t.Fatalf("AddToQueue %s: %v", s.ID, err)
		}
	}
	second := listQueue(t, m)[1]

	if pos, moved, err := m.MoveEntry(ctx, second.ID, -1); err != nil || pos != 1 || !moved {
		t.Fatalf("MoveEntry up = (%d, %v, %v), want (1, true, nil)", pos, moved, err)
	}
	if pos, moved, err := m.MoveEntry(ctx, second.ID, -1); err != nil || pos != 1 || moved {
		t.Fatalf("MoveEntry up at front = (%d, %v, %v), want (1, false, nil)", pos, moved, err)
	}
	if _, _, err := m.MoveEntry(ctx, "missing", 1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("MoveEntry unknown = %v, want sql.ErrNoRows", err)
	}
	if items := listQueue(t, m); items[0].ID != second.ID {
		t.Fatalf("queue head = %s, want %s", items[0].ID, second.ID)
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

// A song carried over into a new cycle whose queue entry is then removed
// belongs to the new cycle: once it plays it must count as played there, or it
// is picked again before the cycle covers the library.
func TestRemovedCarryoverEntryDoesNotRepeatInCycle(t *testing.T) {
	for run := 0; run < 100; run++ {
		assertRemovedCarryoverNoRepeat(t)
	}
}

func assertRemovedCarryoverNoRepeat(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	const n = 6
	cfg := ManagerConfig{
		Shuffle: ShuffleConfig{Strategy: ShuffleRoundRobin},
		Fill:    FillConfig{Strategy: FillPreload, PreloadSize: 2},
	}
	m := buildManager(t, cfg, library(n))
	if _, err := m.PrepareInitialQueue(ctx); err != nil {
		t.Fatalf("PrepareInitialQueue: %v", err)
	}
	// The fifth play picks the seventh song, which completes the first cycle
	// while the queue head is still in flight, so that song is carried over.
	for i := 0; i < n-1; i++ {
		if _, err := m.NextSong(ctx); err != nil {
			t.Fatalf("NextSong[%d]: %v", i, err)
		}
	}
	items := listQueue(t, m)
	if len(items) != 2 {
		t.Fatalf("queue length = %d, want 2", len(items))
	}
	if _, ok := m.carryover[items[0].SongID]; !ok {
		t.Fatalf("setup: %s not carried over (carryover %v)", items[0].SongID, m.carryover)
	}
	if err := m.queue.Remove(ctx, items[0].ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// The next n plays are exactly the new cycle's picks.
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		s, err := m.NextSong(ctx)
		if err != nil {
			t.Fatalf("NextSong after remove [%d]: %v", i, err)
		}
		if seen[s.ID] {
			t.Fatalf("%s repeated within one cycle", s.ID)
		}
		seen[s.ID] = true
	}
}

// buildBlockingManager is buildManagerWithSongs with a blocklist saved under
// a temp dir; blocked IDs are added to the store before the Manager is built.
func buildBlockingManager(t *testing.T, cfg ManagerConfig, songs []*models.Song, blocked ...string) (*Manager, *repository.MemSongRepo, *blocklist.Store) {
	t.Helper()
	store := blocklist.Empty(filepath.Join(t.TempDir(), "radio.blocked.json"))
	for _, id := range blocked {
		if err := store.Add(id); err != nil {
			t.Fatalf("block %s: %v", id, err)
		}
	}
	cfg.Blocklist = store
	m, songRepo := buildManagerWithSongs(t, cfg, songs)
	return m, songRepo, store
}

// sameArtistLibrary is library(n) with one artist, so artist adjacency never
// shapes the picks.
func sameArtistLibrary(n int) []*models.Song {
	lib := library(n)
	for _, s := range lib {
		s.Artist = "One"
	}
	return lib
}

func TestAutoSelectNeverPicksBlockedSong(t *testing.T) {
	fills := map[string]FillConfig{
		"auto_refill": {Strategy: FillAutoRefill},
		"preload":     {Strategy: FillPreload, PreloadSize: 2},
	}
	for _, strat := range []ShuffleStrategy{ShuffleRoundRobin, ShuffleWeightedHistory} {
		for fillName, fill := range fills {
			t.Run(string(strat)+"/"+fillName, func(t *testing.T) {
				cfg := ManagerConfig{Shuffle: ShuffleConfig{Strategy: strat}, Fill: fill}
				m, _, _ := buildBlockingManager(t, cfg, library(5), "s0", "s3")
				for i := 0; i < 50; i++ {
					s, err := m.NextSong(context.Background())
					if err != nil {
						t.Fatalf("NextSong[%d]: %v", i, err)
					}
					if s.ID == "s0" || s.ID == "s3" {
						t.Fatalf("NextSong[%d] picked blocked song %s", i, s.ID)
					}
				}
			})
		}
	}
}

// TestCycleResetsWhenOnlyUnplayedSongIsBlocked: once the last unplayed song is
// blocked the cycle must restart, so each pair of picks covers both songs left.
func TestCycleResetsWhenOnlyUnplayedSongIsBlocked(t *testing.T) {
	ctx := context.Background()
	m, _, _ := buildBlockingManager(t, autoRefillCfg(ShuffleRoundRobin), sameArtistLibrary(3))
	playedIDs := map[string]bool{}
	for i := 0; i < 2; i++ {
		s, err := m.NextSong(ctx)
		if err != nil {
			t.Fatalf("NextSong[%d]: %v", i, err)
		}
		playedIDs[s.ID] = true
	}
	var unplayed string
	for _, id := range []string{"s0", "s1", "s2"} {
		if !playedIDs[id] {
			unplayed = id
		}
	}
	if _, err := m.BlockSong(ctx, unplayed); err != nil {
		t.Fatalf("BlockSong: %v", err)
	}
	for round := 0; round < 10; round++ {
		first, err := m.NextSong(ctx)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		second, err := m.NextSong(ctx)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if first.ID == second.ID || first.ID == unplayed || second.ID == unplayed {
			t.Fatalf("round %d played %s, %s; want both of the unblocked songs", round, first.ID, second.ID)
		}
	}
}

func TestEverySongBlockedIsLibraryEmpty(t *testing.T) {
	ctx := context.Background()
	cfg := ManagerConfig{Fill: FillConfig{Strategy: FillPreload, PreloadSize: 2}}
	m, _, _ := buildBlockingManager(t, cfg, library(2), "s0", "s1")
	if added, err := m.PrepareInitialQueue(ctx); err != nil || added != 0 {
		t.Fatalf("PrepareInitialQueue = %d, %v; want 0, nil", added, err)
	}
	if _, err := m.NextSong(ctx); !errors.Is(err, ErrLibraryEmpty) {
		t.Fatalf("NextSong error = %v, want ErrLibraryEmpty", err)
	}
	if total, err := m.TotalSongs(ctx); err != nil || total != 0 {
		t.Fatalf("TotalSongs = %d, %v; want 0, nil", total, err)
	}
}

func TestAddAndPlayNextRefuseBlockedSong(t *testing.T) {
	ctx := context.Background()
	m, _, _ := buildBlockingManager(t, ManagerConfig{Fill: FillConfig{Strategy: FillManualOnly}}, library(2), "s0")
	if _, err := m.AddToQueue(ctx, "s0", models.QueueSourceManual); !errors.Is(err, ErrSongBlocked) {
		t.Fatalf("AddToQueue error = %v, want ErrSongBlocked", err)
	}
	if err := m.PlayNext(ctx, "s0"); !errors.Is(err, ErrSongBlocked) {
		t.Fatalf("PlayNext error = %v, want ErrSongBlocked", err)
	}
	if items := listQueue(t, m); len(items) != 0 {
		t.Fatalf("queue = %+v, want empty", items)
	}
}

func TestBlockSongPurgesQueuedEntries(t *testing.T) {
	ctx := context.Background()
	m, _, store := buildBlockingManager(t, ManagerConfig{Fill: FillConfig{Strategy: FillManualOnly}}, library(2))
	for _, id := range []string{"s0", "s1", "s0"} {
		if _, err := m.AddToQueue(ctx, id, models.QueueSourceManual); err != nil {
			t.Fatalf("AddToQueue(%s): %v", id, err)
		}
	}
	if _, err := m.BlockSong(ctx, "missing"); !errors.Is(err, ErrSongNotFound) {
		t.Fatalf("BlockSong(missing) = %v, want ErrSongNotFound", err)
	}
	for i, want := range []int{2, 0} { // the second call checks it is idempotent
		if purged, err := m.BlockSong(ctx, "s0"); err != nil || purged != want {
			t.Fatalf("BlockSong[%d] = %d, %v; want %d, nil", i, purged, err, want)
		}
	}
	items := listQueue(t, m)
	if len(items) != 1 || items[0].SongID != "s1" {
		t.Fatalf("queue = %+v, want only s1", items)
	}
	if got := store.List(); len(got) != 1 || got[0] != "s0" {
		t.Fatalf("store = %v, want [s0]", got)
	}
}

// TestNextSongDropsEntryBlockedAfterQueueing covers an entry that was queued
// before its song was blocked without the purge (the store edited directly).
func TestNextSongDropsEntryBlockedAfterQueueing(t *testing.T) {
	ctx := context.Background()
	m, _, store := buildBlockingManager(t, ManagerConfig{Fill: FillConfig{Strategy: FillManualOnly}}, library(2))
	for _, id := range []string{"s0", "s1"} {
		if _, err := m.AddToQueue(ctx, id, models.QueueSourceManual); err != nil {
			t.Fatalf("AddToQueue(%s): %v", id, err)
		}
	}
	if err := store.Add("s0"); err != nil {
		t.Fatal(err)
	}
	s, err := m.NextSong(ctx)
	if err != nil || s.ID != "s1" {
		t.Fatalf("NextSong = %v, %v; want s1", s, err)
	}
	if _, err := m.NextSong(ctx); !errors.Is(err, ErrQueueEmpty) {
		t.Fatalf("NextSong error = %v, want ErrQueueEmpty", err)
	}
}

func TestUnblockSong(t *testing.T) {
	ctx := context.Background()
	m, songRepo, store := buildBlockingManager(t, autoRefillCfg(ShuffleRoundRobin), sameArtistLibrary(2), "s0", "s1")
	if _, _, err := songRepo.DeleteByPath(ctx, "/music/s1.mp3"); err != nil {
		t.Fatal(err)
	}

	if err := m.UnblockSong(ctx, "s1"); err != nil {
		t.Fatalf("UnblockSong(stale s1) = %v, want nil", err)
	}
	if err := m.UnblockSong(ctx, "s1"); !errors.Is(err, ErrSongNotFound) {
		t.Fatalf("UnblockSong(s1 again) = %v, want ErrSongNotFound", err)
	}
	if _, err := m.NextSong(ctx); !errors.Is(err, ErrLibraryEmpty) {
		t.Fatalf("NextSong error = %v, want ErrLibraryEmpty while s0 is blocked", err)
	}
	if err := m.UnblockSong(ctx, "s0"); err != nil {
		t.Fatalf("UnblockSong(s0): %v", err)
	}
	if err := m.UnblockSong(ctx, "s0"); err != nil {
		t.Fatalf("UnblockSong(s0 again) = %v, want nil for an indexed song", err)
	}
	if s, err := m.NextSong(ctx); err != nil || s.ID != "s0" {
		t.Fatalf("NextSong = %v, %v; want s0", s, err)
	}
	if got := store.List(); len(got) != 0 {
		t.Fatalf("store = %v, want empty", got)
	}
}

func TestBlockedSongs(t *testing.T) {
	ctx := context.Background()
	m, songRepo, _ := buildBlockingManager(t, ManagerConfig{}, library(2), "s1", "s0")
	if _, _, err := songRepo.DeleteByPath(ctx, "/music/s1.mp3"); err != nil {
		t.Fatal(err)
	}
	got, err := m.BlockedSongs(ctx)
	if err != nil {
		t.Fatalf("BlockedSongs: %v", err)
	}
	if len(got) != 2 || got[0].ID != "s0" || got[0].Song == nil || got[1].ID != "s1" || got[1].Song != nil {
		t.Fatalf("BlockedSongs = %+v, want s0 with its song, then s1 missing", got)
	}
}

// failingPurgeRepo is a QueueRepository whose RemoveBySongID always fails.
type failingPurgeRepo struct {
	QueueRepository
}

func (failingPurgeRepo) RemoveBySongID(context.Context, string) (int, error) {
	return 0, errors.New("purge failed")
}

func TestBlockSongFailedPurgeBlocksNothing(t *testing.T) {
	ctx := context.Background()
	songRepo := repository.NewMemSongRepo()
	if err := songRepo.Create(ctx, library(1)[0]); err != nil {
		t.Fatal(err)
	}
	store := blocklist.Empty(filepath.Join(t.TempDir(), "radio.blocked.json"))
	m := New(songRepo, played.New(1000, 0.01), failingPurgeRepo{repository.NewMemQueueRepo(songRepo)}, ManagerConfig{
		Fill:      FillConfig{Strategy: FillManualOnly},
		Blocklist: store,
	})
	if purged, err := m.BlockSong(ctx, "s0"); err == nil || purged != 0 {
		t.Fatalf("BlockSong = %d, %v; want 0 and an error", purged, err)
	}
	if got := store.List(); len(got) != 0 {
		t.Fatalf("store = %v, want empty after a failed purge", got)
	}
}

func TestBlockSongFailedSaveReportsPurge(t *testing.T) {
	ctx := context.Background()
	store := blocklist.Empty(filepath.Join(t.TempDir(), "missing-dir", "radio.blocked.json"))
	m, _ := buildManagerWithSongs(t, ManagerConfig{Fill: FillConfig{Strategy: FillManualOnly}, Blocklist: store}, library(2))
	if _, err := m.AddToQueue(ctx, "s0", models.QueueSourceManual); err != nil {
		t.Fatalf("AddToQueue: %v", err)
	}
	purged, err := m.BlockSong(ctx, "s0")
	if err == nil || purged != 1 {
		t.Fatalf("BlockSong = %d, %v; want 1 and an error", purged, err)
	}
	if store.Has("s0") {
		t.Fatal("s0 blocked after a failed save")
	}
	if items := listQueue(t, m); len(items) != 0 {
		t.Fatalf("queue = %+v, want empty after the purge", items)
	}
}

// callLogTracker records EnsureCapacity and Reset calls in order.
type callLogTracker struct {
	*played.Tracker
	calls []string
}

func (c *callLogTracker) EnsureCapacity(n uint) {
	c.calls = append(c.calls, fmt.Sprintf("ensure(%d)", n))
	c.Tracker.EnsureCapacity(n)
}

func (c *callLogTracker) Reset() {
	c.calls = append(c.calls, "reset")
	c.Tracker.Reset()
}

// TestCycleResetSizesTrackerForLibrary: a cycle reset sizes the tracker for
// the current library first, so songs added at runtime are covered next cycle.
func TestCycleResetSizesTrackerForLibrary(t *testing.T) {
	ctx := context.Background()
	songRepo := repository.NewMemSongRepo()
	for _, s := range sameArtistLibrary(3) {
		if err := songRepo.Create(ctx, s); err != nil {
			t.Fatalf("seed song %s: %v", s.ID, err)
		}
	}
	tracker := &callLogTracker{Tracker: played.New(1000, 0.01)}
	m := New(songRepo, tracker, repository.NewMemQueueRepo(songRepo), autoRefillCfg(ShuffleRoundRobin))
	for i := 0; i < 4; i++ {
		if _, err := m.NextSong(ctx); err != nil {
			t.Fatalf("NextSong[%d]: %v", i, err)
		}
	}
	if got := strings.Join(tracker.calls, ","); got != "ensure(3),reset" {
		t.Fatalf("tracker calls = %q, want %q", got, "ensure(3),reset")
	}
}

// leastPlayedRuns repeats the least_played checks on fresh managers, so a
// pass is not random luck.
const leastPlayedRuns = 50

// playN calls NextSong n times and returns the picked IDs in order.
func playN(t *testing.T, m *Manager, n int) []string {
	t.Helper()
	ids := make([]string, n)
	for i := range ids {
		s, err := m.NextSong(context.Background())
		if err != nil {
			t.Fatalf("NextSong[%d]: %v", i, err)
		}
		ids[i] = s.ID
	}
	return ids
}

// TestLeastPlayedPicksNewImportFirst: after one full cycle and one more play,
// the Bloom cycle has restarted, so only the play count tells the fresh import
// apart from the songs not yet replayed. least_played must pick it next.
func TestLeastPlayedPicksNewImportFirst(t *testing.T) {
	for run := 0; run < leastPlayedRuns; run++ {
		m, songRepo := buildManagerWithSongs(t, autoRefillCfg(ShuffleLeastPlayed), library(3))
		playN(t, m, 4)
		if err := songRepo.Create(context.Background(), song("new", "New", "Artist new")); err != nil {
			t.Fatalf("add song: %v", err)
		}
		if got := playN(t, m, 1)[0]; got != "new" {
			t.Fatalf("run %d: picked %s, want the new import", run, got)
		}
	}
}

// TestLeastPlayedSkipsMostPlayedSong: a song played twice waits until every
// song played once has caught up, even though the Bloom cycle just restarted.
func TestLeastPlayedSkipsMostPlayedSong(t *testing.T) {
	ctx := context.Background()
	for run := 0; run < leastPlayedRuns; run++ {
		m := buildManager(t, autoRefillCfg(ShuffleLeastPlayed), sameArtistLibrary(4))
		playN(t, m, 4)
		if _, err := m.AddToQueue(ctx, "s0", models.QueueSourceManual); err != nil {
			t.Fatalf("AddToQueue: %v", err)
		}
		if got := playN(t, m, 1)[0]; got != "s0" {
			t.Fatalf("run %d: manual entry played %s, want s0", run, got)
		}
		if got := playN(t, m, 3); slices.Contains(got, "s0") {
			t.Fatalf("run %d: picks %v include s0 (2 plays) while others had 1", run, got)
		}
	}
}

// TestLeastPlayedZeroCountsFollowBloomCycle: with no plays counted (as after a
// restart), the persisted Bloom marks still decide, as in round_robin.
func TestLeastPlayedZeroCountsFollowBloomCycle(t *testing.T) {
	for run := 0; run < leastPlayedRuns; run++ {
		m := buildManager(t, autoRefillCfg(ShuffleLeastPlayed), sameArtistLibrary(5))
		for _, id := range []string{"s0", "s1", "s2"} {
			m.tracker.MarkPlayed(id)
		}
		got := playN(t, m, 2)
		slices.Sort(got)
		if !slices.Equal(got, []string{"s3", "s4"}) {
			t.Fatalf("run %d: picks %v, want the Bloom-unplayed s3 and s4", run, got)
		}
	}
}

// TestLeastPlayedAvoidsLastArtist: artist adjacency still applies inside the
// lowest-count pool.
func TestLeastPlayedAvoidsLastArtist(t *testing.T) {
	for run := 0; run < leastPlayedRuns; run++ {
		m := buildManager(t, autoRefillCfg(ShuffleLeastPlayed), []*models.Song{
			song("x", "X", "Artist X"), song("y", "Y", "Artist Y"), song("z", "Z", "Artist Z"),
		})
		m.playCounts["z"] = 1
		m.lastArtist = "Artist X"
		if got := playN(t, m, 1)[0]; got != "y" {
			t.Fatalf("run %d: picked %s, want y (x repeats the artist, z has more plays)", run, got)
		}
	}
}

func TestLeastPlayedCountsStayBounded(t *testing.T) {
	ctx := context.Background()

	t.Run("RemoveSong drops the count", func(t *testing.T) {
		m := buildManager(t, autoRefillCfg(ShuffleLeastPlayed), library(2))
		id := playN(t, m, 1)[0]
		if m.playCounts[id] != 1 {
			t.Fatalf("count for %s = %d, want 1", id, m.playCounts[id])
		}
		if _, err := m.RemoveSong(ctx, id); err != nil {
			t.Fatalf("RemoveSong: %v", err)
		}
		if _, ok := m.playCounts[id]; ok {
			t.Fatalf("count for %s survived RemoveSong", id)
		}
	})

	t.Run("pick prunes songs gone from the library but keeps blocked ones", func(t *testing.T) {
		m, _, _ := buildBlockingManager(t, autoRefillCfg(ShuffleLeastPlayed),
			append(library(2), song("blk", "Blocked", "Artist blk")), "blk")
		m.playCounts["gone"] = 1
		m.playCounts["blk"] = 1
		m.playCounts["s0"] = 1
		playN(t, m, 1)
		if _, ok := m.playCounts["gone"]; ok {
			t.Fatal("count for a song gone from the library was kept")
		}
		if m.playCounts["blk"] != 1 {
			t.Fatalf("count for blocked song = %d, want 1", m.playCounts["blk"])
		}
	})

	t.Run("other strategies count nothing", func(t *testing.T) {
		m := buildManager(t, autoRefillCfg(ShuffleRoundRobin), library(2))
		playN(t, m, 3)
		if len(m.playCounts) != 0 {
			t.Fatalf("round_robin kept counts %v", m.playCounts)
		}
	})
}

// TestRequeueRollsBackPlayCount: a requeued song was never delivered, so its
// redelivery must not count as a second play, or least_played starves it for a
// cycle.
func TestRequeueRollsBackPlayCount(t *testing.T) {
	ctx := context.Background()

	t.Run("least_played counts the redelivery once", func(t *testing.T) {
		m := buildManager(t, autoRefillCfg(ShuffleLeastPlayed), library(3))
		id := playN(t, m, 1)[0]
		if err := m.Requeue(ctx, id); err != nil {
			t.Fatalf("Requeue: %v", err)
		}
		if got := playN(t, m, 1)[0]; got != id {
			t.Fatalf("NextSong after Requeue = %s, want %s", got, id)
		}
		if m.playCounts[id] != 1 {
			t.Fatalf("count for %s = %d, want 1", id, m.playCounts[id])
		}
	})

	t.Run("default strategy counts nothing", func(t *testing.T) {
		m := buildManager(t, autoRefillCfg(""), library(3))
		id := playN(t, m, 1)[0]
		if err := m.Requeue(ctx, id); err != nil {
			t.Fatalf("Requeue: %v", err)
		}
		if len(m.playCounts) != 0 {
			t.Fatalf("default strategy kept counts %v", m.playCounts)
		}
	})
}

// TestRequeueSkipsBlockedSong: a song blocked after NextSong handed it out
// must not come back at the queue head when its play message fails, but the
// undelivered play is still rolled back.
func TestRequeueSkipsBlockedSong(t *testing.T) {
	ctx := context.Background()

	t.Run("manual queue gets no entry", func(t *testing.T) {
		lib := library(2)
		m, _, _ := buildBlockingManager(t, ManagerConfig{Fill: FillConfig{Strategy: FillManualOnly}}, lib)
		for _, s := range lib {
			if _, err := m.AddToQueue(ctx, s.ID, models.QueueSourceManual); err != nil {
				t.Fatalf("AddToQueue %s: %v", s.ID, err)
			}
		}
		id := playN(t, m, 1)[0]
		if _, err := m.BlockSong(ctx, id); err != nil {
			t.Fatalf("BlockSong: %v", err)
		}

		if err := m.Requeue(ctx, id); err != nil {
			t.Fatalf("Requeue: %v", err)
		}
		for _, item := range listQueue(t, m) {
			if item.SongID == id {
				t.Fatalf("queue holds blocked %s: %+v", id, item)
			}
		}
	})

	t.Run("least_played count is still rolled back", func(t *testing.T) {
		m, _, _ := buildBlockingManager(t, autoRefillCfg(ShuffleLeastPlayed), library(3))
		id := playN(t, m, 1)[0]
		if _, err := m.BlockSong(ctx, id); err != nil {
			t.Fatalf("BlockSong: %v", err)
		}

		if err := m.Requeue(ctx, id); err != nil {
			t.Fatalf("Requeue: %v", err)
		}
		if n, ok := m.playCounts[id]; ok {
			t.Fatalf("count for blocked %s = %d, want rolled back", id, n)
		}
		for _, item := range listQueue(t, m) {
			if item.SongID == id {
				t.Fatalf("queue holds blocked %s: %+v", id, item)
			}
		}
	})
}

// failingEnqueueRepo is a QueueRepository whose Enqueue fails while failing is
// set. EnqueueFront still works, so a rollback can succeed.
type failingEnqueueRepo struct {
	QueueRepository
	failing bool
}

func (r *failingEnqueueRepo) Enqueue(ctx context.Context, item *models.QueueItem) error {
	if r.failing {
		return errors.New("enqueue failed")
	}
	return r.QueueRepository.Enqueue(ctx, item)
}

// TestNextSongFailedRefillKeepsQueuedEntry: when the refill after a dequeue
// fails, the popped entry goes back to the front unchanged and is not marked
// played, so the request plays once the refill works again.
func TestNextSongFailedRefillKeepsQueuedEntry(t *testing.T) {
	fills := map[string]FillConfig{
		"auto_refill": {Strategy: FillAutoRefill, MinAhead: 1},
		"preload":     {Strategy: FillPreload, PreloadSize: 3},
	}
	for name, fill := range fills {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			songRepo := repository.NewMemSongRepo()
			for _, s := range library(4) {
				if err := songRepo.Create(ctx, s); err != nil {
					t.Fatal(err)
				}
			}
			repo := &failingEnqueueRepo{QueueRepository: repository.NewMemQueueRepo(songRepo)}
			m := New(songRepo, played.New(1000, 0.01), repo, ManagerConfig{Fill: fill})
			queued, err := m.AddToQueue(ctx, "s2", models.QueueSourceManual)
			if err != nil {
				t.Fatalf("AddToQueue: %v", err)
			}

			repo.failing = true
			if got, err := m.NextSong(ctx); err == nil {
				t.Fatalf("NextSong = %v, nil; want a refill error", got)
			}
			items, err := m.ListQueue(ctx)
			if err != nil {
				t.Fatalf("ListQueue: %v", err)
			}
			if len(items) == 0 || items[0].ID != queued.ID || items[0].Position != 1 || items[0].Source != models.QueueSourceManual {
				t.Fatalf("queue head = %+v, want entry %s at position 1 with source manual", items, queued.ID)
			}
			if m.tracker.HasPlayed("s2") {
				t.Fatal("s2 marked played after a failed NextSong")
			}

			repo.failing = false
			got, err := m.NextSong(ctx)
			if err != nil || got.ID != "s2" {
				t.Fatalf("NextSong after refill recovers = (%v, %v), want (s2, nil)", got, err)
			}
		})
	}
}

// countOnlySongRepo is a MemSongRepo that fails the test on List, so
// TotalSongs must count without copying the library. A non-nil getErr is
// returned by every GetByID.
type countOnlySongRepo struct {
	*repository.MemSongRepo
	t      *testing.T
	getErr error
}

func (r *countOnlySongRepo) List(context.Context) ([]*models.Song, error) {
	r.t.Error("TotalSongs called List")
	return nil, nil
}

func (r *countOnlySongRepo) GetByID(ctx context.Context, id string) (*models.Song, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.MemSongRepo.GetByID(ctx, id)
}

// countOnlyManager seeds library(n) into a countOnlySongRepo and blocks the
// given IDs; it uses no blocklist when blocked is nil.
func countOnlyManager(t *testing.T, n int, getErr error, blocked []string) *Manager {
	t.Helper()
	songRepo := &countOnlySongRepo{MemSongRepo: repository.NewMemSongRepo(), t: t, getErr: getErr}
	for _, s := range library(n) {
		if err := songRepo.Create(context.Background(), s); err != nil {
			t.Fatalf("seed song %s: %v", s.ID, err)
		}
	}
	var cfg ManagerConfig
	if blocked != nil {
		store := blocklist.Empty(filepath.Join(t.TempDir(), "radio.blocked.json"))
		for _, id := range blocked {
			if err := store.Add(id); err != nil {
				t.Fatalf("block %s: %v", id, err)
			}
		}
		cfg.Blocklist = store
	}
	return New(songRepo, played.New(1000, 0.01), repository.NewMemQueueRepo(songRepo.MemSongRepo), cfg)
}

func TestTotalSongsCountsRotationWithoutList(t *testing.T) {
	tests := []struct {
		name    string
		blocked []string
		want    int
	}{
		{name: "no blocklist", want: 3},
		{name: "empty blocklist", blocked: []string{}, want: 3},
		{name: "blocked song in library", blocked: []string{"s1"}, want: 2},
		{name: "blocked song left library", blocked: []string{"gone"}, want: 3},
		{name: "every song blocked", blocked: []string{"s0", "s1", "s2"}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := countOnlyManager(t, 3, nil, tt.blocked)
			total, err := m.TotalSongs(context.Background())
			if err != nil || total != tt.want {
				t.Fatalf("TotalSongs = %d, %v; want %d, nil", total, err, tt.want)
			}
		})
	}
}

func TestTotalSongsPropagatesLookupError(t *testing.T) {
	lookupErr := errors.New("disk on fire")
	m := countOnlyManager(t, 3, lookupErr, []string{"s1"})
	if _, err := m.TotalSongs(context.Background()); !errors.Is(err, lookupErr) {
		t.Fatalf("TotalSongs error = %v, want %v", err, lookupErr)
	}
}
