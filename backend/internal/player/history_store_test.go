package player

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"lofi-radio-backend/internal/models"
)

func writeHistoryFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "radio.history.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHistoryFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "radio.history.json")
	want := []PlayEntry{
		{Song: models.Song{ID: "a", Title: "A", Artist: "X"}, StartedAt: epoch},
		{Song: models.Song{ID: "b", Title: "B"}, StartedAt: epoch.Add(3 * time.Minute)},
	}
	if err := SaveHistory(path, want); err != nil {
		t.Fatalf("SaveHistory: %v", err)
	}
	got, err := LoadHistoryFile(path)
	if err != nil {
		t.Fatalf("LoadHistoryFile: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestSaveHistoryIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "radio.history.json")
	if err := SaveHistory(path, []PlayEntry{{Song: models.Song{ID: "a"}, StartedAt: epoch}}); err != nil {
		t.Fatalf("SaveHistory: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode = %o, want 600", mode)
	}
}

func TestLoadHistoryFileMissingIsEmpty(t *testing.T) {
	got, err := LoadHistoryFile(filepath.Join(t.TempDir(), "absent.history.json"))
	if err != nil || got != nil {
		t.Fatalf("LoadHistoryFile(missing) = %v, %v; want nil, nil", got, err)
	}
}

func TestLoadHistoryFileRejectsBadFiles(t *testing.T) {
	tests := map[string]string{
		"corrupt JSON":  `{"version":1,"entries":[`,
		"wrong version": `{"version":2,"entries":[]}`,
		"no version":    `{"entries":[]}`,
		"over the cap":  `{"version":1,"entries":[]}` + strings.Repeat(" ", maxHistoryFileBytes),
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := LoadHistoryFile(writeHistoryFixture(t, content))
			if err == nil {
				t.Fatalf("LoadHistoryFile = %v, nil; want an error", got)
			}
		})
	}
}
