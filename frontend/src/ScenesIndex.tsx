import { createSignal, For, Show, onCleanup, onMount } from 'solid-js';
import { SCENES } from './viz/scenes';
import { RAMPS, type RampName } from './viz/palette';
import { TerminalHeader } from './components/TerminalHeader';
import { SignalPreview } from './components/SignalPreview';
import {
  nowPlayingAlignments,
  nowPlayingFonts,
  nowPlayingPath,
  nowPlayingThemes,
  type NowPlayingAlign,
  type NowPlayingFont,
  type NowPlayingTheme,
} from './nowPlaying';

const studies: Record<string, { medium: string; description: string }> = {
  spectrum: { medium: 'Phosphor / phase', description: 'A luminous laboratory trace, paired with a dancing Lissajous orbit. An intimate view inside the signal.' },
  bars: { medium: 'Frequency / architecture', description: 'Spectral towers rise from a luminous floor. Every frequency finds its own place in the skyline.' },
  mirror: { medium: 'Waveform / interference', description: 'Fine strands weave a mirrored interferometer, folding sound into a weightless fabric.' },
  dots: { medium: 'Particles / constellation', description: 'A constellation of light, gathering and scattering with the energy of your music.' },
  ribbon: { medium: 'Waveform / silk', description: 'Layers of aurora silk curl through the dark. Slow movement, vivid colour, soft edges.' },
  line: { medium: 'Waveform / topography', description: 'Stacked contours map an imaginary landscape, redrawn by every passing phrase.' },
  lens: { medium: 'Frequency / refraction', description: 'A dimensional array of prisms catches the signal in translucent, colour-shifting planes.' },
};

const capitalize = (word: string) => word.charAt(0).toUpperCase() + word.slice(1);

