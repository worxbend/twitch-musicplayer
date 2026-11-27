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
//
// announceMu orders a state change together with its broadcast (see
// StartSong, RetagSong and ClearAnd). It is separate from mu because the
// change hook runs outside mu and may call back in.
//
// startSeq counts song starts. A retag replaces currentSong but keeps
// startSeq and currentStartedAt, so it tells a new start from a metadata
// update and does not reset the elapsed time.
type StateTracker struct {
	announceMu       sync.Mutex
	mu               sync.RWMutex
	currentSong      *models.Song
	currentStartedAt time.Time // zero when no song is current
	startSeq         uint64
	history          []PlayEntry // oldest first, at most MaxHistory entries
	now              func() time.Time
	onChange         func(*models.Song)
}

func NewStateTracker() *StateTracker { return &StateTracker{now: time.Now} }

// OnSongChange registers fn to run after every SetCurrentSong, every
// UpdateSong that changed the song and every ClearCurrentSong that cleared
// it, replacing any earlier hook. fn runs
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
	runHook(t.setCurrent(song), song)
}

// ClearCurrentSong marks that nothing is playing, for example when the queue
// runs dry after a song finished. History is left unchanged. It returns false
// and skips the change hook when no song was set, so repeated clears do not
// rewrite or rebroadcast anything; otherwise the hook runs once with nil
// outside the lock.
func (t *StateTracker) ClearCurrentSong() bool {
	hook, ok := t.clear()
	if ok {
		runHook(hook, nil)
	}
	return ok
}

// UpdateSong replaces the current song's metadata when song has the same ID,
// for example after a retag. It does not add a history entry. The newest
// history entry is updated too when it is the same song. It returns whether
// anything changed, and runs the change hook outside the lock when it did.
func (t *StateTracker) UpdateSong(song *models.Song) bool {
	updated, _, hook, ok := t.update(song)
	if ok {
		runHook(hook, &updated)
	}
	return ok
}

// StartSong is SetCurrentSong followed by announce, with no other StartSong,
// RetagSong or ClearAnd change or announce in between. Announce callbacks
// broadcast the change; because they run in the order the changes were made,
// clients never receive an older song after a newer one. announce must not
// block and must not call StartSong, RetagSong or ClearAnd. A nil announce is
// skipped. The change hook runs after announce, outside both locks.
func (t *StateTracker) StartSong(song *models.Song, announce func()) {
	t.announceMu.Lock()
	hook := t.setCurrent(song)
	if announce != nil {
		announce()
	}
	t.announceMu.Unlock()
	runHook(hook, song)
}

// RetagSong is UpdateSong followed, when it changed the song, by announce with
// the updated copy and the time elapsed since the song started, ordered and
// constrained like StartSong. A retag keeps the original start, so elapsed is
// never reset. A retag of a song that is no longer current returns false and
// announces nothing.
func (t *StateTracker) RetagSong(song *models.Song, announce func(models.Song, time.Duration)) bool {
	t.announceMu.Lock()
	updated, elapsed, hook, ok := t.update(song)
	if ok && announce != nil {
		announce(updated, elapsed)
	}
	t.announceMu.Unlock()
	if ok {
		runHook(hook, &updated)
	}
	return ok
}

// ClearAnd is ClearCurrentSong followed, when it cleared a song, by announce,
// ordered and constrained like StartSong.
func (t *StateTracker) ClearAnd(announce func()) bool {
	return t.clearAnd(t.clear, announce)
}

// ClearIfStartAnd is ClearAnd limited to the song start numbered seq, as
// returned by CurrentStart. It clears nothing and returns false once a newer
// song started; a retag of the same start still clears. A delayed clear uses
// it so it cannot blank a song that started after it was scheduled.
func (t *StateTracker) ClearIfStartAnd(seq uint64, announce func()) bool {
	return t.clearAnd(func() (func(*models.Song), bool) { return t.clearStart(seq) }, announce)
}

// clearAnd runs clear and, when it cleared a song, announce under announceMu,
// then the change hook outside both locks.
func (t *StateTracker) clearAnd(clear func() (func(*models.Song), bool), announce func()) bool {
	t.announceMu.Lock()
	hook, ok := clear()
	if ok && announce != nil {
		announce()
	}
	t.announceMu.Unlock()
	if ok {
		runHook(hook, nil)
	}
	return ok
}

func runHook(hook func(*models.Song), song *models.Song) {
	if hook != nil {
		hook(song)
	}
}

// setCurrent applies SetCurrentSong under mu and returns the change hook.
func (t *StateTracker) setCurrent(song *models.Song) func(*models.Song) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.currentSong = song
	t.currentStartedAt = time.Time{}
	if song != nil {
		t.startSeq++
		t.currentStartedAt = t.now()
		t.history = append(t.history, PlayEntry{Song: *song, StartedAt: t.currentStartedAt})
		if excess := len(t.history) - MaxHistory; excess > 0 {
			t.history = t.history[excess:]
		}
	}
	return t.onChange
}

// clear applies ClearCurrentSong under mu and returns the change hook and
// whether a song was cleared.
func (t *StateTracker) clear() (func(*models.Song), bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.clearLocked()
}

// clearStart is clear limited to the song start numbered seq.
func (t *StateTracker) clearStart(seq uint64) (func(*models.Song), bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.startSeq != seq {
		return nil, false
	}
	return t.clearLocked()
}

// clearLocked clears the current song. The caller must hold mu.
func (t *StateTracker) clearLocked() (func(*models.Song), bool) {
	if t.currentSong == nil {
		return nil, false
	}
	t.currentSong = nil
	t.currentStartedAt = time.Time{}
	return t.onChange, true
}

// update applies UpdateSong under mu and returns the stored copy, the time
// elapsed since its start, the change hook and whether anything changed.
func (t *StateTracker) update(song *models.Song) (models.Song, time.Duration, func(*models.Song), bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if song == nil || t.currentSong == nil || t.currentSong.ID != song.ID {
		return models.Song{}, 0, nil, false
	}
	updated := *song
	t.currentSong = &updated
	if last := len(t.history) - 1; last >= 0 && t.history[last].Song.ID == song.ID {
		t.history[last].Song = updated
	}
	return updated, t.elapsedLocked(), t.onChange, true
}

// elapsedLocked is the time since the current song started. The caller must
// hold mu and have checked that a song is current.
func (t *StateTracker) elapsedLocked() time.Duration {
	return t.now().Sub(t.currentStartedAt)
}

func (t *StateTracker) GetCurrentSong() *models.Song {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.currentSong
}

// CurrentStart returns the current song, nil when none, and the number of
// its start for ClearIfStartAnd.
func (t *StateTracker) CurrentStart() (*models.Song, uint64) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.currentSong, t.startSeq
}

// CurrentElapsed returns the current song, nil when none, and the time since
// it started. Elapsed is 0 when no song is current. It is measured from the
// start recorded by SetCurrentSong or StartSong, so it may run past the
// song's duration while the next song is pending.
func (t *StateTracker) CurrentElapsed() (*models.Song, time.Duration) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.currentSong == nil {
		return nil, 0
	}
	return t.currentSong, t.elapsedLocked()
}

// ElapsedSecs converts elapsed to seconds rounded to the millisecond, as the
// elapsed_secs field of the state and now_playing payloads. A negative
// elapsed (clock step back) is reported as 0.
func ElapsedSecs(elapsed time.Duration) *float64 {
	secs := max(elapsed, 0).Round(time.Millisecond).Seconds()
	return &secs
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
