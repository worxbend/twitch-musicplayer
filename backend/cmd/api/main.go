package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"lofi-radio-backend/internal/api"
	"lofi-radio-backend/internal/blocklist"
	"lofi-radio-backend/internal/config"
	"lofi-radio-backend/internal/models"
	"lofi-radio-backend/internal/played"
	"lofi-radio-backend/internal/player"
	"lofi-radio-backend/internal/queue"
	"lofi-radio-backend/internal/repository"
	"lofi-radio-backend/internal/server"
	"lofi-radio-backend/internal/watcher"
	"lofi-radio-backend/internal/websocket"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// ── In-memory repositories ────────────────────────────────────────────────
	songRepo := repository.NewMemSongRepo()
	queueRepo := repository.NewMemQueueRepo(songRepo)

	// ── Played tracker (Bloom filter; persisted with the blocklist, history) ──
	playedTracker := played.New(10_000, 0.01)
	if err := playedTracker.Load(cfg.BloomPath); err != nil {
		log.Printf("bloom filter: starting fresh (%v)", err)
	} else {
		log.Printf("bloom filter loaded from %s (~%d songs played)", cfg.BloomPath, playedTracker.ApproxCount())
	}

	// ── Blocklist (songs pulled out of rotation, saved next to the Bloom file) ─
	blocked := loadBlocklist(cfg.BloomPath)

	// ── Queue manager ─────────────────────────────────────────────────────────
	queueMgr := queue.New(songRepo, playedTracker, queueRepo, queue.ManagerConfig{
		Shuffle: queue.ShuffleConfig{
			Strategy:     queue.ShuffleStrategy(cfg.Shuffle.Strategy),
			RecentWindow: cfg.Shuffle.RecentWindow,
		},
		Fill: queue.FillConfig{
			Strategy:    queue.FillStrategy(cfg.Queue.Strategy),
			MinAhead:    cfg.Queue.MinAhead,
			PreloadSize: cfg.Queue.PreloadSize,
		},
		Blocklist: blocked,
	})

	// ── Player state tracker ──────────────────────────────────────────────────
	stateTracker := player.NewStateTracker()
	historyPath := sidecarPath(cfg.BloomPath, ".history.json")
	loadHistory(stateTracker, historyPath)
	if cfg.OBS.NowPlayingFile != "" {
		setupNowPlayingFile(stateTracker, cfg.OBS)
	}

	// ── WebSocket hub ─────────────────────────────────────────────────────────
	hub := websocket.NewHub()
	go hub.Run()

	// ── File watcher ──────────────────────────────────────────────────────────
	fsWatcher, err := watcher.New(
		cfg.MusicDir,
		cfg.Music.Extensions,
		songRepo,
		queueMgr,
		func(_ *models.Song) { broadcastQueueUpdated(context.Background(), hub, queueMgr, "new_file") },
	)
	if err != nil {
		log.Fatalf("watcher init failed: %v", err)
	}
	fsWatcher.OnRemoved = func(_ []*models.Song) {
		broadcastQueueUpdated(context.Background(), hub, queueMgr, "file_removed")
	}
	fsWatcher.OnUpdated = func(song *models.Song) {
		onSongUpdated(context.Background(), stateTracker, hub, queueMgr, song)
	}

	// ── HTTP API handlers ─────────────────────────────────────────────────────
	songsH := api.NewSongsHandler(songRepo, cfg.MusicDir)
	queueH := api.NewQueueHandler(queueMgr, hub)
	playerH := api.NewPlayerHandler(queueRepo, queueMgr, hub, stateTracker, cfg.OBS.NowPlayingFormat)

	// ── HTTP server ───────────────────────────────────────────────────────────
	httpServer := server.New(server.Options{
		Port:           cfg.Port,
		Hub:            hub,
		Songs:          songsH,
		Queue:          queueH,
		Player:         playerH,
		BaseURL:        cfg.BaseURL,
		QueueMgr:       queueMgr,
		StateTracker:   stateTracker,
		AllowedOrigins: cfg.Server.AllowedOrigins,
	})

	// ── Bootstrap ─────────────────────────────────────────────────────────────
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	scanSummary, err := fsWatcher.ScanExisting(ctx)
	if err != nil {
		log.Printf("warning: scan existing failed: %v", err)
	}
	if n, err := songRepo.Count(ctx); err != nil {
		log.Printf("warning: count songs failed, keeping bloom filter size: %v", err)
	} else {
		playedTracker.EnsureCapacity(uint(n))
		log.Printf("bloom filter sized for %d songs (library: %d)", playedTracker.Capacity(), n)
	}
	queuedAdded, err := queueMgr.PrepareInitialQueue(ctx)
	if err != nil {
		log.Printf("warning: initial queue prepare failed: %v", err)
	}
	songs, err := songRepo.List(ctx)
	if err != nil {
		log.Printf("warning: list songs failed: %v", err)
	}
	queueItems, err := queueMgr.ListQueue(ctx)
	if err != nil {
		log.Printf("warning: list queue failed: %v", err)
	}
	logStartupInventory(cfg, scanSummary, songs, queueItems, queueMgr.InitialQueueTarget(), queuedAdded)

	go func() {
		if err := fsWatcher.Start(ctx); err != nil {
			log.Printf("watcher stopped: %v", err)
		}
	}()
	go playedTracker.RunAutosave(ctx, cfg.BloomPath, time.Minute, log.Printf)
	go runHistoryAutosave(ctx, stateTracker, historyPath, time.Minute)

	// ── Shutdown ──────────────────────────────────────────────────────────────
	done := make(chan bool, 1)
	go func() {
		<-ctx.Done()
		log.Println("shutting down gracefully…")
		stop()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownSequence{
			hub:         hub,
			server:      httpServer,
			nowPlaying:  stateTracker,
			bloom:       playedTracker,
			bloomPath:   cfg.BloomPath,
			history:     stateTracker,
			historyPath: historyPath,
		}.run(shutCtx)
		done <- true
	}()

	log.Printf("lofi-radio  port=%d  music=%s  bloom=%s  shuffle=%s  queue=%s",
		cfg.Port, cfg.MusicDir, cfg.BloomPath, cfg.Shuffle.Strategy, cfg.Queue.Strategy)
	log.Printf("swagger UI: http://localhost:%d/swagger", cfg.Port)

	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("http server error: %s", err)
	}
	<-done
	fmt.Println("shutdown complete.")
}

