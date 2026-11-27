import { createEffect, createSignal, onMount, onCleanup, Show, For } from 'solid-js';
import type { AudioEngine } from '../audio/AudioEngine';
import { loadVolume, saveVolume, shouldIgnoreVolumeKey, stepVolume, VOLUME_STEP } from '../audio/volumeStore';
import type { ConnectionStatus, PlaybackStatus } from '../audio/BackendPlaybackClient';
import { TerminalHeader } from './TerminalHeader';
import { playerStatus } from './playerStatus';
import { SCENES } from '../viz/scenes';

interface Props {
  trackName: () => string;
  trackArtist: () => string;
  /** Embedded cover art of the playing track, or undefined when it has none. */
  trackCover: () => string | undefined;
  wsStatus: () => ConnectionStatus;
  playbackStatus: () => PlaybackStatus;
  audio: () => AudioEngine | null;
  /** The audio element is paused, e.g. by a media key, while the radio still reports playing. */
  paused?: () => boolean;
  onEnableAudio: () => Promise<void> | undefined;
  sceneName?: string;
  sceneSlug?: string;
}
function fmtTime(secs: number): string {
  if (!Number.isFinite(secs) || secs < 0) secs = 0;
  return `${Math.floor(secs / 60)}:${Math.floor(secs % 60).toString().padStart(2, '0')}`;
}

