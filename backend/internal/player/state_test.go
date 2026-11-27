package player

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"lofi-radio-backend/internal/models"
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// newFakeClockTracker returns a tracker whose clock advances one second per call.
func newFakeClockTracker() *StateTracker {
	t := NewStateTracker()
	ticks := 0
	t.now = func() time.Time {
		ticks++
		return epoch.Add(time.Duration(ticks) * time.Second)
	}
	return t
}

func pushSongs(t *StateTracker, n int) {
	for i := range n {
		t.SetCurrentSong(&models.Song{ID: fmt.Sprintf("s%d", i)})
	}
}

func ids(entries []PlayEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Song.ID
	}
	return out
}

func TestSetCurrentSongNilIsNotRecorded(t *testing.T) {
	tr := newFakeClockTracker()
	tr.SetCurrentSong(nil)
	if got := tr.History(MaxHistory); len(got) != 0 {
		t.Fatalf("History = %v, want empty", ids(got))
	}
	pushSongs(tr, 1)
	tr.SetCurrentSong(nil)
	if tr.GetCurrentSong() != nil {
		t.Fatal("current song not cleared")
	}
	if got := tr.History(MaxHistory); len(got) != 1 {
		t.Fatalf("History = %v, want one entry", ids(got))
	}
}

func TestHistoryNewestFirst(t *testing.T) {
	tr := newFakeClockTracker()
	pushSongs(tr, 3)
	got := tr.History(MaxHistory)
	if fmt.Sprint(ids(got)) != "[s2 s1 s0]" {
		t.Fatalf("order = %v, want [s2 s1 s0]", ids(got))
	}
	if !got[0].StartedAt.Equal(epoch.Add(3 * time.Second)) {
		t.Fatalf("newest started_at = %v", got[0].StartedAt)
	}
}

func TestHistoryTrimsToMaxHistory(t *testing.T) {
	tr := newFakeClockTracker()
	pushSongs(tr, MaxHistory+5)
	got := tr.History(MaxHistory + 10)
	if len(got) != MaxHistory {
		t.Fatalf("len = %d, want %d", len(got), MaxHistory)
	}
	if got[0].Song.ID != "s54" || got[len(got)-1].Song.ID != "s5" {
		t.Fatalf("kept %s..%s, want s54..s5", got[0].Song.ID, got[len(got)-1].Song.ID)
	}
}

func TestHistoryLimit(t *testing.T) {
	tr := newFakeClockTracker()
	pushSongs(tr, 3)
	tests := []struct {
		limit int
		want  int
	}{{limit: 10, want: 3}, {limit: 2, want: 2}, {limit: 0, want: 0}, {limit: -1, want: 0}}
	for _, tt := range tests {
		got := tr.History(tt.limit)
		if got == nil || len(got) != tt.want {
			t.Fatalf("History(%d) = %v, want %d non-nil entries", tt.limit, got, tt.want)
		}
	}
}

func TestHistoryReturnsCopies(t *testing.T) {
	tr := newFakeClockTracker()
	song := &models.Song{ID: "a", Title: "Rain"}
	tr.SetCurrentSong(song)
	song.Title = "mutated via caller pointer"

	got := tr.History(1)
	got[0].Song.Title = "mutated via result"
	got[0] = PlayEntry{}

	again := tr.History(1)
	if again[0].Song.ID != "a" || again[0].Song.Title != "Rain" {
		t.Fatalf("history changed: %+v", again[0].Song)
	}
}

func TestStateTrackerConcurrentAccess(t *testing.T) {
	tr := NewStateTracker()
	var wg sync.WaitGroup
	for w := range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := range 100 {
				tr.SetCurrentSong(&models.Song{ID: fmt.Sprintf("w%d-%d", w, i)})
			}
		}()
		go func() {
			defer wg.Done()
			for range 100 {
				_ = tr.History(MaxHistory)
				_ = tr.GetCurrentSong()
			}
		}()
	}
	wg.Wait()
	if got := tr.History(MaxHistory + 1); len(got) != MaxHistory {
		t.Fatalf("len = %d, want %d", len(got), MaxHistory)
	}
}

