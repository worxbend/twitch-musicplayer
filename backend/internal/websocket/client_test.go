package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	ws "github.com/gorilla/websocket"

	"lofi-radio-backend/internal/models"
	"lofi-radio-backend/internal/player"
	"lofi-radio-backend/internal/queue"
)

type emptyQueueManager struct{}

func (emptyQueueManager) NextSong(context.Context) (*models.Song, error) {
	return nil, queue.ErrQueueEmpty
}

func (emptyQueueManager) ListQueue(context.Context) ([]*models.QueueItem, error) { return nil, nil }

func (emptyQueueManager) TotalSongs(context.Context) (int, error) { return 0, nil }

func (emptyQueueManager) Requeue(context.Context, string) error { return nil }

// listedQueueManager serves a fixed queue snapshot to ListQueue.
type listedQueueManager struct {
	emptyQueueManager
	items []*models.QueueItem
}

func (m listedQueueManager) ListQueue(context.Context) ([]*models.QueueItem, error) {
	return m.items, nil
}

// dialTestServer starts an Endpoint behind httptest and returns a connected peer
// whose initial state frame has already been consumed.
func dialTestServer(t *testing.T, queueMgr QueueManager) *ws.Conn {
	t.Helper()
	_, conn := dialTestHub(t, queueMgr)
	return conn
}

// dialTestHub is dialTestServer that also returns the server's hub.
func dialTestHub(t *testing.T, queueMgr QueueManager) (*Hub, *ws.Conn) {
	t.Helper()
	hub := NewHub()
	go hub.Run()
	srv := httptest.NewServer(&Endpoint{Hub: hub, QueueMgr: queueMgr, StateTracker: player.NewStateTracker()})
	t.Cleanup(srv.Close)
	return hub, dialWS(t, "ws"+strings.TrimPrefix(srv.URL, "http"))
}

// dialWS connects to url and consumes the initial state frame. By then the
// server has registered the peer.
func dialWS(t *testing.T, url string) *ws.Conn {
	t.Helper()
	conn, _, err := ws.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetReadDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck

	var first Message
	if err := conn.ReadJSON(&first); err != nil || first.Type != MsgState {
		t.Fatalf("first frame = %+v, %v; want %s", first, err, MsgState)
	}
	return conn
}

func TestReadPumpAnswersSmallMessages(t *testing.T) {
	conn := dialTestServer(t, emptyQueueManager{})
	if err := conn.WriteJSON(Message{Type: MsgHeartbeat}); err != nil {
		t.Fatalf("write: %v", err)
	}
	var reply Message
	if err := conn.ReadJSON(&reply); err != nil || reply.Type != MsgHeartbeatAck {
		t.Fatalf("reply = %+v, %v; want %s", reply, err, MsgHeartbeatAck)
	}
}

func TestReadPumpClosesOnOversizeFrame(t *testing.T) {
	conn := dialTestServer(t, emptyQueueManager{})
	if err := conn.WriteMessage(ws.TextMessage, make([]byte, maxMessageSize+904)); err != nil {
		t.Fatalf("write: %v", err)
	}
	for {
		_, _, err := conn.ReadMessage()
		if err == nil {
			continue
		}
		if !ws.IsCloseError(err, ws.CloseMessageTooBig) {
			t.Fatalf("read error = %v, want close %d", err, ws.CloseMessageTooBig)
		}
		return
	}
}