// Reading window.localStorage itself throws when site data is blocked.
function browserStorage(): Storage | undefined {
  try { return window.localStorage; } catch { return undefined; }
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
  const [valueText, setValueText] = createSignal('No track loaded');
  const [volume, setVolume] = createSignal(loadVolume(browserStorage()));
  // A cover that failed to load falls back to the record art instead of a broken image.
  const [failedCover, setFailedCover] = createSignal<string>();
  const cover = () => {
    const url = props.trackCover();
    return url && url !== failedCover() ? url : undefined;
  };
  const [muted, setMuted] = createSignal(false);
  const storeVolume = (next: number) => {
    setVolume(next);
    saveVolume(browserStorage(), next);
  };
  const unmuteAt = (next: number) => {
    storeVolume(next);
    setMuted(false);
  };
  createEffect(() => props.audio()?.setVolume(muted() ? 0 : volume()));
  onMount(() => {
    // Lets this page, and no OBS or fullscreen route, scroll when zoomed in.
    document.documentElement.classList.add('player-page');
    onCleanup(() => document.documentElement.classList.remove('player-page'));
  });
  onMount(() => {
    const onKey = (e: KeyboardEvent) => {
      if (shouldIgnoreVolumeKey(e)) return;
      // WCAG 2.1.4: bare-key shortcuts act only while focus is in the deck.
      if (!(e.target instanceof Element) || !e.target.closest('.player-deck')) return;
      if (e.key === 'm' || e.key === 'M') setMuted(m => !m);
      else if (e.key === '-') storeVolume(stepVolume(volume(), -VOLUME_STEP));
      else if (e.key === '=' || e.key === '+') unmuteAt(stepVolume(volume(), VOLUME_STEP));
      else return;
      e.preventDefault();
    };
    window.addEventListener('keydown', onKey);
    onCleanup(() => window.removeEventListener('keydown', onKey));
  });
  onMount(() => {
    const tick = () => {
      setTime(new Date().toLocaleTimeString([], { hour12: false }));
      const progress = props.audio()?.getPlaybackProgress() ?? null;
      if (progress && progress.duration > 0) {
        setFraction(Math.min(1, Math.max(0, progress.currentTime / progress.duration)));
        setElapsed(fmtTime(progress.currentTime));
        setRemaining('-' + fmtTime(progress.duration - progress.currentTime));
        setValueText(`${fmtTime(progress.currentTime)} of ${fmtTime(progress.duration)}`);
      } else { setFraction(0); setElapsed('0:00'); setRemaining('0:00'); setValueText('No track loaded'); }
    };
    tick();
    const interval = window.setInterval(tick, 250);
    onCleanup(() => window.clearInterval(interval));
  });
  const status = () => playerStatus(props.wsStatus(), props.playbackStatus(), props.paused?.() ?? false);
  return (
    <div class="player-shell">
      <TerminalHeader active="player" />
      <main id="main" class="player-workspace" tabIndex={-1}>
        <div class="session-header">
          <div><p class="session-caption">An after-hours listening room</p><h1>{props.sceneName || 'Ambient frequency'}</h1><p class="session-description">{props.sceneSlug ? 'Sound, made visible.' : 'Soft rain. Deep grooves. Your own orbit.'}</p></div>
          <div class="session-meta"><span class="status" classList={{'status--live': status().live}}><span class="status-dot" />{status().label}</span><time>{time()}</time><span class="session-location">Local transmission</span></div>
        </div>
        <p class="sr-only" role="status" aria-live="polite">{`${status().label}. ${status().detail}`}</p>
        <Show when={!status().live}>
          <div class="player-message"><span class="message-prompt" aria-hidden="true">◉</span><p>{status().detail}</p><Show when={props.playbackStatus() === 'blocked'}><button type="button" class="btn btn-primary" aria-disabled={enabling() ? 'true' : undefined} onClick={() => void enableAudio()}>{enabling() ? 'Starting audio…' : 'Enable audio ▶'}</button></Show><Show when={props.playbackStatus() === 'playing' && props.paused?.()}><button class="btn btn-primary" onClick={() => props.audio()?.play()}>Resume ▶</button></Show></div>
        </Show>
        <div class="performance-space" aria-hidden="true"><span class="performance-coordinate">{props.sceneSlug || 'ambient'} / {status().live ? 'audio reactive' : 'idle study'}</span><span class="performance-coordinate">Signal field</span></div>
        <div class="player-controls">
        <div class="scene-switcher" role="group" aria-label="Scene selection">
          <a href={`/${window.location.search}`} aria-current={!props.sceneSlug ? 'page' : undefined}>Ambient</a>
          <For each={SCENES}>{scene => <a href={`/${scene.slug}${new URLSearchParams(window.location.search).has('ramp') ? `?ramp=${encodeURIComponent(new URLSearchParams(window.location.search).get('ramp')!)}` : ''}`} aria-current={props.sceneSlug === scene.slug ? 'page' : undefined}>{scene.label}</a>}</For>
        </div>
        <section class="player-deck" aria-label="Current track">
          <Show when={cover()} keyed fallback={<div class="deck-art" aria-hidden="true"><span /></div>}>
            {url => <img class="np-cover" src={url} alt="" width="58" height="58" decoding="async" onError={() => setFailedCover(url)} />}
          </Show>
          <div class="np-info"><span class="np-label">{status().live ? 'Now playing' : 'Current track'}</span><span id="np-title" title={props.trackName()}>{props.trackName()}</span><span id="np-artist">{props.trackArtist()}</span></div>
          <div class="np-progress"><div class="np-times"><span>{elapsed()}</span><span>{remaining()}</span></div><div class="np-bar" role="progressbar" aria-label="Track progress" aria-valuenow={Math.round(fraction() * 100)} aria-valuetext={valueText()} aria-valuemin={0} aria-valuemax={100}><div class="np-bar-fill" style={{width: `${fraction() * 100}%`}} /></div><span class="progress-caption">Continuous playback</span></div>
          <div class="np-volume"><button type="button" class="btn btn-icon" aria-pressed={muted()} aria-label="Mute" title="Mute (M)" onClick={() => setMuted(m => !m)}>{muted() ? '×' : '♪'}</button><input type="range" min="0" max="100" step="1" aria-label="Volume" title="Volume (- / +)" value={Math.round(volume() * 100)} onInput={e => unmuteAt(Number(e.currentTarget.value) / 100)} /></div>
          <a class="btn deck-link" href="/scenes">Change scene ↗</a>
        </section>
        </div>
        <footer class="player-footer"><span>Stay a little longer.</span><span>Your music. Your frequency.</span><a href={props.sceneSlug ? `/${props.sceneSlug}-overlay${window.location.search}` : `/overlay${window.location.search}`}>Clean OBS view ↗</a></footer>
      </main>
    </div>
  );
}
