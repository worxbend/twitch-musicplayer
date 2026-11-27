package watcher

import (
	"context"
	"os"
	"time"
)

// registerWhenSettled indexes and queues path in the background once it stops
// changing. An event for a path already in flight schedules exactly one more
// pass, so a tag saved after the metadata read is not lost. That pass finds
// the song indexed and only refreshes it, so a directory scan racing the
// file's own Create event still queues it once.
func (w *Watcher) registerWhenSettled(ctx context.Context, path string) {
	if !w.claim(path) {
		return
	}
	w.settling.Go(func() {
		for {
			if err := w.awaitSettled(ctx, path); err != nil {
				if ctx.Err() == nil {
					w.logger.Printf("skip %s: %v", path, err)
				}
				w.release(path)
				return
			}
			if _, _, err := w.registerFile(ctx, path, true); err != nil {
				w.logger.Printf("register %s: %v", path, err)
			}
			if !w.releaseOrRerun(path) {
				return
			}
		}
	})
}

// awaitSettled waits for a free settle slot, then for path to settle. The slot
// is released as soon as the wait ends: the cap bounds stat polling, and
// registerFile is short.
func (w *Watcher) awaitSettled(ctx context.Context, path string) error {
	select {
	case w.settleSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-w.settleSem }()
	return w.settleWait(ctx, path)
}

// claim reports whether the caller now owns path. If path is already in
// flight, it asks the owner for one more pass instead.
func (w *Watcher) claim(path string) bool {
	w.inflightMu.Lock()
	defer w.inflightMu.Unlock()
	if _, busy := w.inflight[path]; busy {
		w.inflight[path] = true
		return false
	}
	w.inflight[path] = false
	return true
}

// releaseOrRerun drops the claim on path and returns false, unless another
// pass was requested: then it keeps the claim and returns true.
func (w *Watcher) releaseOrRerun(path string) bool {
	w.inflightMu.Lock()
	defer w.inflightMu.Unlock()
	if w.inflight[path] {
		w.inflight[path] = false
		return true
	}
	delete(w.inflight, path)
	return false
}

// release drops the claim on path, discarding any requested pass.
func (w *Watcher) release(path string) {
	w.inflightMu.Lock()
	defer w.inflightMu.Unlock()
	delete(w.inflight, path)
}

// waitSettled returns nil once path's size and modtime are unchanged across
// two consecutive polls. It fails if path disappears or ctx ends. There is no
// deadline: a copy over a slow link can take minutes, and no Write event
// would ever restart the wait once it gave up.
func (w *Watcher) waitSettled(ctx context.Context, path string) error {
	prev, err := os.Stat(path)
	if err != nil {
		return err
	}
	poll := time.NewTicker(w.settlePoll)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-poll.C:
		}
		cur, err := os.Stat(path)
		if err != nil {
			return err
		}
		if cur.Size() == prev.Size() && cur.ModTime().Equal(prev.ModTime()) {
			return nil
		}
		prev = cur
	}
}
