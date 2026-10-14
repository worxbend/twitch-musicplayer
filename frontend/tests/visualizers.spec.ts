import { test, expect, type Page } from '@playwright/test';
import { createHash } from 'node:crypto';

// Keep these expectations independent of the production catalogue: removing a
// composition must fail rather than silently reduce the coverage.
const compositions = [
  { slug: '', label: 'Ambient' },
  { slug: 'spectrum', label: 'Oscilloscope' },
  { slug: 'bars', label: 'Equaliser' },
  { slug: 'mirror', label: 'Mirror Wave' },
  { slug: 'dots', label: 'Dot Matrix' },
  { slug: 'ribbon', label: 'Ribbon' },
  { slug: 'line', label: 'Trace' },
  { slug: 'lens', label: 'Facets' },
];
const rampNames = ['SPECTRUM', 'AURORA', 'EMBER', 'NEON', 'VU'];

// A real PCM fixture, not silence and not AudioEngine's idle synthesis. Every
// 0.75s it changes both pitch and amplitude; analyser samples must follow it.
function makeModulatedWav() {
  const rate = 22050;
  const samples = rate * 30;
  const wav = Buffer.alloc(44 + samples * 2);
  wav.write('RIFF'); wav.writeUInt32LE(wav.length - 8, 4); wav.write('WAVEfmt ', 8);
  wav.writeUInt32LE(16, 16); wav.writeUInt16LE(1, 20); wav.writeUInt16LE(1, 22);
  wav.writeUInt32LE(rate, 24); wav.writeUInt32LE(rate * 2, 28);
  wav.writeUInt16LE(2, 32); wav.writeUInt16LE(16, 34);
  wav.write('data', 36); wav.writeUInt32LE(samples * 2, 40);
  let phase = 0;
  for (let i = 0; i < samples; i++) {
    const t = i / rate;
    const high = Math.floor(t / 0.75) % 2 === 1;
    phase += 2 * Math.PI * (high ? 1320 : 220) / rate;
    const envelope = (high ? 0.55 : 0.18) * (0.7 + 0.3 * Math.sin(2 * Math.PI * 3 * t));
    wav.writeInt16LE(Math.round(Math.sin(phase) * envelope * 32767), 44 + i * 2);
  }
  return wav;
}
const wav = makeModulatedWav();
const song = { id: 'visualizer-tone', title: 'Changing test tones', artist: 'QA oscillator', album: 'Regression', filename: 'tones.wav', duration_secs: 30, size_bytes: wav.length };

async function station(page: Page, play = false) {
  if (play) {
    // WS interception can confer synthetic activation. Require trusted input,
    // as in ui.spec.ts; leave HTMLMediaElement and Web Audio entirely real.
    await page.addInitScript(() => {
      let activated = false;
      const activate = (event: Event) => { if (event.isTrusted) activated = true; };
      window.addEventListener('pointerdown', activate, true);
      window.addEventListener('keydown', activate, true);
      Object.defineProperty(navigator, 'userActivation', {
        value: { get isActive() { return activated; }, get hasBeenActive() { return activated; } },
      });
    });
  }
  await page.routeWebSocket('**/ws', ws => {
    ws.onMessage(message => {
      if (play && JSON.parse(String(message)).type === 'need_song') {
        ws.send(JSON.stringify({ type: 'play_song', payload: {
          song, history_id: 'visualizer-history', stream_url: '/v1/songs/visualizer-tone/content',
        } }));
      }
    });
  });
  await page.route('**/v1/**', async route => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith('/content')) {
      await route.fulfill({ contentType: 'audio/wav', body: wav });
    } else {
      await route.fulfill({ json: path === '/v1/songs' ? { songs: play ? [song] : [] }
        : path === '/v1/queue' ? { items: [] } : { current_song: play ? song : null } });
    }
  });
}

