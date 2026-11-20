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
