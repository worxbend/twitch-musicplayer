package player

import (
	"fmt"
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
