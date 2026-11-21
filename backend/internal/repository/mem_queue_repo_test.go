package repository

import (
	"context"
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