func TestPeekNextSkipsDeadHead(t *testing.T) {
	dead := &models.QueueItem{ID: "q1", SongID: "gone"}
	live := &models.QueueItem{ID: "q2", SongID: "b", Song: &models.Song{ID: "b"}}
	conn := dialTestServer(t, listedQueueManager{items: []*models.QueueItem{dead, live}})
	if err := conn.WriteJSON(Message{Type: MsgPeekNext}); err != nil {
		t.Fatalf("write: %v", err)
	}
	var reply Message
	if err := conn.ReadJSON(&reply); err != nil || reply.Type != MsgPrebufferSong {
		t.Fatalf("reply = %+v, %v; want %s", reply, err, MsgPrebufferSong)
	}
	var payload PlaySongPayload
	if err := json.Unmarshal(reply.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Song.ID != "b" {
		t.Fatalf("prebuffer song = %q, want b", payload.Song.ID)
	}
}

func TestPeekNextAllDeadSendsNothing(t *testing.T) {
	dead := &models.QueueItem{ID: "q1", SongID: "gone"}
	conn := dialTestServer(t, listedQueueManager{items: []*models.QueueItem{dead}})
	if err := conn.WriteJSON(Message{Type: MsgPeekNext}); err != nil {
		t.Fatalf("write: %v", err)
	}
	// A heartbeat sent after peek_next is answered in order, so an ack as the
	// first reply proves no prebuffer_song was queued ahead of it.
	if err := conn.WriteJSON(Message{Type: MsgHeartbeat}); err != nil {
		t.Fatalf("write: %v", err)
	}
	var reply Message
	if err := conn.ReadJSON(&reply); err != nil || reply.Type != MsgHeartbeatAck {
		t.Fatalf("reply = %+v, %v; want %s", reply, err, MsgHeartbeatAck)
	}
}

// recordingQueueManager hands out one fixed song and records NextSong calls
// and requeued song IDs.
type recordingQueueManager struct {
	emptyQueueManager
	song  *models.Song
	items []*models.QueueItem // what ListQueue returns after the dequeue

	mu        sync.Mutex
	nextCalls int
	requeued  []string
}

func (m *recordingQueueManager) NextSong(context.Context) (*models.Song, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextCalls++
	return m.song, nil
}

func (m *recordingQueueManager) ListQueue(context.Context) ([]*models.QueueItem, error) {
	return m.items, nil
}

func (m *recordingQueueManager) Requeue(_ context.Context, songID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requeued = append(m.requeued, songID)
	return nil
}

func (m *recordingQueueManager) calls() (int, []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nextCalls, append([]string(nil), m.requeued...)
}

// newSendTestClient builds a client with no connection; sendNextSong never
// touches conn. The hub is not run, so broadcasts stay in hub.broadcast.
func newSendTestClient() (*Client, *recordingQueueManager) {
	mgr := &recordingQueueManager{song: &models.Song{ID: "song-1", Title: "Rain"}}
	return NewClient(NewHub(), nil, mgr, "", player.NewStateTracker()), mgr
}

func assertNothingPublished(t *testing.T, c *Client) {
	t.Helper()
	if cur := c.stateTracker.GetCurrentSong(); cur != nil {
		t.Fatalf("current song = %s, want nil", cur.ID)
	}
	if n := len(c.hub.broadcast); n != 0 {
		t.Fatalf("hub.broadcast holds %d messages, want 0", n)
	}
}

func TestSendNextSongClosedClientDequeuesNothing(t *testing.T) {
	c, mgr := newSendTestClient()
	c.close()

	c.sendNextSong(context.Background())

	if next, requeued := mgr.calls(); next != 0 || len(requeued) != 0 {
		t.Fatalf("NextSong calls = %d, requeued = %v; want 0 and none", next, requeued)
	}
	assertNothingPublished(t, c)
}

func TestSendNextSongFullBufferRequeues(t *testing.T) {
	c, mgr := newSendTestClient()
	for range sendBufSize {
		c.send <- Message{Type: MsgHeartbeatAck}
	}

	c.sendNextSong(context.Background())

	if _, requeued := mgr.calls(); len(requeued) != 1 || requeued[0] != mgr.song.ID {
		t.Fatalf("requeued = %v, want [%s]", requeued, mgr.song.ID)
	}
	assertNothingPublished(t, c)
}

func TestSendNextSongDeliversAndPublishes(t *testing.T) {
	c, mgr := newSendTestClient()

	c.sendNextSong(context.Background())

	if len(c.send) != 1 {
		t.Fatalf("send buffer holds %d messages, want 1", len(c.send))
	}
	sent := <-c.send
	var payload PlaySongPayload
	if err := json.Unmarshal(sent.Payload, &payload); err != nil || sent.Type != MsgPlaySong {
		t.Fatalf("sent = %s (%v), want %s", sent.Type, err, MsgPlaySong)
	}
	if payload.Song.ID != mgr.song.ID {
		t.Fatalf("play_song song = %q, want %q", payload.Song.ID, mgr.song.ID)
	}
	if cur := c.stateTracker.GetCurrentSong(); cur == nil || cur.ID != mgr.song.ID {
		t.Fatalf("current song = %v, want %s", cur, mgr.song.ID)
	}
	if _, requeued := mgr.calls(); len(requeued) != 0 {
		t.Fatalf("requeued = %v, want none", requeued)
	}
	for _, want := range []string{MsgNowPlaying, MsgQueueUpdated} {
		select {
		case got := <-c.hub.broadcast:
			if got.Type != want {
				t.Fatalf("broadcast = %s, want %s", got.Type, want)
			}
			if want == MsgNowPlaying {
				assertNowPlayingHistoryID(t, got, payload.HistoryID)
			}
		default:
			t.Fatalf("broadcast missing %s", want)
		}
	}
}

// The song_started queue_updated frame names the next playable song, skipping
// a dead head, so overlays can show "Up next"; an empty queue omits the key.
func TestSendNextSongQueueUpdatedCarriesNextSong(t *testing.T) {
	dead := &models.QueueItem{ID: "q1", SongID: "gone"}
	live := &models.QueueItem{ID: "q2", SongID: "b", Song: &models.Song{ID: "b", Title: "Snow"}}
	tests := []struct {
		name      string
		items     []*models.QueueItem
		wantDepth float64
		wantNext  string // "" means next_song is absent
	}{
		{name: "dead head skipped", items: []*models.QueueItem{dead, live}, wantDepth: 2, wantNext: "b"},
		{name: "empty queue omits key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, mgr := newSendTestClient()
			mgr.items = tt.items

			c.sendNextSong(context.Background())

			<-c.hub.broadcast // now_playing
			got := decodeObject(t, <-c.hub.broadcast)
			if got["reason"] != "song_started" || got["queue_depth"] != tt.wantDepth {
				t.Fatalf("queue_updated = %v, want song_started depth %v", got, tt.wantDepth)
			}
			assertNextSong(t, got, tt.wantNext)
		})
	}
}

