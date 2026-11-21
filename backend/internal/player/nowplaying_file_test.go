package player

import (
	"os"
	"path/filepath"
	"testing"

	"lofi-radio-backend/internal/models"
)

func TestNowPlayingFileRender(t *testing.T) {
	tests := []struct {
		name   string
		format string
		song   *models.Song
		want   string
	}{
		{name: "artist and title", format: "{artist} - {title}",
			song: &models.Song{Title: "Rain", Artist: "Nujabes"}, want: "Nujabes - Rain"},
		{name: "missing artist", format: "{artist} - {title}",
			song: &models.Song{Title: "Rain"}, want: "Rain"},
		{name: "album placeholder", format: "{title} ({album}) by {artist}",
			song: &models.Song{Title: "Rain", Artist: "Nujabes", Album: "Modal Soul"}, want: "Rain (Modal Soul) by Nujabes"},
		{name: "unicode", format: "♪ {artist} – {title}",
			song: &models.Song{Title: "夜の雨", Artist: "Ñandú"}, want: "♪ Ñandú – 夜の雨"},
		{name: "nil song", format: "{artist} - {title}", song: nil, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewNowPlayingFile("unused", tt.format).Render(tt.song)
			if got != tt.want {
				t.Errorf("Render = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatNowPlayingReplacesLineBreaks(t *testing.T) {
	song := &models.Song{Title: "Rain\r\nDrops", Artist: "Nu\njabes"}
	if got, want := FormatNowPlaying("{artist} - {title}", song), "Nu jabes - Rain  Drops"; got != want {
		t.Errorf("FormatNowPlaying = %q, want %q", got, want)
	}
	if got, want := FormatNowPlaying("{artist} - {title}", &models.Song{Title: "a\nb"}), "a b"; got != want {
		t.Errorf("FormatNowPlaying without artist = %q, want %q", got, want)
	}
}

func TestNowPlayingFileWriteReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nowplaying.txt")
	f := NewNowPlayingFile(path, "{artist} - {title}")

	for _, song := range []*models.Song{
		{Title: "First Song", Artist: "Long Artist Name"},
		{Title: "Two", Artist: "B"},
	} {
		if err := f.Write(song); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "B - Two" {
		t.Errorf("content = %q, want %q", got, "B - Two")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "nowplaying.txt" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("dir holds %v, want only nowplaying.txt", names)
	}
}

func TestNowPlayingFileWriteMissingDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "nowplaying.txt")
	if err := NewNowPlayingFile(path, "{title}").Write(&models.Song{Title: "x"}); err == nil {
		t.Fatal("Write succeeded, want error for missing directory")
	}
}
