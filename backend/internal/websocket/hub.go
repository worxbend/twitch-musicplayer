package websocket

import (
	"context"
	"log"
	"sync"
	"time"

	ws "github.com/gorilla/websocket"
)

// shutdownReason is the close text peers receive with 1001 Going Away.
const shutdownReason = "server shutting down"

// lastPlayerClearGrace is how long now-playing survives its last player, so
// a player that reconnects mid-track keeps it.
const lastPlayerClearGrace = 5 * time.Second

// Hub manages all active WebSocket connections.
type Hub struct {
	clients   map[*Client]struct{}
	broadcast chan Message
	mu        sync.RWMutex

	// closing is set by Shutdown (guarded by mu). Once set, Register refuses
	// new clients and goPump stops tracking pumps.
	closing bool
	// pumps counts write pumps Shutdown waits for. Add only runs under mu
	// while closing is false, so no Add can race Shutdown's Wait.
	pumps sync.WaitGroup

	// clearGrace is the delay before the last player's leaving clears
	// now-playing. pendingClear is that scheduled clear, nil when none is
	// pending (guarded by mu).
	clearGrace   time.Duration
	pendingClear *time.Timer
}

// NewHub creates a new Hub ready to Run.
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]struct{}),
		broadcast:  make(chan Message, 256),
		clearGrace: lastPlayerClearGrace,
	}
}

// Run delivers queued broadcasts to clients. It must be started in its own
// goroutine. Register and Unregister do not go through Run: they edit the
// client set directly under h.mu so their order is never reshuffled.
func (h *Hub) Run() {
	for msg := range h.broadcast {
		h.deliver(msg)
	}
}

// deliver fans msg out to every client, dropping any whose send buffer has
// filled up (a peer that cannot keep up is treated as gone).
func (h *Hub) deliver(msg Message) {
	h.mu.RLock()
	var stalled []*Client
	for c := range h.clients {
		if !c.trySend(msg) {
			stalled = append(stalled, c)
		}
	}
	h.mu.RUnlock()

	// Dropped only after RUnlock: drop takes the write lock.
	for _, c := range stalled {
		h.drop(c)
	}
}

// drop removes a client from the hub and signals its write pump to finish.
// When c was the last client acting as a player, it schedules a clear of
// now-playing after clearGrace; a player joining inside that window cancels
// it. Shutdown does not come through here, so it keeps now-playing.
func (h *Hub) drop(c *Client) {
	h.mu.Lock()
	_, known := h.clients[c]
	delete(h.clients, c)
	if known && c.isPlayer() && !h.hasPlayerLocked() {
		h.scheduleClearLocked(c)
	}
	h.mu.Unlock()

	if known {
		c.close()
	}
}

// scheduleClearLocked replaces any pending clear with one that runs after
// clearGrace unless the hub is closing, a player has joined, or a newer clear
// replaced it. The caller must hold h.mu.
func (h *Hub) scheduleClearLocked(c *Client) {
	h.cancelClearLocked()
	var timer *time.Timer
	timer = time.AfterFunc(h.clearGrace, func() {
		h.mu.Lock()
		run := !h.closing && h.pendingClear == timer && !h.hasPlayerLocked()
		if run {
			h.pendingClear = nil
		}
		h.mu.Unlock()

		// Outside h.mu: clearNowPlaying broadcasts and reads the queue
		// manager. A player whose first need_song is in flight may set a song
		// again right after; that is the correct end state.
		if run {
			c.clearNowPlaying(context.Background())
		}
	})
	h.pendingClear = timer
}

// cancelClearLocked stops the pending clear, if any. The caller must hold h.mu.
func (h *Hub) cancelClearLocked() {
	if h.pendingClear != nil {
		h.pendingClear.Stop()
		h.pendingClear = nil
	}
}

// playerJoined cancels the pending clear once c starts acting as a player.
// A client already dropped cancels nothing.
func (h *Hub) playerJoined(c *Client) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		h.cancelClearLocked()
	}
	h.mu.Unlock()
}

// hasPlayerLocked reports whether any registered client acts as a player.
// The caller must hold h.mu.
func (h *Hub) hasPlayerLocked() bool {
	for other := range h.clients {
		if other.isPlayer() {
			return true
		}
	}
	return false
}

// Broadcast enqueues msg for delivery to all connected clients (non-blocking).
func (h *Hub) Broadcast(msg Message) {
	select {
	case h.broadcast <- msg:
	default:
		// Drop if the broadcast channel itself is full rather than blocking the caller.
	}
}

// Register adds a client to the hub. During shutdown it refuses the client
// and closes it with 1001 Going Away instead.
//
// A non-nil first builds the client's first frame. It runs and is queued
// under the write lock that deliver needs, so no broadcast can reach c ahead
// of it, and every later broadcast reflects state at least as new as the
// state first read. first must not call back into the hub's locking methods.
func (h *Hub) Register(c *Client, first func() (Message, error)) {
	h.mu.Lock()
	refused := h.closing
	if !refused {
		if first != nil {
			queueFirstFrame(c, first)
		}
		h.clients[c] = struct{}{}
		if c.isPlayer() {
			h.cancelClearLocked()
		}
	}
	h.mu.Unlock()

	if refused {
		c.closeWith(ws.CloseGoingAway, shutdownReason)
	}
}

// queueFirstFrame queues first's message on c. An encode failure is logged
// and the client is still registered, just without the frame.
func queueFirstFrame(c *Client, first func() (Message, error)) {
	msg, err := first()
	if err != nil {
		log.Printf("websocket first frame: %v", err)
		return
	}
	c.trySend(msg)
}

// goPump runs pump in its own goroutine. Pumps started before Shutdown are
// waited for by Shutdown; later ones are not tracked.
func (h *Hub) goPump(pump func()) {
	h.mu.Lock()
	tracked := !h.closing
	if tracked {
		h.pumps.Add(1)
	}
	h.mu.Unlock()

	go func() {
		if tracked {
			defer h.pumps.Done()
		}
		pump()
	}()
}

// Shutdown closes every client with 1001 Going Away, refuses new ones, and
// waits for the write pumps to send their close frames. It returns ctx.Err()
// if ctx ends first. Safe to call more than once.
func (h *Hub) Shutdown(ctx context.Context) error {
	h.mu.Lock()
	h.closing = true
	h.cancelClearLocked()
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.clients = make(map[*Client]struct{})
	h.mu.Unlock()

	for _, c := range clients {
		c.closeWith(ws.CloseGoingAway, shutdownReason)
	}

	done := make(chan struct{})
	go func() {
		h.pumps.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Unregister removes a client from the hub and stops its write pump.
func (h *Hub) Unregister(c *Client) {
	h.drop(c)
}

// ClientCount returns the number of currently connected clients.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
