/* Tests for the scene lifecycle shared by every visualizer.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 *
 * The Pixi app is faked down to a renderer size and a stage, so no GPU or DOM
 * is needed: Graphics only record instructions until a renderer draws them.
 * The bloom filter needs a DOM, and WaveScene already runs without it.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as PIXI from 'pixi.js';
import type { AudioEngine } from '../audio/AudioEngine.ts';
import { SCENES } from './scenes.ts';
import { LofiRainVisualizer } from './LofiRainVisualizer.ts';
import type { Visualizer } from './types.ts';

const CYCLES = 5;
const DT = 1 / 60;

// Ribbon's FillGradient paints its ramp into a 2D canvas, which node lacks.
// A no-op stub is enough: the test never renders the texture.
const fakeCanvas2d = {
  fillStyle: null as unknown,
  createLinearGradient: () => ({ addColorStop() { /* no pixels to colour */ } }),
  fillRect() { /* nothing to paint */ },
};
PIXI.DOMAdapter.set({
  ...PIXI.DOMAdapter.get(),
  createCanvas: (width = 1, height = 1) =>
    ({ width, height, getContext: () => fakeCanvas2d }) as unknown as HTMLCanvasElement,
});

/** Analyser fake: every frequency bin reports a mid level, the waveform is flat. */
class FakeAnalyserNode {
  fftSize = 0;
  smoothingTimeConstant = 0;
  getByteFrequencyData(out: Uint8Array) {
    out.fill(140);
  }
  getByteTimeDomainData(out: Uint8Array) {
    out.fill(128);
  }
  disconnect() { /* nothing to release */ }
}

function fakeAudio(): AudioEngine {
  const fake = {
    ctx: { sampleRate: 44100, createAnalyser: () => new FakeAnalyserNode() },
    analyser: { connect() { /* tap */ }, disconnect() { /* untap */ } },
    audible: true,
    spectrum: new Float32Array(512),
    bands: { bass: 0, mids: 0, highs: 0, volume: 0 },
    beat: false, beatStrength: 0,
  };
  return fake as unknown as AudioEngine;
}

function fakeApp() {
  const app = { renderer: { width: 1280, height: 720, resolution: 1 }, stage: new PIXI.Container() };
  return app as unknown as PIXI.Application;
}

function graphicsContexts(node: PIXI.Container, found: PIXI.GraphicsContext[] = []) {
  if (node instanceof PIXI.Graphics) found.push(node.context);
  for (const child of node.children) graphicsContexts(child, found);
  return found;
}

/** Disposes the scene and asserts every context it drew into went with it. */
function assertDisposedClean(app: PIXI.Application, scene: Visualizer) {
  const contexts = graphicsContexts(app.stage);
  assert.ok(contexts.length > 0, 'scene mounted no Graphics');
  scene.dispose();
  const live = contexts.filter((context) => !context.destroyed).length;
  assert.equal(live, 0, `${live} of ${contexts.length} GraphicsContexts outlived dispose`);
  assert.equal(app.stage.children.length, 0);
}

const builders = [
  ...SCENES.map((def) => ({
    name: def.slug,
    create: (app: PIXI.Application) =>
      def.create(app, fakeAudio(), { showBackground: true, transparent: false }),
  })),
  { name: 'lofi-rain', create: (app: PIXI.Application) => new LofiRainVisualizer(app, fakeAudio()) },
];

for (const { name, create } of builders) {
  test(`${name}: dispose destroys every GraphicsContext across scene switches`, () => {
    const app = fakeApp();
    for (let cycle = 0; cycle < CYCLES; cycle++) {
      const scene = create(app);
      scene.update(DT);
      scene.update(DT);
      assertDisposedClean(app, scene);
    }
  });

  test(`${name}: dispose before the first update destroys every GraphicsContext`, () => {
    const app = fakeApp();
    assertDisposedClean(app, create(app));
  });
}

test('lofi-rain mounts its platter before the first update', () => {
  const app = fakeApp();
  const scene = new LofiRainVisualizer(app, fakeAudio());
  // root -> bg, art -> platter, gfx: the platter is reachable without a resize.
  assert.equal(graphicsContexts(app.stage).length, 3);
  assertDisposedClean(app, scene);
});
