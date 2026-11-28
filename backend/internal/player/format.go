package player

import "strings"

// HasNowPlayingPlaceholder reports whether format contains {title} or
// {artist}. It is the one rule shared by config (obs.now_playing_format) and
// the API (?format= on GET /v1/now-playing.txt): a format without either
// placeholder would never show the song.
func HasNowPlayingPlaceholder(format string) bool {
	return strings.Contains(format, "{title}") || strings.Contains(format, "{artist}")
}