// The connect state frame names the next playable song.
func TestConnectStateCarriesNextSong(t *testing.T) {
	live := &models.QueueItem{ID: "q1", SongID: "b", Song: &models.Song{ID: "b", Title: "Snow"}}
	hub := NewHub()
	go hub.Run()
	srv := httptest.NewServer(&Endpoint{Hub: hub, QueueMgr: listedQueueManager{items: []*models.QueueItem{live}}, StateTracker: player.NewStateTracker()})
	t.Cleanup(srv.Close)

	conn, _, err := ws.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetReadDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck

	var first Message
	if err := conn.ReadJSON(&first); err != nil || first.Type != MsgState {
		t.Fatalf("first frame = %+v, %v; want %s", first, err, MsgState)
	}
	assertNextSong(t, decodeObject(t, first), "b")
}

// decodeObject decodes msg's payload as a JSON object, so tests can tell an
// absent key from a zero value.
func decodeObject(t *testing.T, msg Message) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(msg.Payload, &got); err != nil {
		t.Fatalf("decode %s: %v", msg.Type, err)
	}
	return got
}

// assertNextSong checks payload's next_song id, or that the key is absent
// when wantID is empty.
func assertNextSong(t *testing.T, payload map[string]any, wantID string) {
	t.Helper()
	next, ok := payload["next_song"]
	if wantID == "" {
		if ok {
			t.Fatalf("next_song = %v, want key absent", next)
		}
		return
	}
	song, _ := next.(map[string]any)
	if song == nil || song["id"] != wantID {
		t.Fatalf("next_song = %v, want id %q", next, wantID)
	}
}

// assertNowPlayingHistoryID checks that a now_playing broadcast carries the
// play_song history_id, so overlays can tell a new start from a retag.
func assertNowPlayingHistoryID(t *testing.T, msg Message, want string) {
	t.Helper()
	var now NowPlayingPayload
	if err := json.Unmarshal(msg.Payload, &now); err != nil {
		t.Fatalf("decode now_playing: %v", err)
	}
	if want == "" || now.HistoryID != want {
		t.Fatalf("now_playing history_id = %q, want play_song's %q (non-empty)", now.HistoryID, want)
	}
}

// failingQueueManager fails NextSong with an error other than ErrQueueEmpty.
type failingQueueManager struct{ emptyQueueManager }

func (failingQueueManager) NextSong(context.Context) (*models.Song, error) {
	return nil, errors.New("disk on fire")
}

// emptyLibraryQueueManager fails NextSong the way an auto strategy does when
// the library is empty.
type emptyLibraryQueueManager struct{ emptyQueueManager }

func (emptyLibraryQueueManager) NextSong(context.Context) (*models.Song, error) {
	return nil, fmt.Errorf("auto-select: %w", queue.ErrLibraryEmpty)
}

// testDryClearGrace replaces the hub's 4s dry-queue clear grace in tests.
const testDryClearGrace = 50 * time.Millisecond

// newDryTestClient builds an unconnected client, like newSendTestClient, whose
// tracker is already playing song-1 and whose queue manager is mgr. Its hub
// clears now-playing testDryClearGrace after a song_finished on a dry queue.
func newDryTestClient(mgr QueueManager) *Client {
	st := player.NewStateTracker()
	st.SetCurrentSong(&models.Song{ID: "song-1", Title: "Rain"})
	h := NewHub()
	h.dryClearGrace = testDryClearGrace
	return NewClient(h, nil, mgr, "", st)
}

