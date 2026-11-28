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

// newTestQueue returns a repo holding one item per id, in order.
func newTestQueue(t *testing.T, ids ...string) *MemQueueRepo {
	t.Helper()
	repo := NewMemQueueRepo(NewMemSongRepo())
	for _, id := range ids {
		if err := repo.Enqueue(context.Background(), &models.QueueItem{ID: id, SongID: "song-" + id}); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}
	return repo
}

func queueIDs(t *testing.T, r *MemQueueRepo) []string {
	t.Helper()
	items, err := r.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	return ids
}

func queueCount(t *testing.T, r *MemQueueRepo) int {
	t.Helper()
	n, err := r.Count(context.Background())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// tailIsNil fails when a slot past len(r.items) still holds an item, which
// would keep a vacated entry reachable until the next reallocation.
func tailIsNil(t *testing.T, r *MemQueueRepo) {
	t.Helper()
	for i, item := range r.items[len(r.items):cap(r.items)] {
		if item != nil {
			t.Fatalf("vacated slot %d still holds item %q", len(r.items)+i, item.ID)
		}
	}
}

func TestMemQueueRepoDequeue(t *testing.T) {
	ctx := context.Background()
	repo := newTestQueue(t, "A", "B", "C")

	for _, want := range []string{"A", "B", "C"} {
		backing := repo.items
		got, err := repo.Dequeue(ctx)
		if err != nil || got == nil || got.ID != want {
			t.Fatalf("Dequeue = (%+v, %v), want item %s", got, err, want)
		}
		if backing[0] != nil {
			t.Fatalf("dequeued slot still holds item %q", backing[0].ID)
		}
	}

	got, err := repo.Dequeue(ctx)
	if !errors.Is(err, sql.ErrNoRows) || got != nil {
		t.Fatalf("Dequeue on empty = (%+v, %v), want (nil, sql.ErrNoRows)", got, err)
	}
}

func TestMemQueueRepoEnqueueFrontDequeuesFirst(t *testing.T) {
	ctx := context.Background()
	repo := newTestQueue(t, "A", "B")
	if err := repo.EnqueueFront(ctx, &models.QueueItem{ID: "F", SongID: "song-F"}); err != nil {
		t.Fatalf("enqueue front: %v", err)
	}

	got, err := repo.Dequeue(ctx)
	if err != nil || got.ID != "F" {
		t.Fatalf("Dequeue = (%+v, %v), want item F", got, err)
	}
	if order := queueIDs(t, repo); !slices.Equal(order, []string{"A", "B"}) {
		t.Fatalf("order = %v, want [A B]", order)
	}
}

func TestMemQueueRepoRemove(t *testing.T) {
	tests := []struct {
		name      string
		id        string
		wantErr   error
		wantOrder []string
	}{
		{name: "head", id: "A", wantOrder: []string{"B", "C"}},
		{name: "middle", id: "B", wantOrder: []string{"A", "C"}},
		{name: "tail", id: "C", wantOrder: []string{"A", "B"}},
		{name: "unknown id", id: "Z", wantErr: sql.ErrNoRows, wantOrder: []string{"A", "B", "C"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newTestQueue(t, "A", "B", "C")

			if err := repo.Remove(context.Background(), tt.id); !errors.Is(err, tt.wantErr) {
				t.Fatalf("Remove(%q) = %v, want %v", tt.id, err, tt.wantErr)
			}
			if order := queueIDs(t, repo); !slices.Equal(order, tt.wantOrder) {
				t.Fatalf("order = %v, want %v", order, tt.wantOrder)
			}
			tailIsNil(t, repo)
		})
	}
}

func TestMemQueueRepoClear(t *testing.T) {
	ctx := context.Background()
	repo := newTestQueue(t, "A", "B", "C")

	if err := repo.Clear(ctx); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if n := queueCount(t, repo); n != 0 {
		t.Fatalf("Count after Clear = %d, want 0", n)
	}
	tailIsNil(t, repo)
	if got, err := repo.Dequeue(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Dequeue after Clear = (%+v, %v), want sql.ErrNoRows", got, err)
	}
}

func TestMemQueueRepoCount(t *testing.T) {
	ctx := context.Background()
	repo := newTestQueue(t)
	if n := queueCount(t, repo); n != 0 {
		t.Fatalf("Count on new repo = %d, want 0", n)
	}

	for _, id := range []string{"A", "B", "C"} {
		if err := repo.Enqueue(ctx, &models.QueueItem{ID: id, SongID: "song-" + id}); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}
	if n := queueCount(t, repo); n != 3 {
		t.Fatalf("Count after 3 Enqueue = %d, want 3", n)
	}
	if err := repo.Remove(ctx, "B"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if n := queueCount(t, repo); n != 2 {
		t.Fatalf("Count after Remove = %d, want 2", n)
	}
	if _, err := repo.Dequeue(ctx); err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if n := queueCount(t, repo); n != 1 {
		t.Fatalf("Count after Dequeue = %d, want 1", n)
	}
}