const runtimeErrors = new WeakMap<Page, string[]>();
function recordErrors(page: Page) {
  const errors: string[] = [];
  runtimeErrors.set(page, errors);
  page.on('pageerror', error => errors.push(`${page.url()} pageerror: ${error.message}`));
  page.on('console', message => {
    if (message.type() === 'error') errors.push(`${page.url()} console: ${message.text()}`);
  });
  return errors;
}

test.afterEach(async ({ page }, testInfo) => {
  // Preserve first-frame/resize errors even when an earlier pixel/layout
  // assertion fails and never reaches the final errors assertion.
  await testInfo.attach('visualizer-runtime-errors', {
    body: Buffer.from(JSON.stringify(runtimeErrors.get(page) ?? [], null, 2)),
    contentType: 'application/json',
  });
});

async function paintedFrame(page: Page) {
  await expect(page.locator('#stage canvas')).toBeVisible();
  await page.evaluate(() => new Promise<void>(resolve => {
    requestAnimationFrame(() => requestAnimationFrame(() => resolve()));
  }));
}

for (const composition of compositions) {
  test(`${composition.label}: real artwork, signal response, colour ramps and framebuffer alpha`, async ({ page }) => {
    const errors = recordErrors(page);
    await station(page);
    // The directory loads Vite's modules without starting a full-screen stage.
    await page.goto('/scenes');
    const result = await page.evaluate(async ({ slug, ramps }) => {
      // Resolve Pixi through the same Vite dependency URL as the scene module,
      // including its optimizer version. No production hooks/globals required.
      const source = await (await fetch('/src/viz/WaveScene.ts')).text();
      const pixiPath = source.match(/import\s+\*\s+as\s+PIXI\s+from\s+["']([^"']+)["']/)?.[1];
      if (!pixiPath) throw new Error('Cannot resolve the scene\'s Vite Pixi import');
      const PIXI = await import(pixiPath) as typeof import('pixi.js');
      const scenePath = '/src/viz/scenes.ts';
      const audioPath = '/src/audio/AudioEngine.ts';
      const analysisPath = '/src/audio/AudioAnalysisEngine.ts';
      const ambientPath = '/src/viz/LofiRainVisualizer.ts';
      const { SCENES } = await import(scenePath) as typeof import('../src/viz/scenes');
      const { AudioEngine } = await import(audioPath) as typeof import('../src/audio/AudioEngine');
      const { AudioAnalysisEngine } = await import(analysisPath) as typeof import('../src/audio/AudioAnalysisEngine');
      const { LofiRainVisualizer } = await import(ambientPath) as typeof import('../src/viz/LofiRainVisualizer');
      const definition = SCENES.find(scene => scene.slug === slug);
      if (slug && !definition) throw new Error(`Missing composition: ${slug}`);

      const originalUpdate = AudioAnalysisEngine.prototype.update;
      const originalRandom = Math.random;
      let level = 0.8;
      // Only replace the analysis boundary. All geometry, gradients, bloom,
      // renderer and scene construction are the actual production code.
      AudioAnalysisEngine.prototype.update = function (audio, dt) {
        const frame = originalUpdate.call(this, audio, dt);
        frame.bass = frame.lowMid = frame.mid = frame.highMid = frame.treble = level;
        frame.rms = level * 0.5; frame.peak = level;
        frame.beat = false; frame.beatStrength = 0;
        for (let i = 0; i < frame.bands.length; i++) {
          const value = level * (0.65 + 0.35 * Math.sin(i * 0.27) ** 2);
          frame.bands[i] = frame.bandPeaks[i] = value;
        }
        for (let i = 0; i < frame.spectrum.length; i++) frame.spectrum[i] = level * Math.exp(-i / 300);
        for (let i = 0; i < frame.timeDomain.length; i++) {
          frame.timeDomain[i] = level * Math.sin(i / frame.timeDomain.length * Math.PI * 18);
        }
        for (let i = 0; i < frame.waveRms.length; i++) {
          const value = level * (0.25 + 0.65 * Math.sin(i * 0.11) ** 2);
          frame.waveMin[i] = -value; frame.waveMax[i] = value; frame.waveRms[i] = value * 0.7;
        }
        return frame;
      };

      function summarize(pixels: Uint8Array | Uint8ClampedArray) {
        let clear = 0, opaque = 0, visible = 0, chromatic = 0, bias = 0;
        let hash = 2166136261;
        for (let i = 0; i < pixels.length; i += 4) {
          const r = pixels[i], g = pixels[i + 1], b = pixels[i + 2], a = pixels[i + 3];
          if (a === 0) clear++;
          if (a === 255) opaque++;
          if (a > 8 && Math.max(r, g, b) > 40) visible++;
          if (a > 16 && Math.max(r, g, b) > 40 && Math.max(r, g, b) - Math.min(r, g, b) > 20) {
            chromatic++;
            bias += (r - b) / Math.max(1, r + g + b);
          }
          for (let c = 0; c < 4; c++) hash = Math.imul(hash ^ pixels[i + c], 16777619);
        }
        return { clear, opaque, visible, chromatic, warmBias: bias / Math.max(1, chromatic), hash: hash >>> 0, total: pixels.length / 4 };
      }

      async function capture(ramp: string, signal: number, transparent: boolean, checkResize = false) {
        level = signal;
        // Identical initialization and dt for every input: differences cannot
        // be attributed to random particles or elapsed animation time.
        let seed = 0x12345678;
        Math.random = () => { seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0; return seed / 4294967296; };
        const app = new PIXI.Application();
        const audio = new AudioEngine();
        let viz: import('../src/viz/types').Visualizer | undefined;
        let initialized = false;
        try {
          await app.init({ width: 512, height: 320, resolution: 1, autoStart: false,
            preference: 'webgl', antialias: true, preserveDrawingBuffer: true,
            backgroundAlpha: transparent ? 0 : 1, background: 0x080c18 });
          initialized = true;
          app.stop();
          const options = { ramp, showBackground: !transparent, transparent, fitToViewport: !transparent };
          viz = definition ? definition.create(app, audio, options) : new LofiRainVisualizer(app, audio, options);
          // Exercise resize before the first update as well as afterwards.
          viz.resize();
          viz.update(1 / 60);
          const extract = () => app.renderer.extract.pixels({ target: app.stage,
            frame: new PIXI.Rectangle(0, 0, app.screen.width, app.screen.height),
            resolution: 1, clearColor: [0, 0, 0, 0] }).pixels;
          const first = summarize(extract());
          for (let i = 0; i < 11; i++) viz.update(1 / 60);
          const pixels = new Uint8Array(extract());
          const art = summarize(pixels);
          const artBounds = () => {
            const bounds = (viz as unknown as { gfx: InstanceType<typeof PIXI.Graphics> }).gfx.getBounds();
            return { left: bounds.minX, top: bounds.minY, right: bounds.maxX, bottom: bounds.maxY,
              width: app.screen.width, height: app.screen.height };
          };
          const fittedBounds = artBounds();
          // Also inspect the real canvas, not just a transparent render texture
          // or the CSS body: renderer backgroundAlpha must be honored.
          app.render();
          const canvas = document.createElement('canvas');
          canvas.width = app.canvas.width; canvas.height = app.canvas.height;
          const ctx = canvas.getContext('2d')!;
          ctx.drawImage(app.canvas, 0, 0);
          const framebuffer = summarize(ctx.getImageData(0, 0, canvas.width, canvas.height).data);
          let resized: ReturnType<typeof summarize> | null = null;
          let resizedBounds: ReturnType<typeof artBounds> | null = null;
          if (checkResize) {
            app.renderer.resize(320, 220);
            viz.resize(); viz.update(1 / 60);
            resized = summarize(extract());
            resizedBounds = artBounds();
          }
          return { ramp, first, art, framebuffer, resized, pixels, fittedBounds, resizedBounds };
        } finally {
          viz?.dispose(); audio.dispose();
          if (initialized) app.destroy({ removeView: true }, { children: true });
        }
      }

      try {
        const quiet = await capture('EMBER', 0.08, true);
        const captures = [];
        let changedSignalPixels = 0;
        for (const ramp of ramps) {
          const captureResult = await capture(ramp, 0.8, true, true);
          if (ramp === 'EMBER') {
            for (let i = 0; i < quiet.pixels.length; i += 4) {
              let delta = 0;
              for (let c = 0; c < 4; c++) delta += Math.abs(quiet.pixels[i + c] - captureResult.pixels[i + c]);
              if (delta > 24) changedSignalPixels++;
            }
          }
          const { pixels: _pixels, ...summary } = captureResult;
          captures.push(summary);
        }
        const { pixels: _pixels, ...opaque } = await capture('EMBER', 0.8, false, true);
        return { catalogue: SCENES.map(scene => scene.slug), captures, opaque, changedSignalPixels };
      } finally {
        AudioAnalysisEngine.prototype.update = originalUpdate;
        Math.random = originalRandom;
      }
    }, { slug: composition.slug, ramps: rampNames });

    expect(result.catalogue).toEqual(compositions.slice(1).map(scene => scene.slug));
    expect(result.changedSignalPixels, 'same clock/randomness; changing only AudioFrame must change actual pixels').toBeGreaterThan(100);
    for (const capture of result.captures) {
      expect(capture.first.visible, `${capture.ramp} first-frame artwork`).toBeGreaterThan(30);
      expect(capture.art.chromatic, `${capture.ramp} coloured art, not a blank background`).toBeGreaterThan(30);
      expect(capture.framebuffer.clear, `${capture.ramp} genuine transparent canvas area`).toBeGreaterThan(capture.framebuffer.total * 0.1);
      expect(capture.framebuffer.visible, `${capture.ramp} overlay must also contain artwork`).toBeGreaterThan(30);
      expect(capture.resized!.visible, `${capture.ramp} art after narrow resize`).toBeGreaterThan(30);
    }
    expect(new Set(result.captures.map(capture => capture.art.hash)).size, 'every selectable ramp changes artwork').toBe(rampNames.length);
    const ember = result.captures.find(capture => capture.ramp === 'EMBER')!;
    const neon = result.captures.find(capture => capture.ramp === 'NEON')!;
    expect(ember.art.warmBias, 'EMBER must visibly be warmer than NEON, not just different geometry').toBeGreaterThan(neon.art.warmBias + 0.08);
    expect(result.opaque.framebuffer.opaque).toBe(result.opaque.framebuffer.total);
    expect(result.opaque.first.chromatic).toBeGreaterThan(30);
    expect(result.opaque.resized!.chromatic).toBeGreaterThan(30);
    for (const bounds of [result.opaque.fittedBounds, result.opaque.resizedBounds!]) {
      // Do not hide incorrect scale behind a clipped bitmap: the actual
      // reactive geometry must fit inside its safe rendering rectangle.
      expect(bounds.left).toBeGreaterThanOrEqual(-2);
      expect(bounds.top).toBeGreaterThanOrEqual(-2);
      expect(bounds.right).toBeLessThanOrEqual(bounds.width + 2);
      expect(bounds.bottom).toBeLessThanOrEqual(bounds.height + 2);
    }
    expect(errors).toEqual([]);
  });
}

