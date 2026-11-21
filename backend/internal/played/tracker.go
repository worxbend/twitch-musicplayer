package played

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/bits-and-blooms/bloom/v3"
)

// Tracker tracks which songs have been played using an in-memory Bloom filter.
// It is thread-safe. Bloom filters may have false positives but no false negatives —
// a song that was played will always be reported as played; an unplayed song may
// rarely be reported as played (at the configured false-positive rate).
type Tracker struct {
	mu     sync.RWMutex
	f      *bloom.BloomFilter
	cap    uint
	fpRate float64
}

// New creates a Tracker sized for n estimated items at the given false-positive rate.
func New(n uint, fpRate float64) *Tracker {
	return &Tracker{
		f:      bloom.NewWithEstimates(n, fpRate),
		cap:    n,
		fpRate: fpRate,
	}
}

// MarkPlayed records that songID has been played.
func (t *Tracker) MarkPlayed(songID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.f.AddString(songID)
}

// HasPlayed reports whether songID has been played.
func (t *Tracker) HasPlayed(songID string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.f.TestString(songID)
}

// Reset clears all play records, beginning a new cycle.
func (t *Tracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.f = bloom.NewWithEstimates(t.cap, t.fpRate)
}

// ApproxCount returns the estimated number of distinct songs played since last Reset.
func (t *Tracker) ApproxCount() uint32 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.f.ApproximatedSize()
}

// Save persists the filter to path atomically: it writes and syncs a temp file
// in the same directory, then renames it over path. A crash mid-write leaves the
// previous file intact.
func (t *Tracker) Save(path string) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	tmp, err := os.CreateTemp(filepath.Dir(path), ".bloom-*")
	if err != nil {
		return err
	}
	if err := writeSynced(tmp, t.f); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// writeSynced writes f to tmp, syncs it, and closes it. tmp is always closed.
func writeSynced(tmp *os.File, f *bloom.BloomFilter) error {
	if _, err := f.WriteTo(tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	return tmp.Close()
}

// Load restores the filter from path. Returns nil if path does not exist.
// On a read error the in-memory filter is left unchanged.
func (t *Tracker) Load(path string) error {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	f := &bloom.BloomFilter{}
	if _, err := f.ReadFrom(file); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.f = f
	return nil
}
