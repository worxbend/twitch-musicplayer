/* Tests for the crossfading audio engine.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 *
 * The Web Audio graph and <audio> elements are replaced with small fakes.
 * FakeAudioParam enforces the spec rule that matters here: an automation
 * event inside an active setValueCurveAtTime range throws NotSupportedError.
 */
import { afterEach, beforeEach, mock, test } from 'node:test';
import assert from 'node:assert/strict';
import type { AudioEngine as Engine } from './AudioEngine';

interface ScheduledCurve {
  start: number;
  end: number;
}

class FakeAudioParam {
  value = 0;
  private curves: ScheduledCurve[] = [];

  // Spec: drops events at or after t, plus any curve still running at t.
  cancelScheduledValues(t: number) {
    this.curves = this.curves.filter(curve => curve.start < t && curve.end <= t);
  }

  setValueAtTime(value: number, t: number) {
    this.assertOutsideCurves(t, t);
    this.value = value;
  }

  linearRampToValueAtTime(value: number, t: number) {
    this.assertOutsideCurves(t, t);
    this.value = value;
  }

  setValueCurveAtTime(curve: Float32Array, t: number, duration: number) {
    this.assertOutsideCurves(t, t + duration);
    this.curves.push({ start: t, end: t + duration });
    this.value = curve[curve.length - 1];
  }

  private assertOutsideCurves(from: number, to: number) {
    const overlaps = this.curves.some(curve => curve.start <= to && from < curve.end);
    if (overlaps) throw new DOMException('overlap', 'NotSupportedError');
  }
}

const graphNode = () => ({ connect() {}, disconnect() {} });

class FakeAnalyser {
  fftSize = 2048;
  smoothingTimeConstant = 0;
  get frequencyBinCount() {
    return this.fftSize / 2;
  }
  getByteFrequencyData(_bins: Uint8Array) {}
  connect() {}
  disconnect() {}
}

class FakeAudioContext {
  static last: FakeAudioContext | null = null;
  currentTime = 0;
  state = 'running';
  destination = graphNode();
  analyser = new FakeAnalyser();

  constructor() {
    FakeAudioContext.last = this;
  }

  createAnalyser() {
    return this.analyser;
  }

  createGain() {
    return { gain: new FakeAudioParam(), ...graphNode() };
  }

  createMediaElementSource() {
    return graphNode();
  }

  async resume() {}
  async close() {
    this.state = 'closed';
  }
}

class FakeAudioElement {
  autoplay = false;
  crossOrigin = '';
  preload = '';
  src = '';
  paused = true;
  ended = false;
  loop = false;
  currentTime = 0;
  duration = Number.NaN;
  onended: (() => void) | null = null;
  onerror: (() => void) | null = null;

  play() {
    this.paused = false;
    this.ended = false;
    return Promise.resolve();
  }

  pause() {
    this.paused = true;
  }

  load() {}

  removeAttribute(name: string) {
    if (name === 'src') this.src = '';
  }

  addEventListener() {}
  removeEventListener() {}
}

Object.assign(globalThis, {
  Audio: FakeAudioElement,
  window: {
    AudioContext: FakeAudioContext,
    setTimeout: (fn: () => void, ms?: number) => globalThis.setTimeout(fn, ms),
    clearTimeout: (id?: ReturnType<typeof setTimeout>) => globalThis.clearTimeout(id),
  },
});

const { AudioEngine } = await import('./AudioEngine.ts');

const FADE_MS = 2500;

let engine: Engine;

beforeEach(() => {
  mock.timers.enable({ apis: ['setTimeout'] });
  engine = new AudioEngine({ allowAutoplay: true });
});

afterEach(() => {
  engine.dispose();
  mock.timers.reset();
});

function flush() {
  return new Promise<void>(resolve => setImmediate(resolve));
}

function ctx() {
  assert.ok(FakeAudioContext.last, 'audio context was not created');
  return FakeAudioContext.last;
}