test('trusted keyboard unlock plays non-silent WAV; live analyser spectrum and waveform vary', async ({ page }) => {
  const errors = recordErrors(page);
  await station(page, true);
  await page.goto('/bars?ramp=EMBER');
  const enable = page.getByRole('button', { name: 'Enable audio' });
  await expect(enable).toBeVisible();

  // Observe the actual mounted engine and analysis frames through module
  // prototypes. Store observations in a test-created DOM output, not production
  // globals. Crucially no analyser, play(), or audio-data method is mocked.
  await page.evaluate(async () => {
    const audioPath = '/src/audio/AudioEngine.ts';
    const analysisPath = '/src/audio/AudioAnalysisEngine.ts';
    const { AudioEngine } = await import(audioPath) as typeof import('../src/audio/AudioEngine');
    const { AudioAnalysisEngine } = await import(analysisPath) as typeof import('../src/audio/AudioAnalysisEngine');
    const output = document.createElement('output');
    output.id = 'qa-live-audio'; output.hidden = true; document.body.append(output);
    const originalAudioUpdate = AudioEngine.prototype.update;
    const originalAnalysisUpdate = AudioAnalysisEngine.prototype.update;
    const spectrumHashes = new Set<number>();
    const dominantBins = new Set<number>();
    const dominantHz = new Set<number>();
    let samples = 0, maxRms = 0, minRms = Infinity, time = 0;
    AudioEngine.prototype.update = function (dt, intensity) {
      originalAudioUpdate.call(this, dt, intensity);
      if (this.mode === 'backend' && this.playing && this.ctx?.state === 'running' && this.analyser) {
        time = this.getPlaybackProgress()?.currentTime ?? 0;
        output.dataset.engine = JSON.stringify({ playing: this.playing, mode: this.mode, context: this.ctx.state, time });
      }
    };
    AudioAnalysisEngine.prototype.update = function (audio, dt) {
      const frame = originalAnalysisUpdate.call(this, audio, dt);
      if (audio.mode === 'backend' && audio.playing && audio.ctx?.state === 'running' && time > 2.6) {
        let hash = 2166136261, dominant = 0;
        for (let i = 0; i < frame.spectrum.length; i++) {
          hash = Math.imul(hash ^ Math.round(frame.spectrum[i] * 255), 16777619);
          if (frame.spectrum[i] > frame.spectrum[dominant]) dominant = i;
        }
        spectrumHashes.add(hash); dominantBins.add(dominant);
        dominantHz.add(Math.round(dominant * audio.ctx.sampleRate / (frame.spectrum.length * 2)));
        const rms = Math.sqrt(frame.timeDomain.reduce((sum, value) => sum + value * value, 0) / frame.timeDomain.length);
        maxRms = Math.max(maxRms, rms); minRms = Math.min(minRms, rms); samples++;
        output.dataset.analysis = JSON.stringify({ samples, hashes: spectrumHashes.size, bins: [...dominantBins], frequencies: [...dominantHz], maxRms, minRms });
      }
      return frame;
    };
  });
  const content = page.waitForResponse(response => response.url().endsWith('/v1/songs/visualizer-tone/content'));
  await enable.focus(); await page.keyboard.press('Enter');
  expect((await content).status()).toBe(200);
  await expect(page.locator('.status')).toHaveText('On air', { timeout: 10000 });
  await expect(page.locator('#np-title')).toHaveText(song.title);
  const readAnalysis = () => page.locator('#qa-live-audio').evaluate(el => JSON.parse((el as HTMLElement).dataset.analysis || '{}'));
  await expect.poll(async () => (await readAnalysis()).samples ?? 0, { timeout: 12000 }).toBeGreaterThan(30);
  await expect.poll(async () => (await readAnalysis()).bins?.length ?? 0).toBeGreaterThan(1);
  // An idle synthetic spectrum could also vary. Require the two frequencies
  // encoded in our WAV to appear in the actual scene's analysis frames.
  await expect.poll(async () => {
    const frequencies: number[] = (await readAnalysis()).frequencies ?? [];
    return frequencies.some(hz => hz > 150 && hz < 300)
      && frequencies.some(hz => hz > 1100 && hz < 1500);
  }, { timeout: 8000 }).toBe(true);
  const analysis = await readAnalysis();
  expect(analysis.hashes).toBeGreaterThan(10);
  expect(analysis.maxRms).toBeGreaterThan(0.05);
  expect(analysis.maxRms - analysis.minRms).toBeGreaterThan(0.03);
  const engine = await page.locator('#qa-live-audio').evaluate(el => JSON.parse((el as HTMLElement).dataset.engine!));
  expect(engine).toMatchObject({ playing: true, mode: 'backend', context: 'running' });
  const time = engine.time;
  await expect.poll(async () => page.locator('#qa-live-audio').evaluate(el => JSON.parse((el as HTMLElement).dataset.engine!).time)).toBeGreaterThan(time + 0.5);
  expect(errors).toEqual([]);
});

