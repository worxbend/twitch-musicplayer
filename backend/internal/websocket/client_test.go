package websocket

import (
	"context"
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

// dialTestServer starts ServeWS behind httptest and returns a connected peer
// whose initial state frame has already been consumed.
func dialTestServer(t *testing.T) *ws.Conn {
	t.Helper()
	hub := NewHub()
	go hub.Run()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeWS(hub, emptyQueueManager{}, "", player.NewStateTracker(), w, r)
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
	conn := dialTestServer(t)
	if err := conn.WriteJSON(Message{Type: MsgHeartbeat}); err != nil {
		t.Fatalf("write: %v", err)
	}
	var reply Message
	if err := conn.ReadJSON(&reply); err != nil || reply.Type != MsgHeartbeatAck {
		t.Fatalf("reply = %+v, %v; want %s", reply, err, MsgHeartbeatAck)
	}
}

func TestReadPumpClosesOnOversizeFrame(t *testing.T) {
	conn := dialTestServer(t)
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
