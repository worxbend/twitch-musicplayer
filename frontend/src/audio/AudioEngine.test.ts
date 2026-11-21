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

  // Jumps straight to the target; the fakes have no running clock.
  setTargetAtTime(value: number, t: number, _timeConstant: number) {
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

class FakeNode {
  connections: unknown[] = [];
  disconnected = false;
  connect(target: unknown) {
    this.connections.push(target);
  }
  disconnect() {
    this.disconnected = true;
  }
}

class FakeGain extends FakeNode {
  gain = new FakeAudioParam();
}

class FakeAnalyser extends FakeNode {
  fftSize = 2048;
  smoothingTimeConstant = 0;
  get frequencyBinCount() {
    return this.fftSize / 2;
  }
  getByteFrequencyData(_bins: Uint8Array) {}
}

class FakeAudioContext {
  static last: FakeAudioContext | null = null;
  currentTime = 0;
  state = 'running';
  destination = new FakeNode();
  analyser = new FakeAnalyser();

  constructor() {
    FakeAudioContext.last = this;
  }

  createAnalyser() {
    return this.analyser;
  }

  createGain() {
    return new FakeGain();
  }

  createMediaElementSource() {
    return new FakeNode();
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

  loads = 0;
  load() {
    this.loads++;
  }

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

function master() {
  const node = engine['_master'] as unknown as FakeGain | null;
  assert.ok(node, 'master gain was not created');
  return node;
}

function channelGains() {
  return [engine['_channelA'], engine['_channelB']].map(channel => channel?.gain.gain.value);
}

test('the analyser feeds the master gain, which feeds the destination', async () => {
  await playFirstTrack();
  assert.deepEqual(ctx().analyser.connections, [master()]);
  assert.deepEqual(master().connections, [ctx().destination]);
});

test('a volume set before the context exists is applied when it is created', async () => {
  engine.setVolume(0.3);
  await playFirstTrack();
  assert.equal(master().gain.value, 0.3);
});

test('volume 0 silences the master only; channels and the analyser keep the signal', async () => {
  await playFirstTrack();
  const before = channelGains();
  const readSpectrum = mock.method(ctx().analyser, 'getByteFrequencyData');

  engine.setVolume(0);
  engine.update(0.016);

  assert.equal(master().gain.value, 0);
  assert.equal(engine.volume, 0);
  assert.deepEqual(channelGains(), before);
  assert.equal(readSpectrum.mock.callCount(), 1);
});

test('setVolume clamps out-of-range values and treats NaN as full volume', async () => {
  await playFirstTrack();
  engine.setVolume(5);
  assert.equal(master().gain.value, 1);
  engine.setVolume(-1);
  assert.equal(master().gain.value, 0);
  engine.setVolume(Number.NaN);
  assert.equal(engine.volume, 1);
});

test('dispose disconnects the master gain', async () => {
  await playFirstTrack();
  const node = master();
  engine.dispose();
  assert.equal(node.disconnected, true);
  assert.equal(engine['_master'], null);
});

test('play and pause are idempotent and togglePlay still flips state', async () => {
  const el = await playFirstTrack();
  const play = mock.method(el, 'play');
  const pause = mock.method(el, 'pause');

  assert.equal(engine.pause(), false);
  assert.equal(engine.pause(), false);
  assert.equal(pause.mock.callCount(), 1);

  assert.equal(engine.play(), true);
  assert.equal(engine.play(), true);
  assert.equal(play.mock.callCount(), 1);

  assert.equal(engine.togglePlay(), false);
  assert.equal(el.paused, true);
  assert.equal(engine.togglePlay(), true);
  assert.equal(el.paused, false);
});

function inactiveElement() {
  const active = engine['_activeChannel'];
  const other = active === engine['_channelA'] ? engine['_channelB'] : engine['_channelA'];
  const el = other?.audioEl as unknown as FakeAudioElement | undefined;
  assert.ok(el, 'no inactive channel');
  return el;
}

test('preloadNext loads straight into the inactive channel when no fade runs', async () => {
  const first = await playFirstTrack();
  engine.preloadNext('/stream/b');

  assert.equal(inactiveElement().src, '/stream/b');
  assert.equal(first.src, '/stream/a');
  assert.equal(first.paused, false);
});

test('preloadNext waits for the fading-out channel, then the next play reuses it', async () => {
  const first = await playFirstTrack();
  const second = engine.playStream('/stream/b', 'B');
  await flush();

  // A short track asks for the next song while track A is still fading out.
  const pause = mock.method(first, 'pause');
  engine.preloadNext('/stream/c');
  assert.equal(first.src, '/stream/a');
  assert.equal(pause.mock.callCount(), 0);

  mock.timers.tick(FADE_MS);
  await second;
  assert.equal(first.src, '/stream/c');

  const loadsBefore = first.loads;
  const third = engine.playStream('/stream/c', 'C');
  await flush();
  mock.timers.tick(FADE_MS);
  await third;
  assert.equal(first.loads, loadsBefore);
  assert.equal(activeElement(), first);
});

test('a new play that claims the fading-out channel drops the waiting preload', async () => {
  const first = await playFirstTrack();
  const second = engine.playStream('/stream/b', 'B');
  await flush();
  engine.preloadNext('/stream/c');

  const third = engine.playStream('/stream/d', 'D');
  await flush();
  mock.timers.tick(FADE_MS);
  await flush();
  await Promise.all([second, third]);

  assert.equal(activeElement(), first);
  assert.equal(first.src, '/stream/d');
  assert.equal(inactiveElement().src, '');
});