test('analysis tap detaches its input on rebind and repeated disposal without disconnecting playback', async ({ page }) => {
  const errors = recordErrors(page);
  await station(page);
  await page.goto('/scenes');
  const result = await page.evaluate(async () => {
    const path = '/src/audio/AudioAnalysisEngine.ts';
    const { AudioAnalysisEngine } = await import(path) as typeof import('../src/audio/AudioAnalysisEngine');
    const ctx = new AudioContext();
    const sources = [ctx.createAnalyser(), ctx.createAnalyser()];
    const calls: { index: number; targeted: boolean }[] = [];
    for (const [index, source] of sources.entries()) {
      source.connect(ctx.destination);
      const native = source.disconnect.bind(source);
      source.disconnect = ((...args: unknown[]) => {
        calls.push({ index, targeted: args[0] instanceof AnalyserNode });
        // Call the exact native overload with the observed destination node.
        if (args[0] instanceof AudioNode) native(args[0]);
        else native();
      }) as typeof source.disconnect;
    }
    const audio = {
      ctx, analyser: sources[0], playing: false, spectrum: new Float32Array(512),
      bands: { bass: 0, mids: 0, highs: 0, volume: 0 }, beat: false, beatStrength: 0,
    } as import('../src/audio/AudioEngine').AudioEngine;
    const analysis = new AudioAnalysisEngine();
    try {
      analysis.update(audio);
      audio.analyser = sources[1];
      analysis.update(audio);
      analysis.dispose(); analysis.dispose();
      return calls;
    } finally {
      analysis.dispose();
      await ctx.close();
    }
  });
  expect(result).toEqual([{ index: 0, targeted: true }, { index: 1, targeted: true }]);
  expect(errors).toEqual([]);
});

