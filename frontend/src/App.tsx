import { createSignal, onCleanup } from 'solid-js';
import { Stage } from './components/Stage';
import { PlayerUI } from './components/PlayerUI';
import type { ConnectionStatus, ErrorCode, PlaybackStatus } from './audio/BackendPlaybackClient';
import type { AudioEngine } from './audio/AudioEngine';
import { getMediaSession } from './audio/mediaSession';
import { startRadio, type RadioHandle } from './audio/radioSession';

export function App() {
  const [audio, setAudio]           = createSignal<AudioEngine | null>(null);
  const [trackName, setTrackName]   = createSignal('waiting for track…');
  const [trackArtist, setTrackArtist] = createSignal('lofi radio · 24/7');
  const [trackCover, setTrackCover] = createSignal<string | undefined>();
  const [wsStatus, setWsStatus]     = createSignal<ConnectionStatus>('disconnected');
  const [playbackStatus, setPlaybackStatus] = createSignal<PlaybackStatus>('idle');
  const [errorCode, setErrorCode] = createSignal<ErrorCode | undefined>();
  const [mediaState, setMediaState] = createSignal<MediaSessionPlaybackState>('none');
  let radio: RadioHandle | null = null;

  const onReady = (a: AudioEngine) => {
    setAudio(a);
    radio?.dispose();
    radio = startRadio({
      audio: a,
      session: getMediaSession(),
      placeholder: 'waiting for track…',
      untitled: 'untitled',
      artistFallback: 'lofi radio · 24/7',
      onTrack: (name, artist, cover) => {
        setTrackName(name);
        setTrackArtist(artist);
        setTrackCover(cover);
      },
      onStatus: (conn, playback, code) => {
        setWsStatus(conn);
        setPlaybackStatus(playback);
        setErrorCode(code);
      },
      onMediaState: setMediaState,
    });
  };

  onCleanup(() => radio?.dispose());

  return (
    <>
      <Stage onReady={onReady} ramp={new URLSearchParams(window.location.search).get('ramp') ?? undefined} />
      <PlayerUI
        trackName={trackName}
        trackArtist={trackArtist}
        trackCover={trackCover}
        wsStatus={wsStatus}
        playbackStatus={playbackStatus}
        errorCode={errorCode}
        paused={() => mediaState() === 'paused'}
        onEnableAudio={() => radio?.retryPendingPlay()}
        audio={audio}
      />
    </>
  );
}
