import { createSignal, onCleanup, onMount } from 'solid-js';
import { Stage } from './components/Stage';
import { PlayerUI } from './components/PlayerUI';
import { BackendPlaybackClient } from './audio/BackendPlaybackClient';
import type { ConnectionStatus, PlaybackStatus } from './audio/BackendPlaybackClient';
import type { AudioEngine } from './audio/AudioEngine';
import type { SceneDef } from './viz/scenes';
import { bindMediaSession, getMediaSession, setMediaPlayState, updateMediaMetadata } from './audio/mediaSession';

interface Props {
  scene: SceneDef;
  transparent?: boolean;
  /** Colour ramp name from the ?ramp= query parameter. */
  ramp?: string;
}

/**
 * One visualizer page. Every scene in the catalogue renders through this
 * component, so they all share the same playback wiring, broadcast workspace,
 * and transparent-overlay behaviour.
 *
 * The opaque variant shows the shared player UI. The transparent variant
 * shows the visuals alone, so it can be composited over other sources
 * in OBS without a background or any text.
 */
export function SceneApp(props: Props) {
  const [trackName, setTrackName] = createSignal('Waiting for a track…');
  const [trackArtist, setTrackArtist] = createSignal('lofi radio · 24/7');
  const [audio, setAudio] = createSignal<AudioEngine | null>(null);
  const [wsStatus, setWsStatus] = createSignal<ConnectionStatus>('disconnected');
  const [playbackStatus, setPlaybackStatus] = createSignal<PlaybackStatus>('idle');
  const [mediaState, setMediaState] = createSignal<MediaSessionPlaybackState>('none');
  let backendClient: BackendPlaybackClient | null = null;
  let unbindMediaSession = () => {};

  onMount(() => {
    document.title = `lofi.radio — ${props.scene.label}${props.transparent ? ' overlay' : ''}`;
    const root = document.documentElement;
    // Transparent pages get the overlay class only, so the page background
    // stays clear for compositing. Opaque pages get the scene's own backdrop.
    const pageClass = props.transparent
      ? 'logo-overlay-page'
      : props.scene.pageClass ?? 'scene-page';
    root.classList.add(pageClass);

    const retryPending = () => backendClient?.retryPendingPlay();
    if (props.transparent) window.addEventListener('pointerdown', retryPending);

    onCleanup(() => {
      window.removeEventListener('pointerdown', retryPending);
      root.classList.remove(pageClass);
    });
  });

  const onReady = (audio: AudioEngine) => {
    setAudio(audio);
    backendClient?.dispose();
    unbindMediaSession();
    // OBS overlay sources must never claim the OS media session.
    const session = props.transparent ? null : getMediaSession();
    backendClient = new BackendPlaybackClient({
      audio,
      onStatus: (status) => {
        setWsStatus(status.connection);
        setPlaybackStatus(status.playback);
      },
      onSongChange: (song) => {
        setTrackName(song?.title || 'Waiting for a track…');
        setTrackArtist(song?.artist || 'lofi radio · 24/7');
        updateMediaMetadata(session, song);
      },
    });
    const unbindHandlers = bindMediaSession(session, {
      onPlay: () => playbackStatus() === 'blocked' ? backendClient?.retryPendingPlay() : audio.play(),
      onPause: () => audio.pause(),
    });
    // The OS play/pause state follows the audio element, not the radio's connection reports.
    // PlayerUI reads it too, so a media-key pause shows as Paused.
    audio.onPlayStateChange = (state) => { setMediaPlayState(session, state); setMediaState(state); };
    unbindMediaSession = () => { audio.onPlayStateChange = undefined; setMediaState('none'); unbindHandlers(); };
    backendClient.start();
  };

  onCleanup(() => { unbindMediaSession(); backendClient?.dispose(); });

  return (
    <>
      <Stage
        onReady={onReady}
        transparent={props.transparent}
        showBackground={!props.transparent}
        createVisualizer={(app, audio, opts) =>
          props.scene.create(app, audio, {
            ...opts,
            ramp: props.ramp,
          })
        }
      />
      {!props.transparent && (
        <PlayerUI
          trackName={trackName}
          trackArtist={trackArtist}
          wsStatus={wsStatus}
          playbackStatus={playbackStatus}
          paused={() => mediaState() === 'paused'}
          audio={audio}
          onEnableAudio={() => backendClient?.retryPendingPlay()}
          sceneName={props.scene.label}
          sceneSlug={props.scene.slug}
        />
      )}
    </>
  );
}
