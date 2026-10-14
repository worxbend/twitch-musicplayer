import { createSignal, For, Show, onCleanup, onMount } from 'solid-js';
import { SCENES } from './viz/scenes';
import { RAMPS, type RampName } from './viz/palette';
import { TerminalHeader } from './components/TerminalHeader';
import { SignalPreview } from './components/SignalPreview';

export function ScenesIndex() {
  const [ramp, setRamp] = createSignal<RampName>('SPECTRUM');
  const [notice, setNotice] = createSignal('');
  const [copyError, setCopyError] = createSignal(false);
  const [manualURL, setManualURL] = createSignal('');
  let timer = 0;
  onMount(() => {
    document.documentElement.classList.add('scenes-index-page');
    document.title = 'lofi.radio — Scene directory';
  });
  onCleanup(() => {
    document.documentElement.classList.remove('scenes-index-page');
    window.clearTimeout(timer);
  });
  const sceneURL = (slug: string, overlay = false) =>
    `/${slug}${overlay ? '-overlay' : ''}${slug === 'spectrum' ? '' : `?ramp=${ramp()}`}`;
  async function copy(path: string) {
    const url = `${window.location.origin}${path}`;
    window.clearTimeout(timer);
    try {
      if (!navigator.clipboard) throw new Error('Clipboard unavailable');
      await navigator.clipboard.writeText(url);
      setCopyError(false);
      setManualURL('');
      setNotice('✓ Browser-source URL copied. Paste it into OBS.');
    } catch {
      setCopyError(true);
      setManualURL(url);
      setNotice('Clipboard unavailable. Select and copy the URL below.');
    }
    timer = window.setTimeout(() => setNotice(''), 4500);
  }
  return (
    <>
      <TerminalHeader active="scenes" />
      <main id="main" class="scenes-shell" tabIndex={-1}>
        <section class="scenes-intro">
          <div>
            <div class="command-line"><span>streamer@lofi</span>:~$ ls ./scenes</div>
            <h1>Good sound.<br />Great signal<span class="terminal-cursor" aria-hidden="true">_</span></h1>
            <p>Give your radio a visual frequency. Seven audio-reactive scenes, ready for your next stream.</p>
          </div>
          <aside class="setup-note" aria-label="OBS setup">
            <div class="setup-note-title"><span aria-hidden="true">[ i ]</span> Plug into your stream</div>
            <ol>
              <li>Choose a scene and a colour ramp.</li>
              <li>Copy its browser-source URL.</li>
              <li>Paste into OBS. Set to 1920 × 1080.</li>
            </ol>
            <p>Use an overlay for a transparent background.</p>
          </aside>
        </section>
        <div class="scene-toolbar">
          <h2 class="panel-title"><span aria-hidden="true">~/</span>Scene directory <span class="directory-count">{SCENES.length} available</span></h2>
          <label class="ramp-control">Colour ramp
            <select value={ramp()} onChange={e => setRamp(e.currentTarget.value as RampName)}>
              <For each={Object.keys(RAMPS) as RampName[]}>{name => <option value={name}>{name.toLowerCase()}</option>}</For>
            </select>
          </label>
        </div>
        <div class="scenes-grid">
          <For each={SCENES}>{scene => (
            <article class="scene-card">
              <div class="scene-preview"><SignalPreview slug={scene.slug} ramp={ramp()} /><span class="scene-preview-label">{scene.slug === 'spectrum' ? 'fixed / cyan + magenta' : `${ramp().toLowerCase()} / signal study`}</span></div>
              <div class="scene-card-body">
                <div class="scene-card-heading"><h2>{scene.label}</h2><span class="scene-format">WebGL</span></div>
                <p>{scene.description}</p>
                <div class="scene-links">
                  <a class="btn btn-primary" href={sceneURL(scene.slug)}>Open scene <span aria-hidden="true">↗</span></a>
                  <a class="btn" href={sceneURL(scene.slug, true)}>Overlay <span aria-hidden="true">↗</span></a>
                </div>
                <div class="scene-urls">
                  <button class="scene-url" onClick={() => void copy(sceneURL(scene.slug))} aria-label={`Copy ${scene.label} scene URL`}><span>/{scene.slug}</span><span class="copy-hint">copy ⧉</span></button>
                  <button class="scene-url" onClick={() => void copy(sceneURL(scene.slug, true))} aria-label={`Copy ${scene.label} overlay URL`}><span>/{scene.slug}-overlay</span><span class="copy-hint">copy ⧉</span></button>
                </div>
              </div>
            </article>
          )}</For>
          <aside class="scene-card scene-extra">
            <span class="extra-symbol" aria-hidden="true">&gt;_</span>
            <h2>Prefer a little rain?</h2>
            <p>The original lofi scene. A night sky, soft rain, and a frequency of your own.</p>
            <a class="btn" href="/">Open ambient player ↗</a>
            <button class="scene-url" onClick={() => void copy('/overlay')}><span>/overlay</span><span class="copy-hint">copy ⧉</span></button>
            <button class="scene-url" onClick={() => void copy('/logo-overlay')}><span>/logo-overlay</span><span class="copy-hint">copy ⧉</span></button>
          </aside>
        </div>
        <Show when={manualURL()}><label class="manual-copy">Browser-source URL<input readonly value={manualURL()} onFocus={e => e.currentTarget.select()} /></label></Show>
        <footer class="terminal-footer"><span>▂▅█▅▂ &nbsp; your music. your frequency.</span><span>WebGL visuals / backend-driven audio / OBS-ready</span></footer>
      </main>
      <div role="status" aria-live="polite"><Show when={notice()}><div class="notification" classList={{'notification--error': copyError()}}>{notice()}</div></Show></div>
    </>
  );
}
