package models

import "testing"

func TestFirstPlayable(t *testing.T) {
	dead := &QueueItem{ID: "q1", SongID: "gone"}
	live := &QueueItem{ID: "q2", SongID: "a", Song: &Song{ID: "a"}}
	later := &QueueItem{ID: "q3", SongID: "b", Song: &Song{ID: "b"}}
	tests := []struct {
		name  string
		items []*QueueItem
		want  *QueueItem
	}{
		{name: "nil slice"},
		{name: "all dead", items: []*QueueItem{dead, {ID: "q4", SongID: "gone2"}}},
		{name: "live head", items: []*QueueItem{live, later}, want: live},
		{name: "dead head skipped", items: []*QueueItem{dead, live, later}, want: live},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FirstPlayable(tt.items); got != tt.want {
				t.Fatalf("FirstPlayable = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestNextPlayableSong(t *testing.T) {
	dead := &QueueItem{ID: "q1", SongID: "gone"}
	live := &QueueItem{ID: "q2", SongID: "a", Song: &Song{ID: "a"}}
	if got := NextPlayableSong([]*QueueItem{dead, live}); got != live.Song {
		t.Fatalf("NextPlayableSong = %+v, want song a", got)
	}
	if got := NextPlayableSong([]*QueueItem{dead}); got != nil {
		t.Fatalf("NextPlayableSong(all dead) = %+v, want nil", got)
	}
}
