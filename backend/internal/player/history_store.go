package player

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"lofi-radio-backend/internal/atomicfile"
)

const historyFileVersion = 1

// maxHistoryFileBytes caps what LoadHistoryFile reads. MaxHistory entries
// take a few tens of KiB, so a larger file is not one SaveHistory wrote.
const maxHistoryFileBytes = 1 << 20

// historyFile is the on-disk JSON shape of the play history.
type historyFile struct {
	Version int         `json:"version"`
	Entries []PlayEntry `json:"entries"`
}

// SaveHistory replaces the file at path with entries, atomically and readable
// only by its owner.
func SaveHistory(path string, entries []PlayEntry) error {
	return atomicfile.Write(path, ".history-*", 0o600, func(w io.Writer) error {
		return json.NewEncoder(w).Encode(historyFile{Version: historyFileVersion, Entries: entries})
	})
}

// LoadHistoryFile reads the play history SaveHistory wrote to path. A missing
// file gives no entries and no error. A file that cannot be read, is larger
// than maxHistoryFileBytes, is not valid JSON or has another version returns
// an error.
func LoadHistoryFile(path string) ([]PlayEntry, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("history %s: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxHistoryFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("history %s: %w", path, err)
	}
	if len(data) > maxHistoryFileBytes {
		return nil, fmt.Errorf("history %s: larger than %d bytes", path, maxHistoryFileBytes)
	}
	var file historyFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("history %s: %w", path, err)
	}
	if file.Version != historyFileVersion {
		return nil, fmt.Errorf("history %s: unsupported version %d", path, file.Version)
	}
	return file.Entries, nil
}