test('oscilloscope preview pixels follow the selected ramp and its scene URLs', async ({ page }) => {
  const errors = recordErrors(page);
  await page.goto('/scenes');
  const card = page.getByRole('article').filter({ has: page.getByRole('heading', { name: 'Oscilloscope', exact: true }) });
  const captures = [];
  for (const ramp of ['EMBER', 'NEON']) {
    await page.getByLabel('Colour ramp').selectOption(ramp);
    await expect(card.getByRole('link', { name: 'Open scene' })).toHaveAttribute('href', `/spectrum?ramp=${ramp}`);
    await expect(card.getByRole('link', { name: 'Overlay' })).toHaveAttribute('href', `/spectrum-overlay?ramp=${ramp}`);
    captures.push(await card.locator('svg').evaluate(async svg => {
      const image = new Image();
      const url = URL.createObjectURL(new Blob([new XMLSerializer().serializeToString(svg)], { type: 'image/svg+xml' }));
      try {
        image.src = url; await image.decode();
        const canvas = document.createElement('canvas'); canvas.width = 480; canvas.height = 240;
        const ctx = canvas.getContext('2d')!; ctx.drawImage(image, 0, 0, 480, 240);
        const pixels = ctx.getImageData(0, 0, 480, 240).data;
        let hash = 2166136261, bias = 0, coloured = 0;
        for (let i = 0; i < pixels.length; i += 4) {
          for (let c = 0; c < 4; c++) hash = Math.imul(hash ^ pixels[i + c], 16777619);
          // Measure opaque luminous traces, not the unchanged translucent
          // blue haze or calibration grid behind them.
          if (pixels[i + 3] > 150 && Math.max(pixels[i], pixels[i + 1], pixels[i + 2]) > 150) {
            bias += (pixels[i] - pixels[i + 2]) / Math.max(1, pixels[i] + pixels[i + 1] + pixels[i + 2]);
            coloured++;
          }
        }
        return { hash: hash >>> 0, bias: bias / Math.max(1, coloured), coloured };
      } finally { URL.revokeObjectURL(url); }
    }));
  }
  expect(captures[0].hash).not.toBe(captures[1].hash);
  expect(captures[0].coloured).toBeGreaterThan(100);
  expect(captures[0].bias).toBeGreaterThan(captures[1].bias + 0.08);
  expect(errors).toEqual([]);
});