// hasPendingDryClear reports whether h has a dry-queue clear scheduled.
func hasPendingDryClear(h *Hub) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.pendingDryClear != nil
}

// finishOnDryQueue sends song_finished on c's dry queue and checks that the
// finished song stays on air for now, with a clear pending.
func finishOnDryQueue(t *testing.T, c *Client) {
	t.Helper()
	c.handleMessage(Message{Type: MsgSongFinished})
	drainSent(t, c, MsgError)
	assertSongKept(t, c)
	if !hasPendingDryClear(c.hub) {
		t.Fatal("song_finished on a dry queue scheduled no clear")
	}
}

// assertNoDryClearPastGrace waits past the dry grace, then checks no clear
// is pending and nothing was broadcast.
func assertNoDryClearPastGrace(t *testing.T, c *Client) {
	t.Helper()
	time.Sleep(2 * c.hub.dryClearGrace)
	if hasPendingDryClear(c.hub) {
		t.Fatal("dry-queue clear still pending")
	}
	if n := len(c.hub.broadcast); n != 0 {
		t.Fatalf("hub.broadcast holds %d messages, want 0", n)
	}
}

// drainSent asserts c.send holds exactly one message of type want and removes it.
func drainSent(t *testing.T, c *Client, want string) {
	t.Helper()
	if n := len(c.send); n != 1 {
		t.Fatalf("send buffer holds %d messages, want 1", n)
	}
	if got := <-c.send; got.Type != want {
		t.Fatalf("sent = %s, want %s", got.Type, want)
	}
}

func assertSongKept(t *testing.T, c *Client) {
	t.Helper()
	if cur := c.stateTracker.GetCurrentSong(); cur == nil || cur.ID != "song-1" {
		t.Fatalf("current song = %v, want song-1", cur)
	}
	if n := len(c.hub.broadcast); n != 0 {
		t.Fatalf("hub.broadcast holds %d messages, want 0", n)
	}
}

// assertNowPlayingCleared asserts the tracker has no current song and exactly
// one state broadcast without current_song went out.
func assertNowPlayingCleared(t *testing.T, c *Client) {
	t.Helper()
	if cur := c.stateTracker.GetCurrentSong(); cur != nil {
		t.Fatalf("current song = %s, want nil", cur.ID)
	}
	if n := len(c.hub.broadcast); n != 1 {
		t.Fatalf("hub.broadcast holds %d messages, want 1", n)
	}
	got := <-c.hub.broadcast
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(got.Payload, &fields); err != nil || got.Type != MsgState {
		t.Fatalf("broadcast = %s (%v), want %s", got.Type, err, MsgState)
	}
	if _, ok := fields["current_song"]; ok {
		t.Fatalf("state payload %s has current_song, want none", got.Payload)
	}
}

func TestSongFinishedOnEmptyQueueClearsNowPlaying(t *testing.T) {
	c := newDryTestClient(emptyQueueManager{})

	finishOnDryQueue(t, c)
	waitNowPlayingCleared(t, c)

	// A second song_finished on the still-empty queue errors again but
	// schedules and broadcasts nothing: nothing is playing any more.
	c.handleMessage(Message{Type: MsgSongFinished})

	drainSent(t, c, MsgError)
	if hasPendingDryClear(c.hub) {
		t.Fatal("second finish scheduled a clear, want none")
	}
	if n := len(c.hub.broadcast); n != 0 {
		t.Fatalf("hub.broadcast holds %d messages after second finish, want 0", n)
	}
}

// refillableQueueManager is dry until refill, then yields one fixed song.
type refillableQueueManager struct {
	emptyQueueManager
	mu   sync.Mutex
	song *models.Song
}

func (m *refillableQueueManager) NextSong(context.Context) (*models.Song, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.song == nil {
		return nil, queue.ErrQueueEmpty
	}
	return m.song, nil
}

func (m *refillableQueueManager) refill(song *models.Song) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.song = song
}