export function ScenesIndex() {
  const [ramp, setRamp] = createSignal<RampName>('SPECTRUM');
  const [notice, setNotice] = createSignal('');
  const [copyError, setCopyError] = createSignal(false);
  const [manualURL, setManualURL] = createSignal('');
  const [align, setAlign] = createSignal<NowPlayingAlign>('left');
  const [size, setSize] = createSignal(32);
  const [hold, setHold] = createSignal(0);
  const [art, setArt] = createSignal(false);
  const [next, setNext] = createSignal(false);
  const [theme, setTheme] = createSignal<NowPlayingTheme>('shadow');
  const [font, setFont] = createSignal<NowPlayingFont>('sans');
  const nowPlayingURL = () => nowPlayingPath({ align: align(), size: size(), hold: hold(), art: art(), theme: theme(), font: font(), next: next() });
  let timer = 0;
  onMount(() => {
    document.documentElement.classList.add('scenes-index-page');
    document.title = 'lofi.radio — Signal collection';
  });
  onCleanup(() => {
    document.documentElement.classList.remove('scenes-index-page');
    window.clearTimeout(timer);
  });
  const sceneURL = (slug: string, overlay = false) =>
    `/${slug}${overlay ? '-overlay' : ''}?ramp=${ramp()}`;
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
            <p class="collection-caption">The visual instrument collection</p>
            <h1>A different way<br />to see sound.</h1>
            <p>Seven signal studies and a place to watch the rain. Find the atmosphere for your next listening session or live broadcast.</p>
          </div>
          <aside class="setup-note" aria-label="OBS setup">
            <div class="setup-note-title">From the observatory to your stream</div>
            <ol>
              <li>Choose a scene and a colour ramp.</li>
              <li>Copy its browser-source URL.</li>
              <li>Paste into OBS. Set to 1920 × 1080.</li>
            </ol>
            <p>Use an overlay for a transparent background.</p>
          </aside>
        </section>
        <div class="scene-toolbar">
          <h2 class="panel-title">Choose your atmosphere <span class="directory-count">{SCENES.length} instruments + ambient</span></h2>
          <label class="ramp-control">Colour ramp
            <select value={ramp()} onChange={e => setRamp(e.currentTarget.value as RampName)}>
              <For each={Object.keys(RAMPS) as RampName[]}>{name => <option value={name}>{name.toLowerCase()}</option>}</For>
            </select>
          </label>
        </div>
        <div class="scenes-grid">
          <For each={SCENES}>{scene => (
            <article class="scene-card" classList={{'scene-card--featured': scene.slug === 'spectrum'}}>
              <div class="scene-preview"><SignalPreview slug={scene.slug} ramp={ramp()} /><span class="scene-preview-label">{studies[scene.slug]?.medium}</span></div>
              <div class="scene-card-body">
                <div class="scene-card-heading"><h2>{scene.label}</h2><span class="scene-format">Reactive</span></div>
                <p>{studies[scene.slug]?.description || scene.description}</p>
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
            <div class="scene-preview"><SignalPreview slug="ambient" ramp={ramp()} /><span class="scene-preview-label">Vinyl / rain / orbit</span></div>
            <div class="scene-card-body">
              <div class="scene-card-heading"><h2>Ambient frequency</h2><span class="scene-format">Signature</span></div>
              <p>A record in orbit. Rain on the window. Settle into the station’s after-hours listening room.</p>
              <a class="btn" href={`/?ramp=${ramp()}`}>Open ambient player ↗</a>
              <div class="scene-urls">
                <button class="scene-url" onClick={() => void copy(`/overlay?ramp=${ramp()}`)}><span>/overlay</span><span class="copy-hint">copy ⧉</span></button>
                <button class="scene-url" onClick={() => void copy(`/logo-overlay?ramp=${ramp()}`)}><span>/logo-overlay</span><span class="copy-hint">copy ⧉</span></button>
              </div>
            </div>
          </aside>
          <aside class="scene-card scene-extra" aria-labelledby="now-playing-heading">
            <div class="scene-card-body">
              <div class="scene-card-heading"><h2 id="now-playing-heading">Now playing text</h2><span class="scene-format">Transparent</span></div>
              <p>The current title and artist as plain text. Lay it over any scene or camera.</p>
              <div class="overlay-builder">
                <label>Align
                  <select value={align()} onChange={e => setAlign(e.currentTarget.value as NowPlayingAlign)}>
                    <For each={nowPlayingAlignments}>{value => <option value={value}>{value}</option>}</For>
                  </select>
                </label>
                <label>Size (px)
                  <input type="number" min="12" max="120" step="1" value={size()} onInput={e => setSize(e.currentTarget.valueAsNumber)} />
                </label>
                <label>Hold (s)
                  <input type="number" min="0" max="600" step="1" value={hold()} aria-describedby="hold-help" onInput={e => setHold(e.currentTarget.valueAsNumber)} />
                </label>
                <small id="hold-help">0 = always visible</small>
                <label>Theme
                  <select value={theme()} onChange={e => setTheme(e.currentTarget.value as NowPlayingTheme)}>
                    <For each={nowPlayingThemes}>{value => <option value={value}>{capitalize(value)}</option>}</For>
                  </select>
                </label>
                <label>Font
                  <select value={font()} onChange={e => setFont(e.currentTarget.value as NowPlayingFont)}>
                    <For each={nowPlayingFonts}>{value => <option value={value}>{capitalize(value)}</option>}</For>
                  </select>
                </label>
                <label class="overlay-check">
                  <input type="checkbox" checked={art()} onChange={e => setArt(e.currentTarget.checked)} />
                  Album art
                </label>
                <label class="overlay-check">
                  <input type="checkbox" checked={next()} onChange={e => setNext(e.currentTarget.checked)} />
                  Up next
                </label>
              </div>
              <div class="scene-urls">
                <button class="scene-url" onClick={() => void copy(nowPlayingURL())} aria-label="Copy now playing overlay URL"><span>{nowPlayingURL()}</span><span class="copy-hint">copy ⧉</span></button>
              </div>
            </div>
          </aside>
        </div>
        <Show when={manualURL()}><label class="manual-copy">Browser-source URL<input readonly value={manualURL()} onFocus={e => e.currentTarget.select()} /></label></Show>
        <footer class="terminal-footer"><span>Your music. Your frequency.</span><span>Audio-reactive instruments. Ready for OBS.</span></footer>
      </main>
      <div role="status" aria-live="polite"><Show when={notice()}><div class="notification" classList={{'notification--error': copyError()}}>{notice()}</div></Show></div>
    </>
  );
}