async function sceneImage(page: Page) {
  const box = await page.locator('.performance-space').boundingBox();
  expect(box, 'dedicated visible performance frame').not.toBeNull();
  expect(box!.width).toBeGreaterThan(80); expect(box!.height).toBeGreaterThan(60);
  // Exclude frame labels/edges, the track progress, and live clock.
  const clip = { x: box!.x + box!.width * 0.1, y: box!.y + box!.height * 0.1,
    width: box!.width * 0.8, height: box!.height * 0.8 };
  return page.screenshot({ clip, animations: 'disabled' });
}

async function colouredImagePixels(page: Page, png: Buffer) {
  return page.evaluate(async base64 => {
    const image = new Image(); image.src = `data:image/png;base64,${base64}`;
    await image.decode();
    const canvas = document.createElement('canvas'); canvas.width = image.width; canvas.height = image.height;
    const ctx = canvas.getContext('2d')!; ctx.drawImage(image, 0, 0);
    const pixels = ctx.getImageData(0, 0, canvas.width, canvas.height).data;
    let count = 0;
    for (let i = 0; i < pixels.length; i += 4) {
      const max = Math.max(pixels[i], pixels[i + 1], pixels[i + 2]);
      const min = Math.min(pixels[i], pixels[i + 1], pixels[i + 2]);
      if (max > 55 && max - min > 20) count++;
    }
    return count;
  }, png.toString('base64'));
}

