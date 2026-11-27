package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	ws "github.com/gorilla/websocket"

	"github.com/google/uuid"
	"lofi-radio-backend/internal/models"
	"lofi-radio-backend/internal/player"
	"lofi-radio-backend/internal/queue"
)

const (
	writeWait   = 10 * time.Second
	pongWait    = 60 * time.Second
	pingPeriod  = 30 * time.Second
	sendBufSize = 16

	// maxMessageSize caps inbound frames. Client messages (need_song,
	// song_finished, peek_next, heartbeat) are a few hundred bytes at most.
	maxMessageSize = 4096
)

// QueueManager is the subset of queue.Manager the client uses.
type QueueManager interface {
	NextSong(ctx context.Context) (*models.Song, error)
	ListQueue(ctx context.Context) ([]*models.QueueItem, error)
	TotalSongs(ctx context.Context) (int, error)
	Requeue(ctx context.Context, songID string) error
}

// Client represents a single connected WebSocket peer.
type Client struct {
	hub          *Hub
	conn         *ws.Conn
	send         chan Message
	queueMgr     QueueManager
	baseURL      string
	stateTracker *player.StateTracker

	// closed is closed exactly once, when the hub drops this client. The send
	// channel itself is deliberately never closed: the read pump, the hub's
	// broadcast loop and the initial-state sender may all still be mid-send,
	// and a send on a closed channel panics even inside a select with a
	// default. Signalling through a separate channel lets senders bail safely.
	closed    chan struct{}
	closeOnce sync.Once
	// sendMu makes trySend's closed check and buffer send one step against
	// closeWith, so nothing enters send once closed is closed and writePump's
	// final drain sees every frame left behind.
	sendMu sync.Mutex
	// closeCode and closeText go into the close frame writePump sends. They
	// are written before closed is closed, so writePump reads them safely
	// after <-closed.
	closeCode int
	closeText string
}

// NewClient constructs a Client. Serve peers through Endpoint instead.
func NewClient(hub *Hub, conn *ws.Conn, queueMgr QueueManager, baseURL string, st *player.StateTracker) *Client {
	return &Client{
		hub:          hub,
		conn:         conn,
		send:         make(chan Message, sendBufSize),
		queueMgr:     queueMgr,
		baseURL:      baseURL,
		stateTracker: st,
		closed:       make(chan struct{}),
	}
}

// close marks the client as gone with 1000 Normal Closure and wakes its write
// pump. Safe to call repeatedly and from any goroutine.
func (c *Client) close() {
	c.closeWith(ws.CloseNormalClosure, "")
}

// closeWith is close with an explicit close code and reason. The first call
// wins; later calls do nothing.
func (c *Client) closeWith(code int, text string) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	c.closeOnce.Do(func() {
		c.closeCode = code
		c.closeText = text
		close(c.closed)
	})
}

// trySend queues msg for delivery without ever blocking the caller. It reports
// false when the client is already closed or its send buffer is full.
func (c *Client) trySend(msg Message) bool {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	// Checked in two steps on purpose. A single select listing both cases
	// would pick at random whenever both are ready, so a closed client with a
	// free buffer slot would still accept messages.
	select {
	case <-c.closed:
		return false
	default:
	}

	select {
	case c.send <- msg:
		return true
	default:
		return false
	}
}

// Endpoint is the /ws handler: it connects peers to one hub.
type Endpoint struct {
	Hub          *Hub
	QueueMgr     QueueManager
	BaseURL      string
	StateTracker *player.StateTracker
	// AllowOrigin reports whether a page at a browser origin may connect.
	// Browsers do not apply CORS to WebSocket upgrades, so without this any
	// page could drive the station. Nil refuses every browser origin.
	AllowOrigin func(origin string) bool
}

// ServeHTTP upgrades the HTTP connection to WebSocket, registers the client
// with the hub and its initial state message, and starts the read/write pumps.
// An upgrade from a refused origin is answered 403.
func (e *Endpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	upgrader := ws.Upgrader{ReadBufferSize: 1024, WriteBufferSize: 1024, CheckOrigin: e.checkOrigin}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade error: %v", err)
		return
	}

	c := NewClient(e.Hub, conn, e.QueueMgr, e.BaseURL, e.StateTracker)
	// Register queues the snapshot as the first frame, ahead of any
	// broadcast. The request context is not used here: it is cancelled as
	// soon as ServeHTTP returns, which would abort these lookups.
	e.Hub.Register(c, func() (Message, error) {
		return Encode(MsgState, c.snapshotState(context.Background()))
	})

	e.Hub.goPump(c.writePump)
	go c.readPump()
}

