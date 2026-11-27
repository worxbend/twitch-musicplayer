package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"lofi-radio-backend/internal/config"
	"lofi-radio-backend/internal/models"
	"lofi-radio-backend/internal/played"
	"lofi-radio-backend/internal/player"
	"lofi-radio-backend/internal/websocket"
)

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "unknown"},
		{-1, "unknown"},
		{59.4, "0m59s"},
		{59.6, "1m00s"},
		{61, "1m01s"},
		{3600, "1h00m00s"},
		{3725, "1h02m05s"},
	}
	for _, c := range cases {
		if got := formatDuration(c.in); got != c.want {
			t.Errorf("formatDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{-5, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536 * 1024, "1.5 MiB"},
		{5 << 50, "5120.0 TiB"}, // units stop at TiB
	}
	for _, c := range cases {
		if got := formatBytes(c.in); got != c.want {
			t.Errorf("formatBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatExtensions(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ".mp3"},
		{[]string{}, ".mp3"},
		{[]string{".mp3", ".flac"}, ".mp3,.flac"},
	}
	for _, c := range cases {
		if got := formatExtensions(c.in); got != c.want {
			t.Errorf("formatExtensions(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSummarizeSongs(t *testing.T) {
	songs := []*models.Song{
		{DurationSecs: 60.5, SizeBytes: 1000},
		nil,
		{DurationSecs: 120, SizeBytes: 2500},
	}
	if d, b := summarizeSongs(songs); d != 180.5 || b != 3500 {
		t.Errorf("summarizeSongs = (%v, %d), want (180.5, 3500)", d, b)
	}
	if d, b := summarizeSongs(nil); d != 0 || b != 0 {
		t.Errorf("summarizeSongs(nil) = (%v, %d), want (0, 0)", d, b)
	}
}

const testNowPlayingFormat = "{artist} - {title}"

// newNowPlayingSetup returns a tracker wired to a now-playing file that
// already holds a stale title, and the file's path.
func newNowPlayingSetup(t *testing.T) (*player.StateTracker, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "np.txt")
	if err := os.WriteFile(path, []byte("old title"), 0o644); err != nil {
		t.Fatal(err)
	}
	tracker := player.NewStateTracker()
	setupNowPlayingFile(tracker, config.OBSConfig{NowPlayingFile: path, NowPlayingFormat: testNowPlayingFormat})
	return tracker, path
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("file content = %q, want %q", got, want)
	}
}

func TestSetupNowPlayingFileClearsStaleTitle(t *testing.T) {
	_, path := newNowPlayingSetup(t)
	assertFileContent(t, path, "")
}

// The tracker runs its change hook synchronously, so each write has landed
// by the time the tracker method returns.
func TestSetupNowPlayingFileFollowsTracker(t *testing.T) {
	tracker, path := newNowPlayingSetup(t)

	tracker.SetCurrentSong(&models.Song{ID: "s1", Artist: "A", Title: "T"})
	assertFileContent(t, path, "A - T")

	if !tracker.UpdateSong(&models.Song{ID: "s1", Artist: "A", Title: "T2"}) {
		t.Fatal("UpdateSong did not apply the retag")
	}
	assertFileContent(t, path, "A - T2")

	if !tracker.ClearCurrentSong() {
		t.Fatal("ClearCurrentSong reported nothing to clear")
	}
	assertFileContent(t, path, "")
}

// scriptedSongSource stands in for the StateTracker so a test can replay the
// change-hook orders that concurrent song changes allow. When hold is set,
// the next GetCurrentSong reads the song, signals held, then waits on hold.
type scriptedSongSource struct {
	mu      sync.Mutex
	current *models.Song
	hook    func(*models.Song)
	hold    chan struct{}
	held    chan struct{}
}

func (s *scriptedSongSource) GetCurrentSong() *models.Song {
	s.mu.Lock()
	song, hold, held := s.current, s.hold, s.held
	s.hold = nil
	s.mu.Unlock()
	if hold != nil {
		close(held)
		<-hold
	}
	return song
}

func (s *scriptedSongSource) OnSongChange(fn func(*models.Song)) { s.hook = fn }

func (s *scriptedSongSource) setCurrent(song *models.Song) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = song
}

func newScriptedNowPlaying(t *testing.T) (*scriptedSongSource, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "np.txt")
	src := &scriptedSongSource{}
	setupNowPlayingFile(src, config.OBSConfig{NowPlayingFile: path, NowPlayingFormat: testNowPlayingFormat})
	return src, path
}

var (
	songA = &models.Song{ID: "a", Artist: "A", Title: "Old"}
	songB = &models.Song{ID: "b", Artist: "B", Title: "New"}
)

// StateTracker runs hooks after releasing its lock, so the hook for an older
// change can run last. The file must still show the tracker's current song,
// not the older hook argument.
func TestNowPlayingFileIgnoresLateHookArgument(t *testing.T) {
	src, path := newScriptedNowPlaying(t)

	src.setCurrent(songB)
	src.hook(songB)
	src.hook(songA)

	assertFileContent(t, path, "B - New")
}

// Two hooks running at once must not interleave read and write: one that read
// the older song must not overwrite a newer write that finished meanwhile.
func TestNowPlayingFileSerializesConcurrentWrites(t *testing.T) {
	src, path := newScriptedNowPlaying(t)
	src.setCurrent(songA)
	src.hold, src.held = make(chan struct{}), make(chan struct{})
	hold := src.hold

	first := make(chan struct{})
	go func() { defer close(first); src.hook(songA) }()
	select {
	case <-src.held:
	case <-time.After(5 * time.Second):
		t.Fatal("write hook never read the current song")
	}

	src.setCurrent(songB)
	second := make(chan struct{})
	go func() { defer close(second); src.hook(songB) }()
	// With writes serialized the second hook cannot finish while the first
	// is held, so the wait ends on the timeout instead.
	select {
	case <-second:
	case <-time.After(100 * time.Millisecond):
	}
	close(hold)
	<-first
	<-second

	assertFileContent(t, path, "B - New")
}

// blockingHub is a hub whose Shutdown never finishes before ctx ends.
type blockingHub struct{}

func (blockingHub) Shutdown(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func runShutdown(t *testing.T, ctx context.Context, hub interface{ Shutdown(context.Context) error }, srv *http.Server, tracker *player.StateTracker) string {
	t.Helper()
	bloomPath := filepath.Join(t.TempDir(), "played.bloom")
	shutdownSequence{
		hub:        hub,
		server:     srv,
		nowPlaying: tracker,
		bloom:      played.New(100, 0.01),
		bloomPath:  bloomPath,
	}.run(ctx)
	return bloomPath
}

func assertShutdownCleared(t *testing.T, tracker *player.StateTracker, npPath, bloomPath string) {
	t.Helper()
	assertFileContent(t, npPath, "")
	if song := tracker.GetCurrentSong(); song != nil {
		t.Errorf("current song after shutdown = %+v, want nil", song)
	}
	if _, err := os.Stat(bloomPath); err != nil {
		t.Errorf("bloom filter not saved: %v", err)
	}
}

func TestShutdownClearsNowPlayingFile(t *testing.T) {
	tracker, npPath := newNowPlayingSetup(t)
	tracker.SetCurrentSong(&models.Song{ID: "a", Artist: "Artist", Title: "Title"})
	assertFileContent(t, npPath, "Artist - Title")

	bloomPath := runShutdown(t, context.Background(), websocket.NewHub(), &http.Server{}, tracker)
	assertShutdownCleared(t, tracker, npPath, bloomPath)
}

func TestShutdownStillClearsWhenHubTimesOut(t *testing.T) {
	tracker, npPath := newNowPlayingSetup(t)
	tracker.SetCurrentSong(&models.Song{ID: "a", Artist: "Artist", Title: "Title"})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	bloomPath := runShutdown(t, ctx, blockingHub{}, &http.Server{}, tracker)
	assertShutdownCleared(t, tracker, npPath, bloomPath)
}

// A song stream stalled past the shutdown budget must not keep its
// connection open: Close drops it and cancels the request context.
func TestShutdownClosesStalledRequests(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(cancelled)
	})}
	go func() { _ = srv.Serve(ln) }()
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String())
		if err == nil {
			resp.Body.Close()
		}
	}()
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	runShutdown(t, ctx, websocket.NewHub(), srv, player.NewStateTracker())

	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("stalled request still open after shutdown")
	}
}