for (const slug of ['', 'spectrum', 'line']) {
  test(`${slug || 'ambient'}: reduced-motion art stays still through real playback and survives resize`, async ({ page }) => {
    const errors = recordErrors(page);
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await page.setViewportSize({ width: 1000, height: 800 });
    await station(page, true);
    await page.goto(`/${slug}?ramp=NEON`);
    await paintedFrame(page);
    expect(await colouredImagePixels(page, await sceneImage(page)), 'initial still must contain coloured scene art').toBeGreaterThan(30);
    await page.getByRole('button', { name: 'Enable audio' }).click();
    await expect(page.locator('.status')).toHaveText('On air', { timeout: 10000 });
    await paintedFrame(page);
    const hash = (png: Buffer) => createHash('sha256').update(png).digest('hex');
    const first = await sceneImage(page);
    expect(await colouredImagePixels(page, first)).toBeGreaterThan(30);
    await page.waitForTimeout(350);
    expect(hash(await sceneImage(page))).toBe(hash(first));
    await page.setViewportSize({ width: 740, height: 560 });
    await paintedFrame(page);
    const resized = await sceneImage(page);
    expect(await colouredImagePixels(page, resized), 'resize must redraw rather than clear the still').toBeGreaterThan(30);
    await page.waitForTimeout(350);
    expect(hash(await sceneImage(page))).toBe(hash(resized));
    const progress = Number(await page.getByRole('progressbar').getAttribute('aria-valuenow'));
    await expect.poll(async () => Number(await page.getByRole('progressbar').getAttribute('aria-valuenow'))).toBeGreaterThan(progress);
    expect(errors).toEqual([]);
  });
}

