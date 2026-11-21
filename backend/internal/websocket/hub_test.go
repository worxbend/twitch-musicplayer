package websocket

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	ws "github.com/gorilla/websocket"
)

// newTestClient builds a Client with only the fields the hub touches, so hub
// behaviour can be exercised without a real network connection.
func newTestClient(bufSize int) *Client {
	return &Client{
		send:   make(chan Message, bufSize),
		closed: make(chan struct{}),
	}
}

// TestSendAfterDropDoesNotPanic covers the crash where the hub closed a
// client's send channel while other goroutines were still sending on it: a send
// on a closed channel panics even inside a select with a default branch.
func TestSendAfterDropDoesNotPanic(t *testing.T) {
	h := NewHub()
	c := newTestClient(1)
	h.clients[c] = struct{}{}

	h.drop(c)

	if c.trySend(Message{Type: MsgHeartbeatAck}) {
		t.Fatal("trySend should report failure for a dropped client")
	}
	select {
	case <-c.closed:
	default:
		t.Fatal("drop should have closed the client")
	}
}

// TestDeliverDropsStalledClientsWithoutDeadlock covers the hub deadlock: Run
// used to remove an overflowed client by sending to an unregister channel only
// Run itself drained, so the hub froze once that 16-slot buffer filled. More
// stalled clients than that buffer held are used here on purpose.
func TestDeliverDropsStalledClientsWithoutDeadlock(t *testing.T) {
	h := NewHub()
	const stalledClients = 64

	for i := 0; i < stalledClients; i++ {
		c := newTestClient(0) // zero buffer: every send fails immediately
		h.clients[c] = struct{}{}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.deliver(Message{Type: MsgSkipNow})
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("deliver deadlocked while dropping stalled clients")
	}

	if got := h.ClientCount(); got != 0 {
		t.Fatalf("expected all stalled clients dropped, %d remain", got)
	}
}

// TestDropIsIdempotent guards the close-once behaviour, since a client is
// commonly dropped twice: once by its own read pump and once by a broadcast
// that found its buffer full.
func TestDropIsIdempotent(t *testing.T) {
	h := NewHub()
	c := newTestClient(1)
	h.clients[c] = struct{}{}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.drop(c)
		}()
	}
	wg.Wait()
}

// TestDeliverReachesLiveClients is the happy path: a client with room in its
// buffer receives the broadcast and stays registered.
func TestDeliverReachesLiveClients(t *testing.T) {
	h := NewHub()
	c := newTestClient(1)
	h.clients[c] = struct{}{}

	h.deliver(Message{Type: MsgSkipNow})

	select {
	case msg := <-c.send:
		if msg.Type != MsgSkipNow {
			t.Fatalf("got message type %q, want %q", msg.Type, MsgSkipNow)
		}
	default:
		t.Fatal("live client did not receive the broadcast")
	}
	if got := h.ClientCount(); got != 1 {
		t.Fatalf("live client should stay registered, count=%d", got)
	}
}

// TestRegisterUnregisterOrderIsPreserved covers the ghost client: Register and
// Unregister used separate channels drained by one select, so a peer that
// disconnected right after connecting could be unregistered before it was
// registered, leaving a dead client counted in the hub.
func TestRegisterUnregisterOrderIsPreserved(t *testing.T) {
	h := NewHub()
	go h.Run()

	const clients = 2000
	all := make([]*Client, 0, clients)
	for i := 0; i < clients; i++ {
		c := NewClient(h, nil, nil, "", nil)
		h.Register(c)
		h.Unregister(c)
		all = append(all, c)
	}

	deadline := time.Now().Add(2 * time.Second)
	for h.ClientCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("ghost clients left in hub: %d", h.ClientCount())
		}
		time.Sleep(time.Millisecond)
	}
	for i, c := range all {
		select {
		case <-c.closed:
		default:
			t.Fatalf("client %d was unregistered but never closed", i)
		}
	}
}

// assertCloseCode reads from conn until the close frame and checks its code.
func assertCloseCode(t *testing.T, conn *ws.Conn, want int) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
	for {
		_, _, err := conn.ReadMessage()
		if err == nil {
			continue
		}
		var closeErr *ws.CloseError
		if !errors.As(err, &closeErr) || closeErr.Code != want {
			t.Fatalf("read error = %v, want close %d", err, want)
		}
		return
	}
}

// TestShutdownSendsGoingAway covers peers seeing 1006 on server exit:
// httpServer.Shutdown never closes hijacked /ws connections.
func TestShutdownSendsGoingAway(t *testing.T) {
	hub, conn := dialTestHub(t, emptyQueueManager{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := hub.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown = %v, want nil", err)
	}

	assertCloseCode(t, conn, ws.CloseGoingAway)
	if got := hub.ClientCount(); got != 0 {
		t.Fatalf("ClientCount after Shutdown = %d, want 0", got)
	}
}

// TestDropSendsNormalClosure covers the empty close payload peers read as 1005.
func TestDropSendsNormalClosure(t *testing.T) {
	hub, conn := dialTestHub(t, emptyQueueManager{})

	hub.mu.RLock()
	var server *Client
	for c := range hub.clients {
		server = c
	}
	hub.mu.RUnlock()
	if server == nil {
		t.Fatal("server-side client not registered")
	}

	hub.Unregister(server)
	assertCloseCode(t, conn, ws.CloseNormalClosure)
}

func TestRegisterAfterShutdownClosesClient(t *testing.T) {
	h := NewHub()
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown = %v, want nil", err)
	}

	c := NewClient(h, nil, nil, "", nil)
	h.Register(c)

	if got := h.ClientCount(); got != 0 {
		t.Fatalf("ClientCount = %d, want 0", got)
	}
	select {
	case <-c.closed:
	default:
		t.Fatal("client registered during shutdown was not closed")
	}
	if c.closeCode != ws.CloseGoingAway {
		t.Fatalf("closeCode = %d, want %d", c.closeCode, ws.CloseGoingAway)
	}
}

func TestShutdownIsIdempotent(t *testing.T) {
	h := NewHub()
	c := newTestClient(1)
	h.Register(c)

	for i := 0; i < 2; i++ {
		if err := h.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown call %d = %v, want nil", i+1, err)
		}
	}
	if c.closeCode != ws.CloseGoingAway {
		t.Fatalf("closeCode = %d, want %d", c.closeCode, ws.CloseGoingAway)
	}
}

func TestShutdownRespectsContext(t *testing.T) {
	h := NewHub()
	h.pumps.Add(1) // a pump that never finishes
	t.Cleanup(h.pumps.Done)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := h.Shutdown(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown = %v, want %v", err, context.DeadlineExceeded)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Shutdown took %v, want well under 1s", elapsed)
	}
}