// checkOrigin lets in requests without an Origin header, which only
// non-browser clients send, and browser origins AllowOrigin accepts.
func (e *Endpoint) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	return origin == "" || (e.AllowOrigin != nil && e.AllowOrigin(origin))
}

// snapshotState builds the PlayerState sent to a peer on connect and
// broadcast when now-playing is cleared. Lookup
// failures degrade to zero counts rather than withholding the whole snapshot.
func (c *Client) snapshotState(ctx context.Context) models.PlayerState {
	state := models.PlayerState{CurrentSong: c.stateTracker.GetCurrentSong()}
	if items, err := c.queueMgr.ListQueue(ctx); err == nil {
		state.QueueLength = len(items)
	} else {
		log.Printf("websocket state: list queue: %v", err)
	}
	if total, err := c.queueMgr.TotalSongs(ctx); err == nil {
		state.TotalSongs = total
	} else {
		log.Printf("websocket state: count songs: %v", err)
	}
	return state
}

// readPump reads incoming messages from the WebSocket connection.
func (c *Client) readPump() {
	defer func() {
		c.hub.Unregister(c)
		c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			if ws.IsUnexpectedCloseError(err, ws.CloseGoingAway, ws.CloseAbnormalClosure) {
				log.Printf("websocket read error: %v", err)
			}
			return
		}

		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("websocket unmarshal error: %v", err)
			continue
		}

		c.handleMessage(msg)
	}
}

// handleMessage dispatches an incoming client message.
func (c *Client) handleMessage(msg Message) {
	ctx := context.Background()

	switch msg.Type {
	case MsgNeedSong:
		c.sendNextSong(ctx)

	case MsgSongFinished:
		// Song is already marked played when it starts; just request the next
		// one. A dry queue (manual_only) or an empty library (auto strategies)
		// clears now-playing. Only a finished song may clear it: need_song also
		// comes from a second player tab while the first is still playing.
		err := c.sendNextSong(ctx)
		if errors.Is(err, queue.ErrQueueEmpty) || errors.Is(err, queue.ErrLibraryEmpty) {
			c.clearNowPlaying(ctx)
		}

	case MsgPeekNext:
		// Client wants to prebuffer the next song without dequeuing it.
		c.sendPrebuffer(ctx)

	case MsgHeartbeat:
		c.trySend(Message{Type: MsgHeartbeatAck})

	default:
		log.Printf("unknown message type: %q", msg.Type)
	}
}

// sendNextSong dequeues the next song and pushes a play_song message to this
// client. A closed client dequeues nothing. When the message cannot be queued
// (client closed meanwhile or send buffer full) the song goes back to the
// front of the queue and shared state is left untouched. Only after a
// successful send does it update the state tracker and broadcast now_playing
// and queue_updated so management UIs stay in sync. It returns the NextSong
// error, after sending it to this client as an error message, and nil on
// every other path.
func (c *Client) sendNextSong(ctx context.Context) error {
	select {
	case <-c.closed:
		return nil
	default:
	}

	song, err := c.queueMgr.NextSong(ctx)
	if err != nil {
		log.Printf("next song error: %v", err)
		if errMsg, encErr := Encode(MsgError, ErrorPayload{Message: err.Error()}); encErr == nil {
			c.trySend(errMsg)
		}
		return err
	}

	items, _ := c.queueMgr.ListQueue(ctx)
	queueDepth := len(items)

	payload := PlaySongPayload{
		Song:       *song,
		StreamURL:  c.streamURL(song.ID),
		HistoryID:  uuid.New().String(),
		QueueDepth: queueDepth,
	}

	playMsg, err := Encode(MsgPlaySong, payload)
	if err != nil {
		log.Printf("encode play_song: %v", err)
		return nil
	}

	if !c.trySend(playMsg) {
		log.Printf("play_song not delivered (client closed or send buffer full); requeueing %s", song.ID)
		if err := c.queueMgr.Requeue(ctx, song.ID); err != nil {
			log.Printf("requeue undelivered song: %v", err)
		}
		return nil
	}

	// Update shared state tracker so the REST player endpoint is current.
	if c.stateTracker != nil {
		c.stateTracker.SetCurrentSong(song)
	}

	// Broadcast to all peers so management UIs get live updates.
	if nowMsg, encErr := Encode(MsgNowPlaying, NowPlayingPayload{Song: *song, QueueDepth: queueDepth, HistoryID: payload.HistoryID}); encErr == nil {
		c.hub.Broadcast(nowMsg)
	}
	if queueMsg, encErr := Encode(MsgQueueUpdated, QueueUpdatedPayload{QueueDepth: queueDepth, Reason: "song_started"}); encErr == nil {
		c.hub.Broadcast(queueMsg)
	}
	return nil
}

