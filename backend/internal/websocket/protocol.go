package websocket

import (
	"encoding/json"

	"lofi-radio-backend/internal/models"
)

// Client -> Server message types
const (
	MsgNeedSong     = "need_song"     // ready, send me a song
	MsgSongFinished = "song_finished" // payload: SongFinishedPayload
	MsgHeartbeat    = "heartbeat"
	MsgPeekNext     = "peek_next" // peek at next queued song for prebuffering (no dequeue)
)

// Server -> Client message types
const (
	MsgPlaySong      = "play_song"      // payload: PlaySongPayload (unicast to player)
	MsgPrebufferSong = "prebuffer_song" // response to peek_next: preload this (unicast, no dequeue)
	MsgQueueUpdated  = "queue_updated"  // payload: QueueUpdatedPayload (broadcast)
	MsgNowPlaying    = "now_playing"    // payload: NowPlayingPayload (broadcast, for management UIs)
	MsgSkipNow       = "skip_now"       // no payload; tells audio clients to skip immediately (broadcast)
	MsgError         = "error"          // payload: ErrorPayload
	MsgHeartbeatAck  = "heartbeat_ack"
	MsgState         = "state" // sent on connect: PlayerState
)

// Message is the envelope for all WebSocket messages.
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// PlaySongPayload is sent by the server to instruct the client to play a song.
type PlaySongPayload struct {
	Song       models.Song `json:"song"`
	StreamURL  string      `json:"stream_url"`
	HistoryID  string      `json:"history_id"`
	QueueDepth int         `json:"queue_depth"`
}

// SongFinishedPayload is sent by the client when a song finishes playing.
type SongFinishedPayload struct {
	SongID    string `json:"song_id"`
	HistoryID string `json:"history_id"`
}

// QueueUpdatedPayload is broadcast when the queue changes.
type QueueUpdatedPayload struct {
	QueueDepth int    `json:"queue_depth"`
	Reason     string `json:"reason"` // "song_added" | "song_removed" | "song_moved" | "skipped" | "play_next" | "cleared" | "new_file" | "file_removed" | "file_updated" | "song_started" | "song_blocked" | "song_unblocked"
	// NextSong is the first playable queued song, absent when none is queued.
	NextSong *models.Song `json:"next_song,omitempty"`
}

// QueueUpdatedMessage builds the queue_updated frame for the queue items and
// reason: the depth counts every entry, next_song is the first playable one.
func QueueUpdatedMessage(items []*models.QueueItem, reason string) (Message, error) {
	return Encode(MsgQueueUpdated, QueueUpdatedPayload{
		QueueDepth: len(items),
		Reason:     reason,
		NextSong:   models.NextPlayableSong(items),
	})
}

// NowPlayingPayload is broadcast whenever a new song starts playing.
// Management UIs listen for this to update their "now playing" display.
type NowPlayingPayload struct {
	Song       models.Song `json:"song"`
	QueueDepth int         `json:"queue_depth"`
	// HistoryID matches play_song's history_id. It is set on a real song start
	// and absent when the current song is re-announced after a retag, so
	// overlays can tell a repeat play of the same title from a tag refresh.
	HistoryID string `json:"history_id,omitempty"`
}

// Error codes carried by ErrorPayload.Code. The set is closed: clients treat
// an unknown or missing code as ErrCodeInternal.
const (
	ErrCodeQueueEmpty   = "queue_empty"   // manual_only queue ran dry
	ErrCodeLibraryEmpty = "library_empty" // no songs in the library
	ErrCodeInternal     = "internal"      // anything else; details stay in the server log
)

// ErrorPayload carries a stable error code and a display-only message to the
// client. Message is never raw error text, so internal details do not leak.
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Encode wraps a typed payload into a Message with JSON encoding.
func Encode(msgType string, payload any) (Message, error) {
	if payload == nil {
		return Message{Type: msgType}, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Message{}, err
	}
	return Message{
		Type:    msgType,
		Payload: json.RawMessage(raw),
	}, nil
}

// DecodePayload unmarshals msg.Payload into target.
func DecodePayload(msg Message, target any) error {
	return json.Unmarshal(msg.Payload, target)
}