// sidecarPath returns where a file kept next to the Bloom file lives: the
// Bloom file's path with its extension replaced by suffix.
func sidecarPath(bloomPath, suffix string) string {
	return strings.TrimSuffix(bloomPath, filepath.Ext(bloomPath)) + suffix
}

// loadBlocklist loads the blocklist next to the Bloom file. A corrupt file is
// logged and replaced by an empty list on the next change, so it never stops
// the radio.
func loadBlocklist(bloomPath string) *blocklist.Store {
	path := sidecarPath(bloomPath, ".blocked.json")
	store, err := blocklist.Load(path)
	if err != nil {
		log.Printf("warning: blocklist: starting empty (%v)", err)
		return blocklist.Empty(path)
	}
	log.Printf("blocklist loaded from %s (%d songs blocked)", path, len(store.List()))
	return store
}

// loadHistory restores the play history saved at path. Like the blocklist, a
// corrupt file is logged and replaced on the next save, so it never stops the
// radio.
func loadHistory(tracker *player.StateTracker, path string) {
	entries, err := player.LoadHistoryFile(path)
	if err != nil {
		log.Printf("warning: play history: starting empty (%v)", err)
		return
	}
	tracker.LoadHistory(entries)
	log.Printf("play history loaded from %s (%d entries)", path, len(tracker.History(player.MaxHistory)))
}

