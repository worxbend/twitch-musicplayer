package player

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"lofi-radio-backend/internal/models"
)

// NowPlayingFile writes the current song as plain text for an OBS Text source
// set to "Read from file".
type NowPlayingFile struct {
	path   string
	format string
}

// NewNowPlayingFile returns a writer for path. format accepts the {title},
// {artist} and {album} placeholders.
func NewNowPlayingFile(path, format string) *NowPlayingFile {
	return &NowPlayingFile{path: path, format: format}
}

// Render formats song. A nil song renders "". A song without an artist renders
// just its title, so the format's separator never dangles.
func (f *NowPlayingFile) Render(song *models.Song) string {
	if song == nil {
		return ""
	}
	if song.Artist == "" {
		return song.Title
	}
	return strings.NewReplacer(
		"{title}", song.Title,
		"{artist}", song.Artist,
		"{album}", song.Album,
	).Replace(f.format)
}

// Write replaces the file with the rendered song atomically: it writes and
// syncs a temp file in the same directory, then renames it over the target, so
// OBS never reads a half-written title.
func (f *NowPlayingFile) Write(song *models.Song) error {
	tmp, err := os.CreateTemp(filepath.Dir(f.path), ".nowplaying-*.tmp")
	if err != nil {
		return fmt.Errorf("now-playing file: create temp: %w", err)
	}
	if err := writeTextSynced(tmp, f.Render(song)); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("now-playing file: write temp: %w", err)
	}
	if err := os.Rename(tmp.Name(), f.path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("now-playing file: replace %s: %w", f.path, err)
	}
	return nil
}

// writeTextSynced writes text to tmp, makes it world-readable (CreateTemp
// uses 0600 and OBS may run as another user), syncs and closes it. tmp is
// always closed.
func writeTextSynced(tmp *os.File, text string) error {
	if _, err := tmp.WriteString(text); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	return tmp.Close()
}
