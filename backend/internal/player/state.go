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
	onChange    func(*models.Song)
}

func NewStateTracker() *StateTracker { return &StateTracker{now: time.Now} }

// OnSongChange registers fn to run after every SetCurrentSong and every
// UpdateSong that changed the song, replacing any earlier hook. fn runs
// outside the tracker's lock, so it may call back in.
func (t *StateTracker) OnSongChange(fn func(*models.Song)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onChange = fn
}

// SetCurrentSong sets the current song and, when song is non-nil, records the
// start in the play history. The change hook, if any, then runs once with song
// (nil included) after the lock is released.
func (t *StateTracker) SetCurrentSong(song *models.Song) {
	t.mu.Lock()
	t.currentSong = song
	if song != nil {
		t.history = append(t.history, PlayEntry{Song: *song, StartedAt: t.now()})
		if excess := len(t.history) - MaxHistory; excess > 0 {
			t.history = t.history[excess:]
		}
	}
	onChange := t.onChange
	t.mu.Unlock()

	if onChange != nil {
		onChange(song)
	}
}

// UpdateSong replaces the current song's metadata when song has the same ID,
// for example after a retag. It does not add a history entry. The newest
// history entry is updated too when it is the same song. It returns whether
// anything changed, and runs the change hook outside the lock when it did.
func (t *StateTracker) UpdateSong(song *models.Song) bool {
	t.mu.Lock()
	if song == nil || t.currentSong == nil || t.currentSong.ID != song.ID {
		t.mu.Unlock()
		return false
	}
	updated := *song
	t.currentSong = &updated
	if last := len(t.history) - 1; last >= 0 && t.history[last].Song.ID == song.ID {
		t.history[last].Song = updated
	}
	onChange := t.onChange
	t.mu.Unlock()

	if onChange != nil {
		onChange(&updated)
	}
	return true
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