// historySaver is the part of player.StateTracker that persists history.
type historySaver interface {
	SaveHistoryIfDirty(save func([]player.PlayEntry) error) error
}

// saveHistory writes the play history to path when it changed.
func saveHistory(history historySaver, path string) error {
	return history.SaveHistoryIfDirty(func(entries []player.PlayEntry) error {
		return player.SaveHistory(path, entries)
	})
}

// runHistoryAutosave saves the play history every interval when it changed,
// until ctx ends, so a crash loses at most one interval of history.
func runHistoryAutosave(ctx context.Context, history historySaver, path string, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := saveHistory(history, path); err != nil {
				log.Printf("warning: play history autosave failed: %v", err)
			}
		}
	}
}

// bloomSaver is the part of played.Tracker that shutdown persists.
type bloomSaver interface {
	Save(path string) error
	ApproxCount() uint32
}

// shutdownSequence holds what a graceful shutdown stops, clears and saves.
type shutdownSequence struct {
	hub         interface{ Shutdown(context.Context) error }
	server      *http.Server
	nowPlaying  interface{ ClearCurrentSong() bool }
	bloom       bloomSaver
	bloomPath   string
	history     historySaver
	historyPath string
}

// run stops the hub and the HTTP server, clears now-playing and saves the
// Bloom filter and the play history. Every step runs even when an earlier one fails or ctx ends.
func (s shutdownSequence) run(ctx context.Context) {
	// Hijacked /ws connections are not closed by server.Shutdown, so the hub
	// sends peers 1001 Going Away first.
	if err := s.hub.Shutdown(ctx); err != nil {
		log.Printf("websocket shutdown: %v", err)
	}
	// The tracker's change hook empties the OBS file; otherwise OBS keeps
	// showing the last title after the server is gone.
	s.nowPlaying.ClearCurrentSong()
	// Song streams can stall far past ctx; Close drops what Shutdown left.
	if err := s.server.Shutdown(ctx); err != nil {
		log.Printf("server forced shutdown: %v", err)
		if err := s.server.Close(); err != nil {
			log.Printf("server close: %v", err)
		}
	}
	if err := s.bloom.Save(s.bloomPath); err != nil {
		log.Printf("warning: bloom filter save failed: %v", err)
	} else {
		log.Printf("bloom filter saved to %s (~%d songs played)", s.bloomPath, s.bloom.ApproxCount())
	}
	if err := saveHistory(s.history, s.historyPath); err != nil {
		log.Printf("warning: play history save failed: %v", err)
	}
}

// songSource is the part of player.StateTracker the now-playing file follows.
// Tests substitute it to force the hook orders concurrent song changes allow.
type songSource interface {
	GetCurrentSong() *models.Song
	OnSongChange(fn func(*models.Song))
}

// setupNowPlayingFile clears the OBS now-playing file, so no stale title from
// the last run shows, and rewrites it on every song change. Write errors are
// logged, not fatal: on Windows OBS may briefly hold the file open.
func setupNowPlayingFile(tracker songSource, obs config.OBSConfig) {
	file := player.NewNowPlayingFile(obs.NowPlayingFile, obs.NowPlayingFormat)
	var writeMu sync.Mutex
	// Hooks from concurrent song changes can run out of order, so each write
	// re-reads the tracker under writeMu and the last write is always current.
	write := func(*models.Song) {
		writeMu.Lock()
		defer writeMu.Unlock()
		if err := file.Write(tracker.GetCurrentSong()); err != nil {
			log.Printf("warning: %v", err)
		}
	}
	write(nil)
	tracker.OnSongChange(write)
	log.Printf("obs now-playing file: %s", obs.NowPlayingFile)
}

// broadcaster is the part of websocket.Hub the watcher frames go through.
type broadcaster interface {
	Broadcast(msg websocket.Message)
}

