/* Media Session wiring: the OS media hub, lock screen and media keys.
 *
 * Kept free of client and engine code so it runs under node --test with a fake session.
 */
export type MediaSessionLike = Pick<MediaSession, 'metadata' | 'playbackState' | 'setActionHandler'>;

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
  };
}
