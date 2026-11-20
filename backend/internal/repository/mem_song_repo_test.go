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