type fakeQueueLister struct {
	items []*models.QueueItem
	err   error
}

func (f fakeQueueLister) ListQueue(context.Context) ([]*models.QueueItem, error) {
	return f.items, f.err
}

type capturingBroadcaster struct{ msgs []websocket.Message }

func (c *capturingBroadcaster) Broadcast(msg websocket.Message) { c.msgs = append(c.msgs, msg) }

func assertQueueUpdated(t *testing.T, hub *capturingBroadcaster, wantDepth int, wantReason string) {
	t.Helper()
	if len(hub.msgs) != 1 {
		t.Fatalf("broadcast %d frames, want 1", len(hub.msgs))
	}
	msg := hub.msgs[0]
	if msg.Type != websocket.MsgQueueUpdated {
		t.Fatalf("frame type = %q, want %q", msg.Type, websocket.MsgQueueUpdated)
	}
	var got websocket.QueueUpdatedPayload
	if err := websocket.DecodePayload(msg, &got); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	want := websocket.QueueUpdatedPayload{QueueDepth: wantDepth, Reason: wantReason}
	if got != want {
		t.Errorf("payload = %+v, want %+v", got, want)
	}
}

func TestBroadcastQueueUpdatedReportsLiveDepth(t *testing.T) {
	hub := &capturingBroadcaster{}
	q := fakeQueueLister{items: []*models.QueueItem{{}, {}, {}}}

	broadcastQueueUpdated(context.Background(), hub, q, "new_file")

	assertQueueUpdated(t, hub, 3, "new_file")
}

func TestBroadcastQueueUpdatedSendsOnListError(t *testing.T) {
	hub := &capturingBroadcaster{}
	q := fakeQueueLister{err: errors.New("store down")}

	broadcastQueueUpdated(context.Background(), hub, q, "file_removed")

	assertQueueUpdated(t, hub, 0, "file_removed")
}

func TestBlocklistPath(t *testing.T) {
	tests := map[string]string{
		"./lofi-radio.bloom":     "./lofi-radio.blocked.json",
		"/data/lofi-radio.bloom": "/data/lofi-radio.blocked.json",
		"/data/history":          "/data/history.blocked.json",
	}
	for bloom, want := range tests {
		if got := blocklistPath(bloom); got != want {
			t.Errorf("blocklistPath(%q) = %q, want %q", bloom, got, want)
		}
	}
}

// TestLoadBlocklistReplacesCorruptFile: a corrupt file gives an empty list
// that overwrites it on the next change.
func TestLoadBlocklistReplacesCorruptFile(t *testing.T) {
	bloom := filepath.Join(t.TempDir(), "radio.bloom")
	path := blocklistPath(bloom)
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := loadBlocklist(bloom)
	if got := store.List(); len(got) != 0 {
		t.Fatalf("List = %v, want empty", got)
	}
	if err := store.Add("s1"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := loadBlocklist(bloom).List(); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("reloaded List = %v, want [s1]", got)
	}
}
