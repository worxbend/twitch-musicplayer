package player

import (
	"sync"
	"time"

	"lofi-radio-backend/internal/models"
)

// MaxHistory is how many song starts the tracker remembers.
const MaxHistory = 50

// PlayEntry records one song start. The song is stored by value so callers
// cannot mutate history through a shared pointer.
type PlayEntry struct {
	Song      models.Song `json:"song"`
	StartedAt time.Time   `json:"started_at"`
}

// StateTracker holds the currently playing song and a bounded play history,
// updated by the WebSocket layer when a new song starts and read by the HTTP
// player endpoints.
type StateTracker struct {
	mu          sync.RWMutex
	currentSong *models.Song
	history     []PlayEntry // oldest first, at most MaxHistory entries
	now         func() time.Time
}

func NewStateTracker() *StateTracker { return &StateTracker{now: time.Now} }

// SetCurrentSong sets the current song and, when song is non-nil, records the
// start in the play history.
func (t *StateTracker) SetCurrentSong(song *models.Song) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.currentSong = song
	if song == nil {
		return
	}
	t.history = append(t.history, PlayEntry{Song: *song, StartedAt: t.now()})
	if excess := len(t.history) - MaxHistory; excess > 0 {
		t.history = t.history[excess:]
	}
}

func (t *StateTracker) GetCurrentSong() *models.Song {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.currentSong
}

// History returns up to limit recorded starts, newest first, in a new slice.
// A limit <= 0 returns an empty slice.
func (t *StateTracker) History(limit int) []PlayEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	n := max(0, min(limit, len(t.history)))
	out := make([]PlayEntry, 0, n)
	for i := len(t.history) - 1; len(out) < n; i-- {
		out = append(out, t.history[i])
	}
	return out
}
