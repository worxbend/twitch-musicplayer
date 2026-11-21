package websocket

import (
	"context"
	"encoding/json"
	"errors"
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

// dialTestServer starts ServeWS behind httptest and returns a connected peer
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeWS(hub, queueMgr, "", player.NewStateTracker(), w, r)
	}))
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
	return hub, conn
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
	song *models.Song

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

// newDryTestClient builds an unconnected client, like newSendTestClient, whose
// tracker is already playing song-1 and whose queue manager is mgr.
func newDryTestClient(mgr QueueManager) *Client {
	st := player.NewStateTracker()
	st.SetCurrentSong(&models.Song{ID: "song-1", Title: "Rain"})
	return NewClient(NewHub(), nil, mgr, "", st)
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

func TestSongFinishedOnEmptyQueueClearsNowPlaying(t *testing.T) {
	c := newDryTestClient(emptyQueueManager{})

	c.handleMessage(Message{Type: MsgSongFinished})

	drainSent(t, c, MsgError)
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

	// A second song_finished on the still-empty queue errors again but
	// broadcasts nothing: the clear is idempotent.
	c.handleMessage(Message{Type: MsgSongFinished})

	drainSent(t, c, MsgError)
	if n := len(c.hub.broadcast); n != 0 {
		t.Fatalf("hub.broadcast holds %d messages after second finish, want 0", n)
	}
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
