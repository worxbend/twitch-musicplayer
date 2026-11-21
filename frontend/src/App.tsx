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

  const onReady = (a: AudioEngine) => {
    setAudio(a);
    backendClient?.dispose();
    backendClient = new BackendPlaybackClient({
      audio: a,
      // The client reports connection health as a typed value. Sniffing the
      // human-readable message instead used to show "Live" for the status
      // "radio connection error", because that string contains "connect".
      onStatus: (status) => { setWsStatus(status.connection); setPlaybackStatus(status.playback); },
      // The client keeps the title up through a crossfade and sends null only
      // once a track has really ended, so no debounce is needed here.
      onSongChange: (song) => {
        setTrackName(song ? song.title || 'untitled' : 'waiting for track…');
        setTrackArtist(song?.artist || 'lofi radio · 24/7');
      },
    });
    backendClient.start();
  };

  onCleanup(() => backendClient?.dispose());

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