func TestOnSongChangeFiresOncePerSet(t *testing.T) {
	tr := NewStateTracker()
	var got []*models.Song
	tr.OnSongChange(func(s *models.Song) { got = append(got, s) })

	a := &models.Song{ID: "a"}
	tr.SetCurrentSong(a)
	tr.SetCurrentSong(nil)

	if len(got) != 2 || got[0] != a || got[1] != nil {
		t.Fatalf("hook calls = %v, want [a nil]", got)
	}
}

func TestOnSongChangeMayCallBackIn(t *testing.T) {
	tr := NewStateTracker()
	calls := 0
	tr.OnSongChange(func(s *models.Song) {
		calls++
		if s != nil && s.ID == "first" {
			tr.SetCurrentSong(&models.Song{ID: "second"})
		}
	})

	done := make(chan struct{})
	go func() {
		tr.SetCurrentSong(&models.Song{ID: "first"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SetCurrentSong from inside the hook deadlocked")
	}
	if calls != 2 || tr.GetCurrentSong().ID != "second" {
		t.Fatalf("calls = %d, current = %v, want 2 and second", calls, tr.GetCurrentSong())
	}
}

// trackHook registers a change hook that records every song it is called with.
func trackHook(tr *StateTracker) *[]*models.Song {
	var calls []*models.Song
	tr.OnSongChange(func(s *models.Song) { calls = append(calls, s) })
	return &calls
}

func TestUpdateSongReplacesCurrentAndHistoryHead(t *testing.T) {
	tr := newFakeClockTracker()
	tr.SetCurrentSong(&models.Song{ID: "a", Title: "Old"})
	startedAt := tr.History(1)[0].StartedAt
	calls := trackHook(tr)

	if !tr.UpdateSong(&models.Song{ID: "a", Title: "New"}) {
		t.Fatal("UpdateSong = false, want true")
	}
	if got := tr.GetCurrentSong().Title; got != "New" {
		t.Fatalf("current title = %q, want New", got)
	}
	hist := tr.History(10)
	if len(hist) != 1 || hist[0].Song.Title != "New" || !hist[0].StartedAt.Equal(startedAt) {
		t.Fatalf("history = %+v, want one entry titled New started at %v", hist, startedAt)
	}
	if len(*calls) != 1 || (*calls)[0].Title != "New" {
		t.Fatalf("hook calls = %v, want one with title New", *calls)
	}
}

func TestUpdateSongIgnoresNonMatchingAndNil(t *testing.T) {
	tests := []struct {
		name    string
		current *models.Song
		update  *models.Song
	}{
		{name: "different id", current: &models.Song{ID: "a", Title: "Old"}, update: &models.Song{ID: "b", Title: "New"}},
		{name: "no current song", update: &models.Song{ID: "a", Title: "New"}},
		{name: "nil argument", current: &models.Song{ID: "a", Title: "Old"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := newFakeClockTracker()
			if tt.current != nil {
				tr.SetCurrentSong(tt.current)
			}
			before := tr.History(10)
			calls := trackHook(tr)

			if tr.UpdateSong(tt.update) {
				t.Fatal("UpdateSong = true, want false")
			}
			if got := tr.GetCurrentSong(); got != tt.current {
				t.Fatalf("current = %+v, want %+v", got, tt.current)
			}
			if after := tr.History(10); fmt.Sprint(after) != fmt.Sprint(before) {
				t.Fatalf("history = %+v, want %+v", after, before)
			}
			if len(*calls) != 0 {
				t.Fatalf("hook fired %d times, want 0", len(*calls))
			}
		})
	}
}

func TestUpdateSongCopiesArgument(t *testing.T) {
	tr := newFakeClockTracker()
	tr.SetCurrentSong(&models.Song{ID: "a", Title: "Old"})
	update := &models.Song{ID: "a", Title: "New"}
	tr.UpdateSong(update)
	update.Title = "mutated via caller pointer"

	if got := tr.GetCurrentSong().Title; got != "New" {
		t.Fatalf("current title = %q, want New", got)
	}
}

func TestUpdateSongLeavesHistoryWhenSongIsNotCurrent(t *testing.T) {
	tr := newFakeClockTracker()
	tr.SetCurrentSong(&models.Song{ID: "a", Title: "Old"})
	tr.SetCurrentSong(&models.Song{ID: "b", Title: "B"})
	before := tr.History(10)

	if tr.UpdateSong(&models.Song{ID: "a", Title: "New"}) {
		t.Fatal("UpdateSong = true, want false")
	}
	if after := tr.History(10); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("history = %+v, want %+v", after, before)
	}
}

func TestClearCurrentSongIsIdempotent(t *testing.T) {
	tr := newFakeClockTracker()
	pushSongs(tr, 1)
	var calls []*models.Song
	tr.OnSongChange(func(s *models.Song) { calls = append(calls, s) })

	if !tr.ClearCurrentSong() {
		t.Fatal("first ClearCurrentSong = false, want true")
	}
	if tr.ClearCurrentSong() {
		t.Fatal("second ClearCurrentSong = true, want false")
	}
	if len(calls) != 1 || calls[0] != nil {
		t.Fatalf("hook calls = %v, want exactly one with nil", calls)
	}
	if tr.GetCurrentSong() != nil {
		t.Fatal("current song not cleared")
	}
	if got := tr.History(10); fmt.Sprint(ids(got)) != "[s0]" {
		t.Fatalf("History = %v, want [s0]", ids(got))
	}
}

func TestRetagSongIgnoresNonCurrentSong(t *testing.T) {
	tr := NewStateTracker()
	tr.SetCurrentSong(&models.Song{ID: "b", Title: "B"})

	called := false
	ok := tr.RetagSong(&models.Song{ID: "a", Title: "A2"}, func(models.Song) { called = true })

	if ok || called {
		t.Fatalf("RetagSong(non-current) = %v, announced = %v; want false, false", ok, called)
	}
	if got := tr.GetCurrentSong(); got.ID != "b" || got.Title != "B" {
		t.Errorf("current = %+v, want b/B unchanged", got)
	}
}

func TestRetagSongAnnouncesUpdatedCopy(t *testing.T) {
	tr := NewStateTracker()
	tr.SetCurrentSong(&models.Song{ID: "a", Title: "old"})

	var announced models.Song
	ok := tr.RetagSong(&models.Song{ID: "a", Title: "new"}, func(s models.Song) { announced = s })

	if !ok || announced.ID != "a" || announced.Title != "new" {
		t.Fatalf("RetagSong = %v, announced %+v; want true, a/new", ok, announced)
	}
}

func TestClearAndOnEmptyTrackerDoesNotAnnounce(t *testing.T) {
	tr := NewStateTracker()

	called := false
	if tr.ClearAnd(func() { called = true }) || called {
		t.Fatalf("ClearAnd on empty tracker returned true or announced")
	}

	tr.SetCurrentSong(&models.Song{ID: "a"})
	if !tr.ClearAnd(func() { called = true }) || !called {
		t.Fatalf("ClearAnd with a current song did not clear and announce")
	}
}

func TestStartAndRetagAnnouncesStayOrdered(t *testing.T) {
	type announcement struct {
		retag bool
		id    string
	}
	tr := NewStateTracker()
	var (
		logMu sync.Mutex
		log   []announcement
	)
	record := func(a announcement) {
		runtime.Gosched() // widen the window between a change and its announce
		logMu.Lock()
		log = append(log, a)
		logMu.Unlock()
	}

	const workers, iterations = 8, 200
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for i := range iterations {
				if (w+i)%2 == 0 {
					id := fmt.Sprintf("s%d", (w*iterations+i)%5)
					tr.StartSong(&models.Song{ID: id}, func() { record(announcement{id: id}) })
					continue
				}
				cur := tr.GetCurrentSong()
				if cur == nil {
					continue
				}
				retag := *cur
				retag.Title = fmt.Sprintf("t%d-%d", w, i)
				tr.RetagSong(&retag, func(s models.Song) { record(announcement{retag: true, id: s.ID}) })
			}
		})
	}
	wg.Wait()

	if len(log) == 0 {
		t.Fatal("no announcements recorded")
	}
	if last, cur := log[len(log)-1], tr.GetCurrentSong(); cur == nil || last.id != cur.ID {
		t.Fatalf("last announcement %+v, current song %+v", last, cur)
	}
	started := ""
	for i, a := range log {
		if !a.retag {
			started = a.id
			continue
		}
		if a.id != started {
			t.Fatalf("announcement %d retags %q after %q started", i, a.id, started)
		}
	}
}
