/* Media Session wiring: the OS media hub, lock screen and media keys.
 *
 * Kept free of client and engine code so it runs under node --test with a fake session.
 */
import { clamp } from '../viz/util.ts';

// setPositionState is optional: older browsers ship Media Session without it.
export type MediaSessionLike = Pick<MediaSession, 'metadata' | 'playbackState' | 'setActionHandler'> & {
  setPositionState?: MediaSession['setPositionState'];
};

const RADIO_NAME = 'lofi radio';

export function getMediaSession(): MediaSessionLike | null {
  if (typeof navigator === 'undefined' || !('mediaSession' in navigator)) return null;
  return navigator.mediaSession;
}

export function updateMediaMetadata(
  session: MediaSessionLike | null,
  song: { title: string; artist?: string } | null,
): void {
  if (!session || typeof MediaMetadata === 'undefined') return;
  session.metadata = song
    ? new MediaMetadata({ title: song.title, artist: song.artist || RADIO_NAME, album: RADIO_NAME })
    : null;
}

// The OS state for one <audio> element. An ended track reads as 'none', not 'paused',
// so a play key cannot claim it can resume a finished track.
export function mediaStateOf(
  el: { paused: boolean; ended: boolean; hasSource: boolean } | null,
): MediaSessionPlaybackState {
  if (!el || !el.hasSource || el.ended) return 'none';
  return el.paused ? 'paused' : 'playing';
}

export function setMediaPlayState(session: MediaSessionLike | null, state: MediaSessionPlaybackState): void {
  if (session) session.playbackState = state;
}

// The OS progress for one <audio> element, or null when there is no progress to show.
// The OS extrapolates the position from playbackRate, so this is sent on changes only.
export function positionStateOf(
  el: { duration: number; currentTime: number; playbackRate: number; hasSource: boolean; ended: boolean } | null,
): MediaPositionState | null {
  if (!el || !el.hasSource || el.ended) return null;
  const { duration } = el;
  if (!Number.isFinite(duration) || duration <= 0) return null;
  const rateValid = Number.isFinite(el.playbackRate) && el.playbackRate > 0;
  return {
    duration,
    position: Number.isFinite(el.currentTime) ? clamp(el.currentTime, 0, duration) : 0,
    playbackRate: rateValid ? el.playbackRate : 1,
  };
}

// Writes the OS progress; null clears it. The radio is seek-free, so no seekto handler exists.
export function setMediaPosition(session: MediaSessionLike | null, state: MediaPositionState | null): void {
  if (!session?.setPositionState) return;
  try {
    session.setPositionState(state ?? undefined);
  } catch (error) {
    // The browser rejects a state it finds invalid; the OS keeps its last progress.
    if (!(error instanceof TypeError)) throw error;
  }
}

function setHandler(session: MediaSessionLike, action: MediaSessionAction, handler: MediaSessionActionHandler | null) {
  try {
    session.setActionHandler(action, handler);
  } catch {
    // The browser does not support this action; the media key simply stays inert.
  }
}

// Binds the play and pause media keys. Returns an unbind that releases the session.
export function bindMediaSession(
  session: MediaSessionLike | null,
  handlers: { onPlay: () => void; onPause: () => void },
): () => void {
  if (!session) return () => {};
  // playbackState is not written here: the OS picks play or pause for a toggle key from
  // it, so it follows the audio element's own events (see mediaStateOf), not key presses.
  setHandler(session, 'play', handlers.onPlay);
  setHandler(session, 'pause', handlers.onPause);
  return () => {
    setHandler(session, 'play', null);
    setHandler(session, 'pause', null);
    session.metadata = null;
    session.playbackState = 'none';
    setMediaPosition(session, null);
  };
}
