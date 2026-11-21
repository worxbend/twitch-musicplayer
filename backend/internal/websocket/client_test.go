package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ws "github.com/gorilla/websocket"

	"lofi-radio-backend/internal/models"
	"lofi-radio-backend/internal/player"
)

type emptyQueueManager struct{}

func (emptyQueueManager) NextSong(context.Context) (*models.Song, error) {
	return nil, errors.New("queue is empty")
}

func (emptyQueueManager) ListQueue(context.Context) ([]*models.QueueItem, error) { return nil, nil }

func (emptyQueueManager) TotalSongs(context.Context) (int, error) { return 0, nil }

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
