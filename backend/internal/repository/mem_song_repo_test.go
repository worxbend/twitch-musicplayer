package repository

import (
	"context"
	"database/sql"
	"errors"
	"slices"
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

func songPaths(songs []*models.Song) []string {
	paths := make([]string, len(songs))
	for i, s := range songs {
		paths[i] = s.Path
	}
	return paths
}

func TestMemSongRepoListOrdersByTitleArtistPath(t *testing.T) {
	ctx := context.Background()
	repo := NewMemSongRepo()
	for _, s := range []*models.Song{
		{Title: "Same", Artist: "A", Path: "/z.mp3"},
		{Title: "Same", Artist: "A", Path: "/m.mp3"},
		{Title: "Same", Artist: "A", Path: "/a.mp3"},
		{Title: "Same", Artist: "B", Path: "/0.mp3"},
		{Title: "Alpha", Artist: "Z", Path: "/q.mp3"},
	} {
		s.ID = "id:" + s.Path
		if err := repo.Create(ctx, s); err != nil {
			t.Fatalf("create %s: %v", s.Path, err)
		}
	}

	want := []string{"/q.mp3", "/a.mp3", "/m.mp3", "/z.mp3", "/0.mp3"}
	for i := range 20 {
		list, err := repo.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if got := songPaths(list); !slices.Equal(got, want) {
			t.Fatalf("List run %d = %v, want %v", i, got, want)
		}
	}
}

func TestMemSongRepoSearch(t *testing.T) {
	ctx := context.Background()
	repo := NewMemSongRepo()
	for _, s := range []*models.Song{
		{Path: "/m/3.mp3", Filename: "3.mp3", Title: "Rain", Artist: "Kai", Album: "Night"},
		{Path: "/m/2.mp3", Filename: "2.mp3", Title: "Rain", Artist: "Kai", Album: "Night"},
		{Path: "/m/1.mp3", Filename: "1.mp3", Title: "Snow", Artist: "Lune", Album: "Winter"},
		{Path: "/m/tape.mp3", Filename: "Tape.mp3", Title: "Dust", Artist: "Moss", Album: "Attic"},
	} {
		s.ID = "id:" + s.Path
		if err := repo.Create(ctx, s); err != nil {
			t.Fatalf("create %s: %v", s.Path, err)
		}
	}

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"title", "rAIn", []string{"/m/2.mp3", "/m/3.mp3"}},
		{"artist", "LUNE", []string{"/m/1.mp3"}},
		{"album", "attic", []string{"/m/tape.mp3"}},
		{"filename", "tape", []string{"/m/tape.mp3"}},
		{"empty query returns all", "", []string{"/m/tape.mp3", "/m/2.mp3", "/m/3.mp3", "/m/1.mp3"}},
		{"no match", "jazz", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repo.Search(ctx, tt.query)
			if err != nil {
				t.Fatalf("Search(%q): %v", tt.query, err)
			}
			if paths := songPaths(got); !slices.Equal(paths, tt.want) {
				t.Fatalf("Search(%q) = %v, want %v", tt.query, paths, tt.want)
			}
		})
	}
}

func TestMemSongRepoListReturnsCopies(t *testing.T) {
	ctx := context.Background()
	repo := seedSongs(t, "/m/a.mp3")

	list, _ := repo.List(ctx)
	list[0].Title = "Mutated"

	if got, _ := repo.GetByID(ctx, "id:/m/a.mp3"); got.Title != "/m/a.mp3" {
		t.Fatalf("GetByID title = %q after mutating List result, want /m/a.mp3", got.Title)
	}
	if again, _ := repo.List(ctx); again[0].Title != "/m/a.mp3" {
		t.Fatalf("List title = %q after mutating List result, want /m/a.mp3", again[0].Title)
	}
}