func TestNeedSongInsideDryGraceCancelsClear(t *testing.T) {
	mgr := &refillableQueueManager{}
	c := newDryTestClient(mgr)
	finishOnDryQueue(t, c)

	mgr.refill(&models.Song{ID: "song-2", Title: "Snow"})
	c.handleMessage(Message{Type: MsgNeedSong})

	drainSent(t, c, MsgPlaySong)
	if hasPendingDryClear(c.hub) {
		t.Fatal("a new song started, want the dry clear cancelled")
	}
	for len(c.hub.broadcast) > 0 {
		<-c.hub.broadcast // now_playing and queue_updated of song-2
	}
	assertNoDryClearPastGrace(t, c)
	if cur := c.stateTracker.GetCurrentSong(); cur == nil || cur.ID != "song-2" {
		t.Fatalf("current song = %v, want song-2", cur)
	}
}

func TestDryClearAfterNewerStartKeepsSong(t *testing.T) {
	c := newDryTestClient(emptyQueueManager{})
	finishOnDryQueue(t, c)

	// A start that bypasses sendNextSong leaves the timer armed; the start
	// number must still stop it from clearing the new song.
	c.stateTracker.StartSong(&models.Song{ID: "song-2", Title: "Snow"}, nil)

	deadline := time.Now().Add(5 * time.Second)
	for hasPendingDryClear(c.hub) {
		if time.Now().After(deadline) {
			t.Fatal("dry clear did not fire within 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
	assertNoDryClearPastGrace(t, c)
	if cur := c.stateTracker.GetCurrentSong(); cur == nil || cur.ID != "song-2" {
		t.Fatalf("current song = %v, want song-2", cur)
	}
}

func TestShutdownCancelsPendingDryClear(t *testing.T) {
	c := newDryTestClient(emptyQueueManager{})
	finishOnDryQueue(t, c)

	if err := c.hub.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown = %v, want nil", err)
	}

	if hasPendingDryClear(c.hub) {
		t.Fatal("dry clear still pending after Shutdown")
	}
	assertNoDryClearPastGrace(t, c)
	assertSongKept(t, c)
}

func TestNeedSongOnEmptyQueueKeepsNowPlaying(t *testing.T) {
	c := newDryTestClient(emptyQueueManager{})

	c.handleMessage(Message{Type: MsgNeedSong})

	drainSent(t, c, MsgError)
	assertSongKept(t, c)
}

func TestSongFinishedOnOtherErrorKeepsNowPlaying(t *testing.T) {
	c := newDryTestClient(failingQueueManager{})

	c.handleMessage(Message{Type: MsgSongFinished})

	drainSent(t, c, MsgError)
	assertSongKept(t, c)
}

func TestSongFinishedOnEmptyLibraryClearsNowPlaying(t *testing.T) {
	c := newDryTestClient(emptyLibraryQueueManager{})

	finishOnDryQueue(t, c)
	waitNowPlayingCleared(t, c)
}

func TestNeedSongOnEmptyLibraryKeepsNowPlaying(t *testing.T) {
	c := newDryTestClient(emptyLibraryQueueManager{})

	c.handleMessage(Message{Type: MsgNeedSong})

	drainSent(t, c, MsgError)
	assertSongKept(t, c)
}

// erroringQueueManager fails NextSong with err.
type erroringQueueManager struct {
	emptyQueueManager
	err error
}

func (m erroringQueueManager) NextSong(context.Context) (*models.Song, error) {
	return nil, m.err
}

func TestNeedSongErrorFrameCarriesCodeWithoutInternalText(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode string
	}{
		{"queue empty", queue.ErrQueueEmpty, ErrCodeQueueEmpty},
		{"wrapped queue empty", fmt.Errorf("x: %w", queue.ErrQueueEmpty), ErrCodeQueueEmpty},
		{"library empty", queue.ErrLibraryEmpty, ErrCodeLibraryEmpty},
		{"wrapped library empty", fmt.Errorf("auto-select: %w", queue.ErrLibraryEmpty), ErrCodeLibraryEmpty},
		{"internal", fmt.Errorf("list songs: %w", errors.New("open /secret/path: disk failed")), ErrCodeInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newDryTestClient(erroringQueueManager{err: tc.err})

			c.handleMessage(Message{Type: MsgNeedSong})

			if n := len(c.send); n != 1 {
				t.Fatalf("send buffer holds %d messages, want 1", n)
			}
			frame := <-c.send
			raw, err := json.Marshal(frame)
			if err != nil {
				t.Fatalf("marshal frame: %v", err)
			}
			var got ErrorPayload
			if frame.Type != MsgError || DecodePayload(frame, &got) != nil {
				t.Fatalf("frame = %s, want %s with ErrorPayload", raw, MsgError)
			}
			if got.Code != tc.wantCode || got.Message == "" {
				t.Fatalf("payload = %+v, want code %q and a message", got, tc.wantCode)
			}
			for _, leak := range []string{"secret", "disk", "list songs", "auto-select", "x:"} {
				if strings.Contains(string(raw), leak) {
					t.Fatalf("frame %s leaks %q", raw, leak)
				}
			}
		})
	}
}