function activeElement() {
  const el = engine['_activeChannel']?.audioEl as unknown as FakeAudioElement | undefined;
  assert.ok(el, 'no active channel');
  return el;
}

// Plays the first track to the end of its fade-in, leaving the context clock at 10 s.
async function playFirstTrack() {
  const first = engine.playStream('/stream/a', 'A');
  await flush();
  ctx().currentTime = 10;
  mock.timers.tick(FADE_MS);
  await first;
  return activeElement();
}

test('a second track change during a crossfade does not throw', async () => {
  await playFirstTrack();
  const second = engine.playStream('/stream/b', 'B');
  await flush();

  // Still inside the A->B curves: the incoming channel is the one fading out.
  const third = engine.playStream('/stream/c', 'C');
  await flush();
  ctx().currentTime = 20;
  mock.timers.tick(FADE_MS);
  await flush();

  await third;
  await second;
  assert.equal(activeElement().src, '/stream/c');
  assert.equal(activeElement().paused, false);
  assert.equal(engine.playing, true);
});

test('the outgoing channel stops while its fade curve is still running', async () => {
  const first = await playFirstTrack();
  const second = engine.playStream('/stream/b', 'B');
  await flush();

  // The wall-clock timer fires while the context clock is still inside the curve.
  mock.timers.tick(FADE_MS);
  await flush();

  await second;
  assert.equal(first.paused, true);
  assert.equal(first.src, '');
  assert.equal(activeElement().src, '/stream/b');
  assert.equal(activeElement().paused, false);
});

test('visuals fall back to the synth once the active track ends', async () => {
  await playFirstTrack();
  const readSpectrum = mock.method(ctx().analyser, 'getByteFrequencyData');

  engine.update(0.016);
  assert.equal(readSpectrum.mock.callCount(), 1);

  activeElement().ended = true;
  engine.update(0.016);
  assert.equal(readSpectrum.mock.callCount(), 1);
});

// Swaps the next channel's play() for one the test settles by hand.
function holdNextPlay() {
  const el = engine['_nextChannel']().audioEl as unknown as FakeAudioElement;
  let settle!: { resolve: () => void; reject: (error: unknown) => void };
  mock.method(el, 'play', () => {
    el.paused = false;
    return new Promise<void>((resolve, reject) => { settle = { resolve, reject }; });
  });
  return () => settle;
}

test('onStarted fires once when play() resolves, before the crossfade ends', async () => {
  await playFirstTrack();
  const onStarted = mock.fn();
  const second = engine.playStream('/stream/b', 'B', { onStarted });
  await flush();

  assert.equal(onStarted.mock.callCount(), 1);
  mock.timers.tick(FADE_MS);
  await second;
  assert.equal(onStarted.mock.callCount(), 1);
});

test('a superseded playStream does not fire onStarted', async () => {
  await playFirstTrack();
  const settleSecond = holdNextPlay();
  const superseded = mock.fn();
  const second = engine.playStream('/stream/b', 'B', { onStarted: superseded });
  await flush();

  const newer = mock.fn();
  const third = engine.playStream('/stream/c', 'C', { onStarted: newer });
  await flush();
  settleSecond().resolve();
  await flush();
  mock.timers.tick(FADE_MS);
  await Promise.all([second, third]);

  assert.equal(superseded.mock.callCount(), 0);
  assert.equal(newer.mock.callCount(), 1);
});

test('a play() rejection does not fire onStarted', async () => {
  await playFirstTrack();
  const settleSecond = holdNextPlay();
  const onStarted = mock.fn();
  const second = engine.playStream('/stream/b', 'B', { onStarted });
  await flush();

  settleSecond().reject(new DOMException('blocked', 'NotAllowedError'));
  await assert.rejects(second, { name: 'NotAllowedError' });
  assert.equal(onStarted.mock.callCount(), 0);
});
