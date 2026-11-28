package models

import "time"

// Song represents an MP3 file tracked by the service.
type Song struct {
	ID           string    `json:"id"`
	Filename     string    `json:"filename"`
	Path         string    `json:"-"`
	Title        string    `json:"title"`
	Artist       string    `json:"artist"`
	Album        string    `json:"album"`
	DurationSecs float64   `json:"duration_secs"`
	SizeBytes    int64     `json:"size_bytes"`
	HasCover     bool      `json:"has_cover"`
	CoverVersion string    `json:"cover_version,omitempty"`
	AddedAt      time.Time `json:"added_at"`
	// ModTime is the file's modtime when it was last read. A resync compares
	// it, with SizeBytes, to spot a retag whose events were lost.
	ModTime time.Time `json:"-"`
}

// QueueSource indicates whether a queue entry was added automatically or manually.
type QueueSource string

const (
	QueueSourceAuto   QueueSource = "auto"
	QueueSourceManual QueueSource = "manual"
)

// QueueItem is one entry in the playback queue.
type QueueItem struct {
	ID       string      `json:"id"`
	SongID   string      `json:"song_id"`
	Song     *Song       `json:"song,omitempty"`
	Position int         `json:"position"`
	Source   QueueSource `json:"source"`
	AddedAt  time.Time   `json:"added_at"`
}

// FirstPlayable returns the first item whose Song is still in the library, or nil.
// It mirrors the dead-entry skip the queue manager applies at play time.
func FirstPlayable(items []*QueueItem) *QueueItem {
	for _, item := range items {
		if item.Song != nil {
			return item
		}
	}
	return nil
}

// NextPlayableSong returns the Song of FirstPlayable(items), or nil when no
// entry is playable. It is the "up next" title shown to overlays.
func NextPlayableSong(items []*QueueItem) *Song {
	if first := FirstPlayable(items); first != nil {
		return first.Song
	}
	return nil
}

// PlayerState is the snapshot of current playback state sent over WebSocket.
type PlayerState struct {
	CurrentSong *Song `json:"current_song,omitempty"`
	// ElapsedSecs is how long CurrentSong has been playing, computed by the
	// server when the snapshot is built and rounded to the millisecond. It is
	// set exactly when CurrentSong is, and may exceed its duration_secs while
	// the next song is pending.
	ElapsedSecs *float64 `json:"elapsed_secs,omitempty"`
	QueueLength int      `json:"queue_length"`
	TotalSongs  int      `json:"total_songs"`
	// NextSong is the first playable queued song, absent when none is queued.
	NextSong *Song `json:"next_song,omitempty"`
}
