import { createSignal, onCleanup } from 'solid-js';
import { Stage } from './components/Stage';
import { PlayerUI } from './components/PlayerUI';
import { BackendPlaybackClient } from './audio/BackendPlaybackClient';
import type { ConnectionStatus, PlaybackStatus } from './audio/BackendPlaybackClient';
import type { AudioEngine } from './audio/AudioEngine';

export function App() {
  const [audio, setAudio]           = createSignal<AudioEngine | null>(null);
  const [trackName, setTrackName]   = createSignal('waiting for track…');
  const [trackArtist, setTrackArtist] = createSignal('lofi radio · 24/7');
  const [wsStatus, setWsStatus]     = createSignal<ConnectionStatus>('disconnected');
  const [playbackStatus, setPlaybackStatus] = createSignal<PlaybackStatus>('idle');
  let backendClient: BackendPlaybackClient | null = null;
  // Debounce the brief null between tracks so crossfades don't flash "waiting".
  let gapTimer = 0;

  const onReady = (a: AudioEngine) => {
    setAudio(a);
    backendClient?.dispose();
    backendClient = new BackendPlaybackClient({
      audio: a,
      // The client reports connection health as a typed value. Sniffing the
      // human-readable message instead used to show "Live" for the status
      // "radio connection error", because that string contains "connect".
      onStatus: (status) => { setWsStatus(status.connection); setPlaybackStatus(status.playback); },
      onSongChange: (song) => {
        window.clearTimeout(gapTimer);
        if (song) {
          setTrackName(song.title || 'untitled');
          setTrackArtist(song.artist || 'lofi radio · 24/7');
        } else {
          // Only fall back to the idle label if no new track arrives shortly.
          gapTimer = window.setTimeout(() => {
            setTrackName('waiting for track…');
            setTrackArtist('lofi radio · 24/7');
          }, 2500);
        }
      },
    });
    backendClient.start();
  };

  onCleanup(() => {
    window.clearTimeout(gapTimer);
    backendClient?.dispose();
  });

  return (
    <>
      <Stage onReady={onReady} ramp={new URLSearchParams(window.location.search).get('ramp') ?? undefined} />
      <PlayerUI
        trackName={trackName}
        trackArtist={trackArtist}
        wsStatus={wsStatus}
        playbackStatus={playbackStatus}
        onEnableAudio={() => backendClient?.retryPendingPlay()}
        audio={audio}
      />
    </>
  );
}