func TestMemSongRepoCreateSameIDDoesNotDuplicate(t *testing.T) {
	ctx := context.Background()
	repo := seedSongs(t, "/m/a.mp3")

	if err := repo.Create(ctx, &models.Song{ID: "id:/m/a.mp3", Path: "/m/a.mp3", Title: "New"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	list, _ := repo.List(ctx)
	if len(list) != 1 || list[0].Title != "New" {
		t.Fatalf("List = %v, want one song titled New", list)
	}
	if n, _ := repo.Count(ctx); n != 1 {
		t.Fatalf("Count = %d, want 1", n)
	}
	if ok, _ := repo.ExistsByPath(ctx, "/m/a.mp3"); !ok {
		t.Fatal("ExistsByPath after same-path re-create = false, want true")
	}
}

func TestMemSongRepoCreateSameIDNewPathDropsOldPath(t *testing.T) {
	ctx := context.Background()
	repo := NewMemSongRepo()
	for _, p := range []string{"/m/x.mp3", "/m/y.mp3"} {
		if err := repo.Create(ctx, &models.Song{ID: "a", Path: p}); err != nil {
			t.Fatalf("Create %s: %v", p, err)
		}
	}

	if ok, _ := repo.ExistsByPath(ctx, "/m/x.mp3"); ok {
		t.Fatal("ExistsByPath old path = true, want false")
	}
	if ok, _ := repo.ExistsByPath(ctx, "/m/y.mp3"); !ok {
		t.Fatal("ExistsByPath new path = false, want true")
	}
	list, _ := repo.List(ctx)
	if len(list) != 1 || list[0].Path != "/m/y.mp3" {
		t.Fatalf("List = %v, want one song at /m/y.mp3", list)
	}
	if _, found, _ := repo.DeleteByPath(ctx, "/m/x.mp3"); found {
		t.Fatal("DeleteByPath old path found = true, want false")
	}
	if n, _ := repo.Count(ctx); n != 1 {
		t.Fatalf("Count = %d after DeleteByPath old path, want 1", n)
	}
	if _, found, _ := repo.DeleteByPath(ctx, "/m/y.mp3"); !found {
		t.Fatal("DeleteByPath new path found = false, want true")
	}
	if n, _ := repo.Count(ctx); n != 0 {
		t.Fatalf("Count = %d after DeleteByPath new path, want 0", n)
	}
}

func TestMemSongRepoCreateNewPathKeepsPathClaimedByOtherID(t *testing.T) {
	ctx := context.Background()
	repo := NewMemSongRepo()
	for _, s := range []models.Song{
		{ID: "a", Path: "/m/x.mp3"},
		{ID: "b", Path: "/m/x.mp3"},
		{ID: "a", Path: "/m/y.mp3"},
	} {
		if err := repo.Create(ctx, &s); err != nil {
			t.Fatalf("Create %s %s: %v", s.ID, s.Path, err)
		}
	}

	if ok, _ := repo.ExistsByPath(ctx, "/m/x.mp3"); !ok {
		t.Fatal("ExistsByPath claimed path = false, want true")
	}
	got, found, _ := repo.DeleteByPath(ctx, "/m/x.mp3")
	if !found || got.ID != "b" {
		t.Fatalf("DeleteByPath claimed path = %v, found %v; want song b", got, found)
	}
}

func TestMemSongRepoGetByIDUnknown(t *testing.T) {
	repo := seedSongs(t, "/m/a.mp3")
	if _, err := repo.GetByID(context.Background(), "id:/m/missing.mp3"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetByID unknown err = %v, want sql.ErrNoRows", err)
	}
}

func TestMemSongRepoExistsByPathAndCount(t *testing.T) {
	ctx := context.Background()
	repo := seedSongs(t, "/m/a.mp3", "/m/b.mp3", "/m/c.mp3")

	if ok, _ := repo.ExistsByPath(ctx, "/m/a.mp3"); !ok {
		t.Fatal("ExistsByPath seeded = false, want true")
	}
	if ok, _ := repo.ExistsByPath(ctx, "/m/missing.mp3"); ok {
		t.Fatal("ExistsByPath unknown = true, want false")
	}
	if n, _ := repo.Count(ctx); n != 3 {
		t.Fatalf("Count = %d after 3 creates, want 3", n)
	}
	if _, found, _ := repo.DeleteByPath(ctx, "/m/b.mp3"); !found {
		t.Fatal("DeleteByPath /m/b.mp3 found = false")
	}
	if n, _ := repo.Count(ctx); n != 2 {
		t.Fatalf("Count = %d after delete, want 2", n)
	}
	if ok, _ := repo.ExistsByPath(ctx, "/m/b.mp3"); ok {
		t.Fatal("ExistsByPath deleted = true, want false")
	}
}
