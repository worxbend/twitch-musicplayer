package repository

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"

	"lofi-radio-backend/internal/models"
)

func TestMemQueueRepoRemoveBySongID(t *testing.T) {
	ctx := context.Background()
	repo := NewMemQueueRepo(NewMemSongRepo())
	for i, songID := range []string{"A", "B", "A"} {
		item := &models.QueueItem{ID: string(rune('1' + i)), SongID: songID}
		if err := repo.Enqueue(ctx, item); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	n, err := repo.RemoveBySongID(ctx, "A")
	if err != nil || n != 2 {
		t.Fatalf("RemoveBySongID = (%d, %v), want (2, nil)", n, err)
	}
	items, _ := repo.List(ctx)
	if len(items) != 1 || items[0].SongID != "B" {
		t.Fatalf("queue = %v, want only song B", items)
	}
}

func TestMemQueueRepoMove(t *testing.T) {
	tests := []struct {
		name      string
		queue     []string
		id        string
		delta     int
		wantPos   int
		wantMoved bool
		wantErr   error
		wantOrder []string
	}{
		{name: "middle up", queue: []string{"A", "B", "C"}, id: "B", delta: -1, wantPos: 1, wantMoved: true, wantOrder: []string{"B", "A", "C"}},
		{name: "middle down", queue: []string{"A", "B", "C"}, id: "B", delta: 1, wantPos: 3, wantMoved: true, wantOrder: []string{"A", "C", "B"}},
		{name: "first up", queue: []string{"A", "B", "C"}, id: "A", delta: -1, wantPos: 1, wantOrder: []string{"A", "B", "C"}},
		{name: "last down", queue: []string{"A", "B", "C"}, id: "C", delta: 1, wantPos: 3, wantOrder: []string{"A", "B", "C"}},
		{name: "unknown id", queue: []string{"A", "B", "C"}, id: "Z", delta: 1, wantErr: sql.ErrNoRows, wantOrder: []string{"A", "B", "C"}},
		{name: "single up", queue: []string{"A"}, id: "A", delta: -1, wantPos: 1, wantOrder: []string{"A"}},
		{name: "single down", queue: []string{"A"}, id: "A", delta: 1, wantPos: 1, wantOrder: []string{"A"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			repo := NewMemQueueRepo(NewMemSongRepo())
			for _, id := range tt.queue {
				if err := repo.Enqueue(ctx, &models.QueueItem{ID: id, SongID: "song-" + id}); err != nil {
					t.Fatalf("enqueue: %v", err)
				}
			}

			pos, moved, err := repo.Move(ctx, tt.id, tt.delta)
			if !errors.Is(err, tt.wantErr) || pos != tt.wantPos || moved != tt.wantMoved {
				t.Fatalf("Move = (%d, %v, %v), want (%d, %v, %v)", pos, moved, err, tt.wantPos, tt.wantMoved, tt.wantErr)
			}
			items, _ := repo.List(ctx)
			order := make([]string, len(items))
			for i, item := range items {
				order[i] = item.ID
			}
			if !slices.Equal(order, tt.wantOrder) {
				t.Fatalf("order = %v, want %v", order, tt.wantOrder)
			}
		})
	}
}

// TestMemQueueRepoEnqueueStoresCopy checks the repo never aliases the caller's
// item, so later writes by the caller cannot race with List.
func TestMemQueueRepoEnqueueStoresCopy(t *testing.T) {
	ctx := context.Background()
	repo := NewMemQueueRepo(NewMemSongRepo())
	back := &models.QueueItem{ID: "1", SongID: "A"}
	front := &models.QueueItem{ID: "2", SongID: "B"}
	if err := repo.Enqueue(ctx, back); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := repo.EnqueueFront(ctx, front); err != nil {
		t.Fatalf("enqueue front: %v", err)
	}
	if back.Position != 1 || front.Position != 0 {
		t.Fatalf("positions = (%d, %d), want (1, 0) written back", back.Position, front.Position)
	}

	back.SongID = "changed"
	back.Song = &models.Song{ID: "changed"}
	front.SongID = "changed"

	items, _ := repo.List(ctx)
	if len(items) != 2 || items[0].SongID != "B" || items[1].SongID != "A" || items[1].Song != nil {
		t.Fatalf("queue = %+v, want [B A] unaffected by caller writes", items)
	}
}

func TestMemQueueRepoListReflectsRetaggedSong(t *testing.T) {
	ctx := context.Background()
	songs := NewMemSongRepo()
	repo := NewMemQueueRepo(songs)
	old := &models.Song{ID: "A", Path: "/m/a.mp3", Title: "Old"}
	if err := songs.Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := repo.Enqueue(ctx, &models.QueueItem{ID: "1", SongID: "A", Song: old}); err != nil {
		t.Fatal(err)
	}

	if err := songs.Create(ctx, &models.Song{ID: "A", Path: "/m/a.mp3", Title: "New"}); err != nil {
		t.Fatal(err)
	}

	items, _ := repo.List(ctx)
	if len(items) != 1 || items[0].Song == nil || items[0].Song.Title != "New" {
		t.Fatalf("queue = %+v, want song A titled New", items)
	}
}

func TestMemQueueRepoListDropsSongRemovedFromLibrary(t *testing.T) {
	ctx := context.Background()
	songs := NewMemSongRepo()
	repo := NewMemQueueRepo(songs)
	song := &models.Song{ID: "A", Path: "/m/a.mp3", Title: "Old"}
	if err := songs.Create(ctx, song); err != nil {
		t.Fatal(err)
	}
	if err := repo.Enqueue(ctx, &models.QueueItem{ID: "1", SongID: "A", Song: song}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := songs.DeleteByPath(ctx, "/m/a.mp3"); err != nil {
		t.Fatal(err)
	}

	items, _ := repo.List(ctx)
	if len(items) != 1 || items[0].Song != nil {
		t.Fatalf("queue = %+v, want one item with nil Song", items)
	}
}
