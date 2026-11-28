/* Tests for the analysis layer between the audio engine and the visualizers.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 *
 * The engine is faked down to the fields AudioAnalysisEngine reads. The live
 * path gets a fake context and analyser whose byte data is a constant level
 * the test sets, so band envelopes can be checked frame by frame.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { AudioEngine } from './AudioEngine.ts';
import { AudioAnalysisEngine, PEAK_FALL_PER_SEC } from './AudioAnalysisEngine.ts';

class FakeTapNode {
  fftSize = 0;
  smoothingTimeConstant = 0;
  disconnects = 0;
  private readonly source: FakeSourceAnalyser;
  constructor(source: FakeSourceAnalyser) {
    this.source = source;
  }
  getByteFrequencyData(out: Uint8Array) {
    out.fill(this.source.level);
  }
  getByteTimeDomainData(out: Uint8Array) {
    out.fill(128);
  }
  disconnect() {
    this.disconnects++;
  }
}

class FakeSourceAnalyser {
  /** Byte value every frequency bin reports. */
  level = 0;
  connected: unknown[] = [];
  disconnected: unknown[] = [];
  connect(node: unknown) {
    this.connected.push(node);
  }
  disconnect(node: unknown) {
    this.disconnected.push(node);
  }
}

class FakeContext {
  sampleRate = 44100;
  created: FakeTapNode[] = [];
  private readonly source: FakeSourceAnalyser;
  constructor(source: FakeSourceAnalyser) {
    this.source = source;
  }
  createAnalyser() {
    const node = new FakeTapNode(this.source);
    this.created.push(node);
    return node;
  }
}

function liveEngine() {
  const analyser = new FakeSourceAnalyser();
  const ctx = new FakeContext(analyser);
  const fake = {
    ctx, analyser, audible: true,
    spectrum: new Float32Array(512),
    bands: { bass: 0, mids: 0, highs: 0, volume: 0 },
    beat: false, beatStrength: 0,
  };
  return { fake, ctx, analyser, audio: fake as unknown as AudioEngine };
}

function idleEngine() {
  const fake = {
    ctx: null, analyser: null, audible: false,
    spectrum: new Float32Array(512).fill(0.4),
    bands: { bass: 0, mids: 0, highs: 0, volume: 0.3 },
    beat: true, beatStrength: 0.7,
  };
  return fake as unknown as AudioEngine;
}

function run(analysis: AudioAnalysisEngine, audio: AudioEngine, fps: number, secs: number) {
  const frames = Math.round(secs * fps);
  for (let i = 0; i < frames; i++) analysis.update(audio, 1 / fps);
  return analysis.update(audio, 0);
}

function assertClose(a: Float32Array, b: Float32Array, tol: number) {
  assert.equal(a.length, b.length);
  for (let i = 0; i < a.length; i++) {
    assert.ok(Math.abs(a[i] - b[i]) < tol, `index ${i}: ${a[i]} vs ${b[i]}`);
  }
}

test('live bands rise and release alike at 30 and 144 fps', () => {
  const at = (fps: number) => {
    const { audio, analyser } = liveEngine();
    const analysis = new AudioAnalysisEngine();
    analyser.level = 140;
    const risen = Float32Array.from(run(analysis, audio, fps, 0.5).bands);
    analyser.level = 0;
    // 1/3 s (10 and 48 frames) is about three release time constants: a
    // per-frame release would leave the 30 fps bars visibly higher.
    const released = Float32Array.from(run(analysis, audio, fps, 1 / 3).bands);
    return { risen, released };
  };
  const slow = at(30);
  const fast = at(144);
  assertClose(slow.risen, fast.risen, 1e-3);
  assertClose(slow.released, fast.released, 1e-3);
  assert.ok(slow.released[0] > 0 && slow.released[0] < slow.risen[0]);
});

test('idle bands settle alike at 30 and 144 fps and mirror the engine beat', () => {
  const slow = run(new AudioAnalysisEngine(), idleEngine(), 30, 0.5);
  const slowBands = Float32Array.from(slow.bands);
  const fast = run(new AudioAnalysisEngine(), idleEngine(), 144, 0.5);
  assertClose(slowBands, fast.bands, 1e-3);
  assert.equal(fast.beat, true);
  assert.equal(fast.beatStrength, 0.7);
});

test('peak caps fall at PEAK_FALL_PER_SEC once the bands drop', () => {
  const { audio, analyser } = liveEngine();
  const analysis = new AudioAnalysisEngine();
  analyser.level = 255;
  run(analysis, audio, 60, 0.5);
  analyser.level = 0;
  const frame = run(analysis, audio, 60, 0.2);
  const expected = 1 - PEAK_FALL_PER_SEC * 0.2;
  for (let i = 0; i < frame.bandPeaks.length; i++) {
    assert.ok(Math.abs(frame.bandPeaks[i] - expected) < 1e-5, `peak ${i}: ${frame.bandPeaks[i]}`);
    assert.ok(frame.bands[i] < frame.bandPeaks[i]);
  }
});

test('the analysis tap is created once and disconnected on dispose', () => {
  const { audio, ctx, analyser } = liveEngine();
  const analysis = new AudioAnalysisEngine();
  run(analysis, audio, 60, 0.1);

  assert.equal(ctx.created.length, 1);
  const tap = ctx.created[0];
  assert.deepEqual(analyser.connected, [tap]);
  assert.equal(tap.fftSize, 2048);

  analysis.dispose();
  assert.deepEqual(analyser.disconnected, [tap]);
  assert.equal(tap.disconnects, 1);
  analysis.dispose();
  assert.equal(tap.disconnects, 1);
});

test('series have the configured lengths', () => {
  const analysis = new AudioAnalysisEngine({ bands: 24, wavePoints: 100 });
  const frame = analysis.update(idleEngine(), 1 / 60);
  assert.equal(analysis.bandCount, 24);
  assert.equal(analysis.waveCount, 100);
  assert.equal(frame.bands.length, 24);
  assert.equal(frame.bandPeaks.length, 24);
  assert.equal(frame.waveMin.length, 100);
  assert.equal(frame.waveMax.length, 100);
});
