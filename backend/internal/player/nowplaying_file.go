package player

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	"lofi-radio-backend/internal/atomicfile"
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

// Render formats song with the file's format; see FormatNowPlaying.
func (f *NowPlayingFile) Render(song *models.Song) string {
	return FormatNowPlaying(f.format, song)
}

// OneLine replaces every control character (CR, LF, tab, NUL, ESC, C1 codes
// such as NEL) and the Unicode line and paragraph separators with a space, so
// text cannot split the one-line OBS text or a chat bot reply.
func OneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, s)
}

// FormatNowPlaying renders song with format's {title}, {artist} and {album}
// placeholders. A nil song renders "". A song without an artist (empty, or
// only spaces and control characters) renders just its title, so the format's
// separator never dangles. Control characters become spaces (see OneLine), so
// the result is one line.
func FormatNowPlaying(format string, song *models.Song) string {
	if song == nil {
		return ""
	}
	if strings.TrimSpace(OneLine(song.Artist)) == "" {
		return OneLine(song.Title)
	}
	return OneLine(strings.NewReplacer(
		"{title}", song.Title,
		"{artist}", song.Artist,
		"{album}", song.Album,
	).Replace(format))
}

// Write replaces the file with the rendered song atomically (see atomicfile),
// so OBS never reads a half-written title. The file is world-readable (0644)
// because OBS may run as another user.
func (f *NowPlayingFile) Write(song *models.Song) error {
	if err := atomicfile.Write(f.path, ".nowplaying-*.tmp", 0o644, func(w io.Writer) error {
		_, err := io.WriteString(w, f.Render(song))
		return err
	}); err != nil {
		return fmt.Errorf("now-playing file: %w", err)
	}
	return nil
}