test('blocked mobile ambient and landscape equaliser fit real artwork below the unlock card and above the deck', async ({ page }) => {
  const errors = recordErrors(page);
  await station(page, true);
  for (const [route, viewport] of [
    ['/?ramp=NEON', { width: 320, height: 740 }],
    ['/bars?ramp=EMBER', { width: 740, height: 360 }],
  ] as const) {
    await page.setViewportSize(viewport);
    await page.goto(route);
    const enable = page.getByRole('button', { name: 'Enable audio' });
    await expect(enable).toBeVisible(); await paintedFrame(page);
    const fit = async () => {
      const canvas = await page.locator('#stage canvas').boundingBox();
      const frame = await page.locator('.performance-space').boundingBox();
      const message = await page.locator('.player-message').count()
        ? await page.locator('.player-message').boundingBox() : null;
      const deck = await page.locator('.scene-switcher').boundingBox();
      expect(canvas).not.toBeNull(); expect(frame).not.toBeNull(); expect(deck).not.toBeNull();
      expect(canvas!.y).toBeGreaterThanOrEqual(frame!.y - 1);
      expect(canvas!.y + canvas!.height).toBeLessThanOrEqual(deck!.y + 1);
      if (message) expect(canvas!.y).toBeGreaterThanOrEqual(message.y + message.height - 1);
      expect(await colouredImagePixels(page, await page.locator('#stage canvas').screenshot())).toBeGreaterThan(30);
    };
    await fit();
    await enable.click(); await expect(page.locator('.status')).toHaveText('On air');
    await paintedFrame(page); await fit();
  }
  expect(errors).toEqual([]);
});

for (const viewport of [{ width: 320, height: 740 }, { width: 740, height: 360 }]) {
  test(`scene frame, navigation and deck do not overlap at ${viewport.width}×${viewport.height}`, async ({ page }) => {
    const errors = recordErrors(page);
    await station(page);
    await page.setViewportSize(viewport);
    await page.goto('/?ramp=EMBER');
    for (const composition of compositions) {
      if (composition.slug) {
        const link = page.getByRole('group', { name: 'Scene selection' }).getByRole('link', { name: composition.label, exact: true });
        await link.click();
        await expect(page).toHaveURL(new RegExp(`/${composition.slug}\\?ramp=EMBER$`));
        await expect(link).toHaveAttribute('aria-current', 'page');
      }
      await paintedFrame(page);
      // Force an actual resize after each scene's initial render, not just a
      // viewport set before construction. Keep it narrow/short in both sizes.
      await page.setViewportSize({ width: viewport.width - 1, height: viewport.height - 1 });
      await paintedFrame(page);
      const layout = await page.evaluate(() => {
        const rect = (selector: string) => {
          const element = document.querySelector(selector);
          if (!element) throw new Error(`Missing player layout region ${selector}`);
          const r = element.getBoundingClientRect();
          return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, width: r.width, height: r.height };
        };
        return { frame: rect('.performance-space'), deck: rect('.player-deck'),
          scenes: rect('.scene-switcher'), header: rect('.session-header'), canvas: rect('#stage canvas'),
          width: innerWidth, height: innerHeight, scrollWidth: document.documentElement.scrollWidth };
      });
      expect(layout.scrollWidth).toBeLessThanOrEqual(layout.width);
      expect(layout.frame.height, `${composition.label} usable scene area`).toBeGreaterThan(35);
      expect(layout.header.bottom).toBeLessThanOrEqual(layout.frame.top + 1);
      // Check the actual bitmap boundary, not just separated HTML regions:
      // artwork cannot be painted underneath the header, switcher, or deck.
      expect(layout.canvas.top).toBeGreaterThanOrEqual(layout.frame.top - 1);
      expect(layout.canvas.bottom).toBeLessThanOrEqual(layout.frame.bottom + 1);
      expect(layout.canvas.left).toBeGreaterThanOrEqual(layout.frame.left - 1);
      expect(layout.canvas.right).toBeLessThanOrEqual(layout.frame.right + 1);
      expect(layout.frame.bottom, `${composition.label} scene above navigation`).toBeLessThanOrEqual(layout.scenes.top + 1);
      expect(layout.scenes.bottom, `${composition.label} navigation above deck`).toBeLessThanOrEqual(layout.deck.top + 1);
      for (const region of [layout.frame, layout.scenes, layout.deck]) {
        expect(region.left).toBeGreaterThanOrEqual(-1); expect(region.right).toBeLessThanOrEqual(layout.width + 1);
        expect(region.top).toBeGreaterThanOrEqual(0); expect(region.bottom).toBeLessThanOrEqual(layout.height + 1);
      }
      await page.setViewportSize(viewport);
    }
    expect(errors).toEqual([]);
  });
}