// queueLister is the part of queue.Manager that reports the live queue.
type queueLister interface {
	ListQueue(ctx context.Context) ([]*models.QueueItem, error)
}

// broadcastQueueUpdated tells every client the queue changed for reason and
// how deep it now is. A failed queue read still sends the frame, with depth 0
// and no next_song.
func broadcastQueueUpdated(ctx context.Context, hub broadcaster, q queueLister, reason string) {
	items, err := q.ListQueue(ctx)
	if err != nil {
		log.Printf("list queue for queue_updated (%s): %v", reason, err)
		items = nil
	}
	msg, err := websocket.QueueUpdatedMessage(items, reason)
	if err != nil {
		log.Printf("encode queue_updated (%s): %v", reason, err)
		return
	}
	hub.Broadcast(msg)
}

// onSongUpdated handles a retag: when song is still the current song it
// re-announces it so overlays and the admin console show the new metadata at
// once, with the time elapsed since its original start, then reports the
// queue change. The queue depth is read first, outside
// the tracker's announce lock; the update and its now_playing frame then run
// under that lock, so the frame cannot land after a newer song start.
func onSongUpdated(ctx context.Context, tracker *player.StateTracker, hub broadcaster, q queueLister, song *models.Song) {
	items, err := q.ListQueue(ctx)
	if err != nil {
		log.Printf("list queue for now_playing: %v", err)
	}
	tracker.RetagSong(song, func(updated models.Song, elapsed time.Duration) {
		msg, err := websocket.Encode(websocket.MsgNowPlaying, websocket.NowPlayingPayload{
			Song:        updated,
			QueueDepth:  len(items),
			ElapsedSecs: player.ElapsedSecs(elapsed),
		})
		if err != nil {
			log.Printf("encode now_playing: %v", err)
			return
		}
		hub.Broadcast(msg)
	})
	broadcastQueueUpdated(ctx, hub, q, "file_updated")
}

func logStartupInventory(
	cfg *config.Config,
	scan *watcher.ScanSummary,
	songs []*models.Song,
	queueItems []*models.QueueItem,
	queueTarget, queueAdded int,
) {
	if scan == nil {
		scan = &watcher.ScanSummary{}
	}
	totalDuration, totalSize := summarizeSongs(songs)
	log.Printf(
		"library: dir=%q ext=%s found=%d indexed=%d new=%d skipped=%d dirs=%d duration=%s size=%s",
		cfg.MusicDir, formatExtensions(cfg.Music.Extensions),
		scan.MusicFiles, len(songs), scan.NewlyIndexed, scan.Skipped, scan.WatchedDirs,
		formatDuration(totalDuration), formatBytes(totalSize),
	)
	log.Printf(
		"queue: strategy=%s shuffle=%s target=%d added=%d depth=%d",
		cfg.Queue.Strategy, cfg.Shuffle.Strategy,
		queueTarget, queueAdded, len(queueItems),
	)
}

func summarizeSongs(songs []*models.Song) (float64, int64) {
	var d float64
	var b int64
	for _, s := range songs {
		if s != nil {
			d += s.DurationSecs
			b += s.SizeBytes
		}
	}
	return d, b
}

func formatExtensions(ext []string) string {
	if len(ext) == 0 {
		return ".mp3"
	}
	return strings.Join(ext, ",")
}

func formatDuration(s float64) string {
	if s <= 0 {
		return "unknown"
	}
	d := time.Duration(s * float64(time.Second)).Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	sec := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, m, sec)
	}
	return fmt.Sprintf("%dm%02ds", m, sec)
}

func formatBytes(b int64) string {
	if b <= 0 {
		return "0 B"
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	sz := float64(b)
	u := 0
	for sz >= 1024 && u < len(units)-1 {
		sz /= 1024
		u++
	}
	if u == 0 {
		return fmt.Sprintf("%d B", b)
	}
	return fmt.Sprintf("%.1f %s", sz, units[u])
}
