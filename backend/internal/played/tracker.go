package played

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

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
	dirty  bool // filter changed since the last successful SaveIfDirty or Load
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
	t.dirty = true
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
	t.dirty = true
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
	return t.saveLocked(path)
}

// SaveIfDirty saves the filter to path only if it changed since the last
// successful SaveIfDirty or Load. It reports whether it wrote the file. On error
// the tracker stays dirty so the next call retries.
//
// It holds the write lock for the whole write so a concurrent MarkPlayed cannot
// slip in between the save and the dirty reset. The filter is small, so the
// pause for callers is short.
func (t *Tracker) SaveIfDirty(path string) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.dirty {
		return false, nil
	}
	if err := t.saveLocked(path); err != nil {
		return false, err
	}
	t.dirty = false
	return true, nil
}

// RunAutosave calls SaveIfDirty every interval until ctx is done, so a crash or
// SIGKILL loses at most one interval of play history. Failures go to logf;
// successful saves are not logged.
func (t *Tracker) RunAutosave(ctx context.Context, path string, every time.Duration, logf func(string, ...any)) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := t.SaveIfDirty(path); err != nil {
				logf("warning: bloom autosave failed: %v", err)
			}
		}
	}
}

// saveLocked writes the filter to path atomically. The caller holds t.mu
// (read or write).
func (t *Tracker) saveLocked(path string) error {
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
// A file that fails validateBloomFile or decoding returns an error and leaves
// the in-memory filter unchanged.
func (t *Tracker) Load(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := validateBloomFile(data); err != nil {
		return fmt.Errorf("bloom file %s: %w", path, err)
	}
	f := &bloom.BloomFilter{}
	if _, err := f.ReadFrom(bytes.NewReader(data)); err != nil {
		return fmt.Errorf("bloom file %s: %w", path, err)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.f = f
	t.dirty = false
	return nil
}

// bloomHeaderLen is the serialized header: big-endian uint64 m, k and bitset
// length, followed by ceil(length/64) uint64 words.
const bloomHeaderLen = 24

// validateBloomFile checks the header of a serialized filter before decoding.
// The decoder trusts the header: m == 0 makes every lookup divide by zero,
// k == 0 makes every song test as played, and a huge length allocates before
// any bit is read.
func validateBloomFile(data []byte) error {
	if len(data) < bloomHeaderLen {
		return fmt.Errorf("header is %d bytes, want %d", len(data), bloomHeaderLen)
	}
	m := binary.BigEndian.Uint64(data[0:8])
	k := binary.BigEndian.Uint64(data[8:16])
	length := binary.BigEndian.Uint64(data[16:24])
	if m == 0 || k == 0 {
		return fmt.Errorf("degenerate filter m=%d k=%d", m, k)
	}
	if length != m {
		return fmt.Errorf("bitset length %d does not match m=%d", length, m)
	}
	words := m / 64
	if m%64 != 0 {
		words++
	}
	// words <= 2^58, so the byte count cannot overflow uint64.
	if want := bloomHeaderLen + 8*words; uint64(len(data)) != want {
		return fmt.Errorf("file is %d bytes, want %d for m=%d", len(data), want, m)
	}
	return nil
}
