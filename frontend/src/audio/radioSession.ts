/* Radio wiring shared by the player pages: the backend client, track info and the OS media session.
 *
 * Kept free of solid-js so it runs under node --test; each page keeps its own signals.
 */
import type { AudioEngine } from './AudioEngine';
import { BackendPlaybackClient } from './BackendPlaybackClient.ts';
import type { ConnectionStatus, ErrorCode, PlaybackStatus } from './BackendPlaybackClient';
import {
  bindMediaSession,
  setMediaPlayState,
  setMediaPosition,
  updateMediaMetadata,
  type MediaSessionLike,
} from './mediaSession.ts';

type ClientOptions = ConstructorParameters<typeof BackendPlaybackClient>[0];
type RadioClient = Pick<BackendPlaybackClient, 'start' | 'retryPendingPlay' | 'dispose'>;

export interface RadioOptions {
  audio: AudioEngine;
  /** null keeps the page off the OS media session, as OBS overlays must. */
  session: MediaSessionLike | null;
  /** Track name while no song plays. */
  placeholder: string;
  /** Track name for a song with an empty title. */
  untitled: string;
  /** Artist for a missing song or a song without an artist. */
  artistFallback: string;
  onTrack(name: string, artist: string, cover: string | undefined): void;
  /** errorCode is set only while playback is 'error'. */
  onStatus(conn: ConnectionStatus, playback: PlaybackStatus, errorCode?: ErrorCode): void;
  onMediaState(state: MediaSessionPlaybackState): void;
  /** Test seam; defaults to a real BackendPlaybackClient. */
  createClient?: (options: ClientOptions) => RadioClient;
}

export interface RadioHandle {
  /** Retries a play the browser blocked; PlayerUI awaits it to report a failure. */
  retryPendingPlay(): Promise<void> | undefined;
  dispose(): void;
}

export function startRadio(opts: RadioOptions): RadioHandle {
  const { audio, session } = opts;
  const createClient = opts.createClient ?? ((options) => new BackendPlaybackClient(options));
  let playback: PlaybackStatus = 'idle';
  const client = createClient({
    audio,
    // The client reports connection health as a typed value. Sniffing the
    // human-readable message instead used to show "Live" for the status
    // "radio connection error", because that string contains "connect".
    onStatus: (status) => {
      playback = status.playback;
      opts.onStatus(status.connection, status.playback, status.errorCode);
    },
    // The client keeps the title up through a crossfade and sends null only
    // once a track has really ended, so no debounce is needed here.
    onSongChange: (song) => {
      const name = song ? song.title || opts.untitled : opts.placeholder;
      opts.onTrack(name, song?.artist || opts.artistFallback, song?.coverUrl);
      updateMediaMetadata(session, song);
    },
  });
  const unbindHandlers = bindMediaSession(session, {
    onPlay: () => playback === 'blocked' ? client.retryPendingPlay() : audio.play(),
    onPause: () => audio.pause(),
  });
  // The OS play/pause state follows the audio element, not the radio's connection reports.
  // Pages read it too, so a media-key pause shows as Paused.
  audio.onPlayStateChange = (state) => { setMediaPlayState(session, state); opts.onMediaState(state); };
  audio.onPositionChange = (state) => setMediaPosition(session, state);
  client.start();

  let disposed = false;
  return {
    retryPendingPlay: () => client.retryPendingPlay(),
    dispose: () => {
      if (disposed) return;
      disposed = true;
      audio.onPlayStateChange = undefined;
      audio.onPositionChange = undefined;
      opts.onMediaState('none');
      unbindHandlers();
      client.dispose();
    },
  };
}