// clearNowPlaying clears the tracked current song and broadcasts a state
// snapshot without current_song, so overlays and the admin console stop
// showing the finished song. It does nothing when no song was set.
func (c *Client) clearNowPlaying(ctx context.Context) {
	if c.stateTracker == nil || !c.stateTracker.ClearCurrentSong() {
		return
	}
	if msg, err := Encode(MsgState, c.snapshotState(ctx)); err == nil {
		c.hub.Broadcast(msg)
	} else {
		log.Printf("encode state: %v", err)
	}
}

// sendPrebuffer peeks at the first playable queue entry and sends the next song's URL to this
// client for prebuffering. It does NOT dequeue or update shared state.
func (c *Client) sendPrebuffer(ctx context.Context) {
	items, err := c.queueMgr.ListQueue(ctx)
	if err != nil {
		return
	}
	first := models.FirstPlayable(items)
	if first == nil {
		return
	}
	payload := PlaySongPayload{
		Song:      *first.Song,
		StreamURL: c.streamURL(first.Song.ID),
		HistoryID: "", // intentionally empty — this is a prebuffer hint only
	}
	msg, err := Encode(MsgPrebufferSong, payload)
	if err != nil {
		return
	}
	c.trySend(msg)
}

// streamURL builds the absolute URL a client fetches audio bytes from.
func (c *Client) streamURL(songID string) string {
	return c.baseURL + "/v1/songs/" + songID + "/content"
}

// writePump drains the client's send channel and writes messages to the
// connection. It exits when the hub closes the client or a write fails; a
// failed write closes the client itself. Either way every play_song the peer
// never got goes back to the front of the queue.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		// Checked alone first: a select with both cases ready picks at
		// random, and a closed client should write no more buffered frames.
		select {
		case <-c.closed:
			c.finishClosed()
			return
		default:
		}

		select {
		case msg := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteJSON(msg); err != nil {
				log.Printf("websocket write error: %v", err)
				c.abandon(msg)
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(ws.PingMessage, nil); err != nil {
				c.abandon()
				return
			}

		case <-c.closed:
			c.finishClosed()
			return
		}
	}
}

// finishClosed sends the close frame and requeues buffered play_song frames.
func (c *Client) finishClosed() {
	c.conn.SetWriteDeadline(time.Now().Add(writeWait))
	c.conn.WriteMessage(ws.CloseMessage, ws.FormatCloseMessage(c.closeCode, c.closeText)) //nolint:errcheck
	c.requeueUndelivered(c.drainSend())
}

// abandon closes the client after a failed write, so sendNextSong dequeues
// nothing more for a dead peer, and requeues the play_song frames in failed
// and in the send buffer. A write can fail after the peer already got the
// frame; that song then plays twice, which beats losing it.
func (c *Client) abandon(failed ...Message) {
	c.closeWith(ws.CloseAbnormalClosure, "")
	c.requeueUndelivered(append(failed, c.drainSend()...))
}

// drainSend empties the send buffer without blocking. Called only once the
// client is closed, when trySend adds nothing more, so it always finishes.
func (c *Client) drainSend() []Message {
	var left []Message
	for {
		select {
		case msg := <-c.send:
			left = append(left, msg)
		default:
			return left
		}
	}
}

// requeueUndelivered puts the songs of the play_song frames in msgs back at the
// front of the queue and ignores other frames. msgs is in send order and is
// requeued last first, so the earliest song ends up at the front.
func (c *Client) requeueUndelivered(msgs []Message) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Type != MsgPlaySong {
			continue
		}
		var payload PlaySongPayload
		if err := json.Unmarshal(msgs[i].Payload, &payload); err != nil {
			log.Printf("decode undelivered play_song: %v", err)
			continue
		}
		log.Printf("play_song not delivered (write failed or client closed); requeueing %s", payload.Song.ID)
		if err := c.queueMgr.Requeue(context.Background(), payload.Song.ID); err != nil {
			log.Printf("requeue undelivered song: %v", err)
		}
	}
}
