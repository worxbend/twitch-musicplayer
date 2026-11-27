import { createSignal, onCleanup, onMount } from 'solid-js';
import { Stage } from './components/Stage';
import { PlayerUI } from './components/PlayerUI';
import type { ConnectionStatus, PlaybackStatus } from './audio/BackendPlaybackClient';
import type { AudioEngine } from './audio/AudioEngine';
import type { SceneDef } from './viz/scenes';
import { getMediaSession } from './audio/mediaSession';
import { startRadio, type RadioHandle } from './audio/radioSession';

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
  const [trackCover, setTrackCover] = createSignal<string | undefined>();
  const [audio, setAudio] = createSignal<AudioEngine | null>(null);
  const [wsStatus, setWsStatus] = createSignal<ConnectionStatus>('disconnected');
  const [playbackStatus, setPlaybackStatus] = createSignal<PlaybackStatus>('idle');
  const [mediaState, setMediaState] = createSignal<MediaSessionPlaybackState>('none');
  let radio: RadioHandle | null = null;

  onMount(() => {
    document.title = `lofi.radio — ${props.scene.label}${props.transparent ? ' overlay' : ''}`;
    const root = document.documentElement;
    // Transparent pages get the overlay class only, so the page background
    // stays clear for compositing. Opaque pages get the scene's own backdrop.
    const pageClass = props.transparent
      ? 'logo-overlay-page'
      : props.scene.pageClass ?? 'scene-page';
    root.classList.add(pageClass);

    const retryPending = () => radio?.retryPendingPlay();
    if (props.transparent) window.addEventListener('pointerdown', retryPending);

    onCleanup(() => {
      window.removeEventListener('pointerdown', retryPending);
      root.classList.remove(pageClass);
    });
  });

  const onReady = (audio: AudioEngine) => {
    setAudio(audio);
    radio?.dispose();
    radio = startRadio({
      audio,
      // OBS overlay sources must never claim the OS media session.
      session: props.transparent ? null : getMediaSession(),
      placeholder: 'Waiting for a track…',
      untitled: 'Waiting for a track…',
      artistFallback: 'lofi radio · 24/7',
      onTrack: (name, artist, cover) => {
        setTrackName(name);
        setTrackArtist(artist);
        setTrackCover(cover);
      },
      onStatus: (conn, playback) => {
        setWsStatus(conn);
        setPlaybackStatus(playback);
      },
      onMediaState: setMediaState,
    });
  };

  onCleanup(() => radio?.dispose());

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
          trackCover={trackCover}
          wsStatus={wsStatus}
          playbackStatus={playbackStatus}
          paused={() => mediaState() === 'paused'}
          audio={audio}
          onEnableAudio={() => radio?.retryPendingPlay()}
          sceneName={props.scene.label}
          sceneSlug={props.scene.slug}
        />
      )}
    </>
  );
}