// racingQueueManager starts song b and broadcasts now_playing the first time
// ListQueue runs, which is while Endpoint builds the connect snapshot. It then
// waits, giving the hub time to deliver that broadcast if it is allowed to.
type racingQueueManager struct {
	emptyQueueManager
	hub     *Hub
	tracker *player.StateTracker
	once    sync.Once
}

func (m *racingQueueManager) ListQueue(context.Context) ([]*models.QueueItem, error) {
	m.once.Do(func() {
		song := &models.Song{ID: "b"}
		m.tracker.SetCurrentSong(song)
		if msg, err := Encode(MsgNowPlaying, NowPlayingPayload{Song: *song}); err == nil {
			m.hub.Broadcast(msg)
		}
		time.Sleep(50 * time.Millisecond)
	})
	return nil, nil
}

// A song change racing a new peer's connect must not reach the peer ahead of
// the connect snapshot: state(a) then now_playing(b) leaves b on screen, while
// the reverse order leaves the stale a showing for the whole of b.
func TestConnectSnapshotPrecedesRacingNowPlaying(t *testing.T) {
	hub := NewHub()
	go hub.Run()
	tracker := player.NewStateTracker()
	tracker.SetCurrentSong(&models.Song{ID: "a"})
	mgr := &racingQueueManager{hub: hub, tracker: tracker}
	srv := httptest.NewServer(&Endpoint{Hub: hub, QueueMgr: mgr, StateTracker: tracker})
	t.Cleanup(srv.Close)

	conn, _, err := ws.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetReadDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck

	for _, want := range []struct{ msgType, songID string }{{MsgState, "a"}, {MsgNowPlaying, "b"}} {
		var msg Message
		if err := conn.ReadJSON(&msg); err != nil {
			t.Fatalf("read: %v", err)
		}
		var payload struct {
			CurrentSong *models.Song `json:"current_song"`
			Song        *models.Song `json:"song"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			t.Fatalf("decode %s: %v", msg.Type, err)
		}
		song := payload.Song
		if msg.Type == MsgState {
			song = payload.CurrentSong
		}
		if msg.Type != want.msgType || song == nil || song.ID != want.songID {
			t.Fatalf("frame = %s song %v, want %s song %s", msg.Type, song, want.msgType, want.songID)
		}
	}
}

// newServerConn returns the server side of a live WebSocket connection whose
// peer is kept open until the test ends.
func newServerConn(t *testing.T) *ws.Conn {
	t.Helper()
	conns := make(chan *ws.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&ws.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		conns <- conn
	}))
	t.Cleanup(srv.Close)

	peer, _, err := ws.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { peer.Close() })

	select {
	case conn := <-conns:
		t.Cleanup(func() { conn.Close() })
		return conn
	case <-time.After(5 * time.Second):
		t.Fatal("server never upgraded the connection")
		return nil
	}
}

// newPumpTestClient is newSendTestClient on a live server-side connection.
func newPumpTestClient(t *testing.T) (*Client, *recordingQueueManager) {
	t.Helper()
	c, mgr := newSendTestClient()
	c.conn = newServerConn(t)
	return c, mgr
}

func mustPlaySong(t *testing.T, songID string) Message {
	t.Helper()
	msg, err := Encode(MsgPlaySong, PlaySongPayload{Song: models.Song{ID: songID}})
	if err != nil {
		t.Fatalf("encode play_song: %v", err)
	}
	return msg
}

// runWritePump runs c.writePump and fails the test if it does not return.
func runWritePump(t *testing.T, c *Client) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		c.writePump()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("writePump did not return")
	}
}

// A failed write must close the client, so no later need_song dequeues a song
// for a dead peer, and must put back every play_song the peer never got: the
// one that failed and any still buffered, the failed one at the front.
func TestWritePumpWriteErrorClosesAndRequeues(t *testing.T) {
	c, mgr := newPumpTestClient(t)
	c.send <- mustPlaySong(t, "failed")
	c.send <- Message{Type: MsgHeartbeatAck}
	c.send <- mustPlaySong(t, "buffered")
	if err := c.conn.UnderlyingConn().Close(); err != nil {
		t.Fatalf("close underlying conn: %v", err)
	}

	runWritePump(t, c)

	select {
	case <-c.closed:
	default:
		t.Fatal("client still open after write error")
	}
	if _, requeued := mgr.calls(); strings.Join(requeued, ",") != "buffered,failed" {
		t.Fatalf("requeued = %v, want [buffered failed]", requeued)
	}

	c.sendNextSong(context.Background())

	if next, _ := mgr.calls(); next != 0 {
		t.Fatalf("NextSong calls after write error = %d, want 0", next)
	}
	assertNothingPublished(t, c)
}

// A client the hub dropped or that closed normally must hand its buffered
// play_song back to the queue instead of losing it.
func TestWritePumpClosedClientRequeuesBufferedPlaySong(t *testing.T) {
	c, mgr := newPumpTestClient(t)
	c.send <- mustPlaySong(t, "buffered")
	c.send <- Message{Type: MsgHeartbeatAck}
	c.close()

	runWritePump(t, c)

	if _, requeued := mgr.calls(); len(requeued) != 1 || requeued[0] != "buffered" {
		t.Fatalf("requeued = %v, want [buffered]", requeued)
	}
	if n := len(c.send); n != 0 {
		t.Fatalf("send buffer holds %d messages after exit, want 0", n)
	}
}

// testClearGrace replaces the hub's 5s last-player clear grace in tests.
const testClearGrace = 50 * time.Millisecond

// newPlayerTestClients registers n unconnected clients on one hub, sharing one
// state tracker and a queue manager that always yields song-1. The hub clears
// now-playing testClearGrace after its last player leaves.
func newPlayerTestClients(n int) []*Client {
	h := NewHub()
	h.clearGrace = testClearGrace
	st := player.NewStateTracker()
	mgr := &recordingQueueManager{song: &models.Song{ID: "song-1", Title: "Rain"}}
	clients := make([]*Client, n)
	for i := range clients {
		clients[i] = NewClient(h, nil, mgr, "", st)
		h.Register(clients[i], nil)
	}
	return clients
}

// startPlaying makes c act as a player that got song-1, then empties its send
// buffer and the hub's broadcast channel.
func startPlaying(t *testing.T, c *Client) {
	t.Helper()
	c.handleMessage(Message{Type: MsgNeedSong})
	drainSent(t, c, MsgPlaySong)
	for len(c.hub.broadcast) > 0 {
		<-c.hub.broadcast
	}
}

// waitNowPlayingCleared waits for the grace-delayed clear's broadcast, then
// checks it as assertNowPlayingCleared does.
func waitNowPlayingCleared(t *testing.T, c *Client) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(c.hub.broadcast) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("now-playing not cleared within 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
	assertNowPlayingCleared(t, c)
}

// assertSongKeptPastGrace waits well past the hub's clear grace, then checks
// song-1 is still current and nothing was broadcast.
func assertSongKeptPastGrace(t *testing.T, c *Client) {
	t.Helper()
	time.Sleep(3 * c.hub.clearGrace)
	assertSongKept(t, c)
}

// hasPendingClear reports whether h has a last-player clear scheduled.
func hasPendingClear(h *Hub) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.pendingClear != nil
}

func TestLastPlayerDisconnectClearsNowPlaying(t *testing.T) {
	c := newPlayerTestClients(1)[0]
	startPlaying(t, c)

	c.hub.Unregister(c)

	waitNowPlayingCleared(t, c)
}

func TestPlayerDisconnectKeepsNowPlayingWhileAnotherPlayerRemains(t *testing.T) {
	clients := newPlayerTestClients(2)
	first, second := clients[0], clients[1]
	startPlaying(t, first)
	second.handleMessage(Message{Type: MsgPeekNext})

	first.hub.Unregister(first)
	assertSongKept(t, first)

	second.hub.Unregister(second)
	waitNowPlayingCleared(t, second)
}

func TestNonPlayerDisconnectKeepsNowPlaying(t *testing.T) {
	clients := newPlayerTestClients(2)
	playing, overlay := clients[0], clients[1]
	startPlaying(t, playing)
	overlay.handleMessage(Message{Type: MsgHeartbeat})
	drainSent(t, overlay, MsgHeartbeatAck)

	overlay.hub.Unregister(overlay)

	assertSongKept(t, playing)
}

func TestLastPlayerDisconnectWithNoSongBroadcastsNothing(t *testing.T) {
	c := NewClient(NewHub(), nil, emptyQueueManager{}, "", player.NewStateTracker())
	c.hub.clearGrace = testClearGrace
	c.hub.Register(c, nil)
	c.handleMessage(Message{Type: MsgNeedSong})
	drainSent(t, c, MsgError)

	c.hub.Unregister(c)

	time.Sleep(3 * testClearGrace)
	assertNothingPublished(t, c)
}

func TestShutdownKeepsNowPlaying(t *testing.T) {
	c := newPlayerTestClients(1)[0]
	startPlaying(t, c)

	if err := c.hub.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown = %v, want nil", err)
	}

	assertSongKept(t, c)
}

func TestDroppingLastPlayerTwiceClearsOnce(t *testing.T) {
	c := newPlayerTestClients(1)[0]
	startPlaying(t, c)

	c.hub.Unregister(c)
	waitNowPlayingCleared(t, c)
	c.stateTracker.SetCurrentSong(&models.Song{ID: "song-1", Title: "Rain"})
	c.hub.Unregister(c)

	assertSongKeptPastGrace(t, c)
}

// An old frontend reconnects without ?role and only becomes a player on its
// need_song; that must cancel the clear the previous player's leaving set up.
func TestLegacyPlayerNeedSongCancelsPendingClear(t *testing.T) {
	clients := newPlayerTestClients(2)
	gone, legacy := clients[0], clients[1]
	startPlaying(t, gone)

	gone.hub.Unregister(gone)
	if !hasPendingClear(gone.hub) {
		t.Fatal("last player left, want a pending clear")
	}
	startPlaying(t, legacy)

	if hasPendingClear(legacy.hub) {
		t.Fatal("need_song left the clear pending, want it cancelled")
	}
	assertSongKeptPastGrace(t, legacy)
}

func TestShutdownCancelsPendingClear(t *testing.T) {
	c := newPlayerTestClients(1)[0]
	startPlaying(t, c)
	c.hub.Unregister(c)

	if err := c.hub.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown = %v, want nil", err)
	}

	if hasPendingClear(c.hub) {
		t.Fatal("clear still pending after Shutdown")
	}
	assertSongKeptPastGrace(t, c)
}

// newRoleTestServer serves an Endpoint whose tracker holds song-1 and whose
// hub clears now-playing grace after its last player leaves. It returns the
// hub, the tracker and the /ws URL.
func newRoleTestServer(t *testing.T, grace time.Duration) (*Hub, *player.StateTracker, string) {
	t.Helper()
	hub := NewHub()
	hub.clearGrace = grace
	go hub.Run()
	tracker := player.NewStateTracker()
	tracker.SetCurrentSong(&models.Song{ID: "song-1", Title: "Rain"})
	srv := httptest.NewServer(&Endpoint{Hub: hub, QueueMgr: emptyQueueManager{}, StateTracker: tracker})
	t.Cleanup(srv.Close)
	return hub, tracker, "ws" + strings.TrimPrefix(srv.URL, "http")
}

// A peer that connects with ?role=player is a player before it sends
// anything: its leaving clears now-playing for the overlay that stays.
func TestRolePlayerDisconnectClearsNowPlaying(t *testing.T) {
	_, tracker, url := newRoleTestServer(t, testClearGrace)
	overlay := dialWS(t, url)
	dialWS(t, url+"?role=player").Close()

	var msg Message
	if err := overlay.ReadJSON(&msg); err != nil || msg.Type != MsgState {
		t.Fatalf("overlay frame = %+v, %v; want %s", msg, err, MsgState)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(msg.Payload, &fields); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if _, ok := fields["current_song"]; ok {
		t.Fatalf("state payload %s has current_song, want none", msg.Payload)
	}
	if cur := tracker.GetCurrentSong(); cur != nil {
		t.Fatalf("current song = %s, want nil", cur.ID)
	}
}

// A player that reconnects mid-track sends nothing until the track ends, so
// its ?role=player reconnect alone must keep now-playing.
func TestRolePlayerReconnectInsideGraceKeepsNowPlaying(t *testing.T) {
	const grace = 300 * time.Millisecond
	hub, tracker, url := newRoleTestServer(t, grace)
	dialWS(t, url+"?role=player").Close()
	// Redial only once the server has dropped the first player, so the
	// reconnect has a pending clear to cancel.
	deadline := time.Now().Add(5 * time.Second)
	for !hasPendingClear(hub) {
		if time.Now().After(deadline) {
			t.Fatal("first player's close scheduled no clear within 5s")
		}
		time.Sleep(time.Millisecond)
	}
	dialWS(t, url+"?role=player")
	if hasPendingClear(hub) {
		t.Fatal("?role=player reconnect left the clear pending, want it cancelled")
	}

	time.Sleep(2 * grace)

	if cur := tracker.GetCurrentSong(); cur == nil || cur.ID != "song-1" {
		t.Fatalf("current song = %v, want song-1", cur)
	}
}
