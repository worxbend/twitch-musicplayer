package main

import (
	"os"
	"path/filepath"
	"testing"

	"lofi-radio-backend/internal/config"
	"lofi-radio-backend/internal/models"
	"lofi-radio-backend/internal/player"
)

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "unknown"},
		{-1, "unknown"},
		{59.4, "0m59s"},
		{59.6, "1m00s"},
		{61, "1m01s"},
		{3600, "1h00m00s"},
		{3725, "1h02m05s"},
	}
	for _, c := range cases {
		if got := formatDuration(c.in); got != c.want {
			t.Errorf("formatDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{-5, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536 * 1024, "1.5 MiB"},
		{5 << 50, "5120.0 TiB"}, // units stop at TiB
	}
	for _, c := range cases {
		if got := formatBytes(c.in); got != c.want {
			t.Errorf("formatBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatExtensions(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ".mp3"},
		{[]string{}, ".mp3"},
		{[]string{".mp3", ".flac"}, ".mp3,.flac"},
	}
	for _, c := range cases {
		if got := formatExtensions(c.in); got != c.want {
			t.Errorf("formatExtensions(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSummarizeSongs(t *testing.T) {
	songs := []*models.Song{
		{DurationSecs: 60.5, SizeBytes: 1000},
		nil,
		{DurationSecs: 120, SizeBytes: 2500},
	}
	if d, b := summarizeSongs(songs); d != 180.5 || b != 3500 {
		t.Errorf("summarizeSongs = (%v, %d), want (180.5, 3500)", d, b)
	}
	if d, b := summarizeSongs(nil); d != 0 || b != 0 {
		t.Errorf("summarizeSongs(nil) = (%v, %d), want (0, 0)", d, b)
	}
}

const testNowPlayingFormat = "{artist} - {title}"

// newNowPlayingSetup returns a tracker wired to a now-playing file that
// already holds a stale title, and the file's path.
func newNowPlayingSetup(t *testing.T) (*player.StateTracker, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "np.txt")
	if err := os.WriteFile(path, []byte("old title"), 0o644); err != nil {
		t.Fatal(err)
	}
	tracker := player.NewStateTracker()
	setupNowPlayingFile(tracker, config.OBSConfig{NowPlayingFile: path, NowPlayingFormat: testNowPlayingFormat})
	return tracker, path
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("file content = %q, want %q", got, want)
	}
}

func TestSetupNowPlayingFileClearsStaleTitle(t *testing.T) {
	_, path := newNowPlayingSetup(t)
	assertFileContent(t, path, "")
}

// The tracker runs its change hook synchronously, so each write has landed
// by the time the tracker method returns.
func TestSetupNowPlayingFileFollowsTracker(t *testing.T) {
	tracker, path := newNowPlayingSetup(t)

	tracker.SetCurrentSong(&models.Song{ID: "s1", Artist: "A", Title: "T"})
	assertFileContent(t, path, "A - T")

	if !tracker.UpdateSong(&models.Song{ID: "s1", Artist: "A", Title: "T2"}) {
		t.Fatal("UpdateSong did not apply the retag")
	}
	assertFileContent(t, path, "A - T2")

	if !tracker.ClearCurrentSong() {
		t.Fatal("ClearCurrentSong reported nothing to clear")
	}
	assertFileContent(t, path, "")
}
