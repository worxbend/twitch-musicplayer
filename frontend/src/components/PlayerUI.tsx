import { createSignal, onMount, onCleanup, Show, For } from 'solid-js';
import type { AudioEngine } from '../audio/AudioEngine';
import type { ConnectionStatus, PlaybackStatus } from '../audio/BackendPlaybackClient';
import { TerminalHeader } from './TerminalHeader';
import { SCENES } from '../viz/scenes';

interface Props {
  trackName: () => string;
  trackArtist: () => string;
  wsStatus: () => ConnectionStatus;
  playbackStatus: () => PlaybackStatus;
  audio: () => AudioEngine | null;
  onEnableAudio: () => Promise<void> | undefined;
  sceneName?: string;
  sceneSlug?: string;
}
function fmtTime(secs: number): string {
  if (!Number.isFinite(secs) || secs < 0) secs = 0;
  return `${Math.floor(secs / 60)}:${Math.floor(secs % 60).toString().padStart(2, '0')}`;
}

export function PlayerUI(props: Props) {
  const [time, setTime] = createSignal('--:--:--');
  const [enabling, setEnabling] = createSignal(false);
  const enableAudio = async () => {
    if (enabling()) return;
    setEnabling(true);
    try { await props.onEnableAudio(); }
    finally { setEnabling(false); }
  };
  const [fraction, setFraction] = createSignal(0);
  const [elapsed, setElapsed] = createSignal('0:00');
  const [remaining, setRemaining] = createSignal('0:00');
  onMount(() => {
    const tick = () => {
      setTime(new Date().toLocaleTimeString([], { hour12: false }));
      const progress = props.audio()?.getPlaybackProgress() ?? null;
      if (progress && progress.duration > 0) {
        setFraction(Math.min(1, Math.max(0, progress.currentTime / progress.duration)));
        setElapsed(fmtTime(progress.currentTime));
        setRemaining('-' + fmtTime(progress.duration - progress.currentTime));
      } else { setFraction(0); setElapsed('0:00'); setRemaining('0:00'); }
    };
    tick();
    const interval = window.setInterval(tick, 250);
    onCleanup(() => window.clearInterval(interval));
  });
  const isPlaying = () => props.wsStatus() === 'connected' && props.playbackStatus() === 'playing';
  const statusLabel = () => props.wsStatus() === 'connected'
    ? isPlaying() ? 'On air' : 'Standby'
    : props.wsStatus() === 'reconnecting' ? 'Reconnecting' : 'Offline';
  const detail = () => props.wsStatus() !== 'connected' ? 'Waiting for the backend. Reconnecting automatically.'
    : props.playbackStatus() === 'blocked' ? 'Your browser needs a click before it can play audio.'
    : props.playbackStatus() === 'error' ? 'Waiting for playable audio. Check your library; the player will retry automatically.'
    : props.playbackStatus() === 'idle' ? 'Connected. Waiting for the next track.'
    : 'Your library, on a continuous frequency.';
  return (
    <div class="player-shell">
      <TerminalHeader active="player" />
      <main id="main" class="player-workspace" tabIndex={-1}>
        <div class="session-header">
          <div><p class="session-caption">An after-hours listening room</p><h1>{props.sceneName || 'Ambient frequency'}</h1><p class="session-description">{props.sceneSlug ? 'Sound, made visible.' : 'Soft rain. Deep grooves. Your own orbit.'}</p></div>
          <div class="session-meta"><span class="status" classList={{'status--live': isPlaying()}}><span class="status-dot" />{statusLabel()}</span><time>{time()}</time><span class="session-location">Local transmission</span></div>
        </div>
        <Show when={props.playbackStatus() !== 'playing' || props.wsStatus() !== 'connected'}>
          <div class="player-message" role="status"><span class="message-prompt" aria-hidden="true">◉</span><p>{detail()}</p><Show when={props.playbackStatus() === 'blocked'}><button class="btn btn-primary" disabled={enabling()} onClick={() => void enableAudio()}>{enabling() ? 'Starting audio…' : 'Enable audio ▶'}</button></Show></div>
        </Show>
        <div class="performance-space" aria-hidden="true"><span class="performance-coordinate">{props.sceneSlug || 'ambient'} / {isPlaying() ? 'audio reactive' : 'idle study'}</span><span class="performance-coordinate">Signal field</span></div>
        <div class="player-controls">
        <div class="scene-switcher" role="group" aria-label="Scene selection">
          <a href={`/${window.location.search}`} aria-current={!props.sceneSlug ? 'page' : undefined}>Ambient</a>
          <For each={SCENES}>{scene => <a href={`/${scene.slug}${new URLSearchParams(window.location.search).has('ramp') ? `?ramp=${encodeURIComponent(new URLSearchParams(window.location.search).get('ramp')!)}` : ''}`} aria-current={props.sceneSlug === scene.slug ? 'page' : undefined}>{scene.label}</a>}</For>
        </div>
        <section class="player-deck" aria-label="Current track">
          <div class="deck-art" aria-hidden="true"><span /></div>
          <div class="np-info"><span class="np-label">{isPlaying() ? 'Now playing' : 'Current track'}</span><span id="np-title" title={props.trackName()}>{props.trackName()}</span><span id="np-artist">{props.trackArtist()}</span></div>
          <div class="np-progress"><div class="np-times"><span>{elapsed()}</span><span>{remaining()}</span></div><div class="np-bar" role="progressbar" aria-label="Track progress" aria-valuenow={Math.round(fraction() * 100)} aria-valuemin={0} aria-valuemax={100}><div class="np-bar-fill" style={{width: `${fraction() * 100}%`}} /></div><span class="progress-caption">Continuous playback</span></div>
          <a class="btn deck-link" href="/scenes">Change scene ↗</a>
        </section>
        </div>
        <footer class="player-footer"><span>Stay a little longer.</span><span>Your music. Your frequency.</span><a href={props.sceneSlug ? `/${props.sceneSlug}-overlay${window.location.search}` : `/overlay${window.location.search}`}>Clean OBS view ↗</a></footer>
      </main>
    </div>
  );
}
