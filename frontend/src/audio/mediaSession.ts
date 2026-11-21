/* Media Session wiring: the OS media hub, lock screen and media keys.
 *
 * Kept free of client and engine code so it runs under node --test with a fake session.
 */
import type { PlaybackStatus } from './BackendPlaybackClient';

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

export function setMediaPlaybackState(session: MediaSessionLike | null, playback: PlaybackStatus): void {
  if (session) session.playbackState = playback === 'playing' ? 'playing' : 'paused';
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
  // The OS picks play or pause for a toggle key from playbackState, so it must follow a
  // local pause even though the radio itself still reports 'playing'.
  setHandler(session, 'play', () => { handlers.onPlay(); session.playbackState = 'playing'; });
  setHandler(session, 'pause', () => { handlers.onPause(); session.playbackState = 'paused'; });
  return () => {
    setHandler(session, 'play', null);
    setHandler(session, 'pause', null);
    session.metadata = null;
  };
}
