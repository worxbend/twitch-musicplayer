package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"lofi-radio-backend/internal/models"
)

func seedSongs(t *testing.T, paths ...string) *MemSongRepo {
	t.Helper()
	repo := NewMemSongRepo()
	for _, p := range paths {
		if err := repo.Create(context.Background(), &models.Song{ID: "id:" + p, Path: p, Title: p}); err != nil {
			t.Fatalf("create %s: %v", p, err)
		}
	}
	return repo
}

func TestMemSongRepoDeleteByPath(t *testing.T) {
	ctx := context.Background()
	repo := seedSongs(t, "/m/a.mp3", "/m/b.mp3")

	got, found, err := repo.DeleteByPath(ctx, "/m/a.mp3")
	if err != nil || !found {
		t.Fatalf("DeleteByPath = found %v, err %v; want found, nil", found, err)
	}
	if got.Path != "/m/a.mp3" {
		t.Fatalf("removed path = %q, want /m/a.mp3", got.Path)
	}
	if n, _ := repo.Count(ctx); n != 1 {
		t.Fatalf("Count = %d, want 1", n)
	}
	if _, err := repo.GetByID(ctx, got.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetByID removed = %v, want sql.ErrNoRows", err)
	}
	if ok, _ := repo.ExistsByPath(ctx, "/m/a.mp3"); ok {
		t.Fatal("ExistsByPath removed = true, want false")
	}
	list, _ := repo.List(ctx)
	if len(list) != 1 || list[0].Path != "/m/b.mp3" {
		t.Fatalf("List = %v, want only /m/b.mp3", list)
	}
}

func TestMemSongRepoDeleteByPathUnknown(t *testing.T) {
	repo := seedSongs(t, "/m/a.mp3")
	got, found, err := repo.DeleteByPath(context.Background(), "/m/missing.mp3")
	if err != nil || found || got != nil {
		t.Fatalf("DeleteByPath unknown = (%v, %v, %v), want (nil, false, nil)", got, found, err)
	}
}

func TestMemSongRepoDeleteUnderDirIsSeparatorAware(t *testing.T) {
	ctx := context.Background()
	repo := seedSongs(t, "/m/a/x.mp3", "/m/a/b/y.mp3", "/m/ab/z.mp3")

	removed, err := repo.DeleteUnderDir(ctx, "/m/a/")
	if err != nil {
		t.Fatalf("DeleteUnderDir: %v", err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed %d songs, want 2", len(removed))
	}
	list, _ := repo.List(ctx)
	if len(list) != 1 || list[0].Path != "/m/ab/z.mp3" {
		t.Fatalf("List = %v, want only /m/ab/z.mp3", list)
	}
	if ok, _ := repo.ExistsByPath(ctx, "/m/a/x.mp3"); ok {
		t.Fatal("ExistsByPath /m/a/x.mp3 = true after DeleteUnderDir")
	}
}

func TestMemSongRepoUpdateUnknownNeverInserts(t *testing.T) {
	ctx := context.Background()
	repo := NewMemSongRepo()

	err := repo.Update(ctx, &models.Song{ID: "id:/m/a.mp3", Path: "/m/a.mp3", Title: "Ghost"})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Update unknown err = %v, want sql.ErrNoRows", err)
	}
	if n, _ := repo.Count(ctx); n != 0 {
		t.Fatalf("Count = %d after Update unknown, want 0", n)
	}
	if ok, _ := repo.ExistsByPath(ctx, "/m/a.mp3"); ok {
		t.Fatal("ExistsByPath = true after Update unknown")
	}
}

func TestMemSongRepoUpdateReplacesInPlace(t *testing.T) {
	ctx := context.Background()
	repo := seedSongs(t, "/m/a.mp3", "/m/b.mp3")

	if err := repo.Update(ctx, &models.Song{ID: "id:/m/a.mp3", Path: "/m/a.mp3", Title: "Retagged"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := repo.GetByID(ctx, "id:/m/a.mp3")
	if err != nil || got.Title != "Retagged" {
		t.Fatalf("GetByID = (%v, %v), want title Retagged", got, err)
	}
	if want := []string{"id:/m/a.mp3", "id:/m/b.mp3"}; len(repo.order) != 2 || repo.order[0] != want[0] || repo.order[1] != want[1] {
		t.Fatalf("order = %v, want %v", repo.order, want)
	}
	if ok, _ := repo.ExistsByPath(ctx, "/m/a.mp3"); !ok {
		t.Fatal("ExistsByPath = false after Update")
	}
}

func TestMemSongRepoUpdateStoresCopy(t *testing.T) {
	ctx := context.Background()
	repo := seedSongs(t, "/m/a.mp3")
	song := &models.Song{ID: "id:/m/a.mp3", Path: "/m/a.mp3", Title: "Stored"}

	if err := repo.Update(ctx, song); err != nil {
		t.Fatalf("Update: %v", err)
	}
	song.Title = "Mutated"
	if got, _ := repo.GetByID(ctx, song.ID); got.Title != "Stored" {
		t.Fatalf("stored title = %q, want Stored", got.Title)
	}
}
