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

  resumeCalls = 0;
  resumeError: Error | null = null;
  resume() {
    this.resumeCalls++;
    if (this.resumeError) return Promise.reject(this.resumeError);
    this.state = 'running';
    return Promise.resolve();
  }

  closeError: Error | null = null;
  close() {
    if (this.closeError) return Promise.reject(this.closeError);
    this.state = 'closed';
    return Promise.resolve();
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
  playbackRate = 1;
  onended: (() => void) | null = null;
  onerror: (() => void) | null = null;
  error: { code: number } | null = null;

  play() {
    this.paused = false;
    this.ended = false;
    this.dispatch('play');
    return Promise.resolve();
  }

  pause() {
    if (this.paused) return;
    this.paused = true;
    this.dispatch('pause');
  }

  loads = 0;
  // Like the browser, load() clears a previous media error.
  load() {
    this.loads++;
    this.error = null;
  }

  removeAttribute(name: string) {
    if (name === 'src') this.src = '';
  }

  private listeners = new Map<string, Set<() => void>>();
  addEventListener(type: string, fn: () => void) {
    if (!this.listeners.has(type)) this.listeners.set(type, new Set());
    this.listeners.get(type)!.add(fn);
  }
  removeEventListener(type: string, fn: () => void) {
    this.listeners.get(type)?.delete(fn);
  }
  dispatch(type: string) {
    for (const fn of this.listeners.get(type) ?? []) fn();
  }
}

Object.assign(globalThis, {
  Audio: FakeAudioElement,
  window: {
    AudioContext: FakeAudioContext,
    setTimeout: (fn: () => void, ms?: number) => globalThis.setTimeout(fn, ms),
    clearTimeout: (id?: ReturnType<typeof setTimeout>) => globalThis.clearTimeout(id),
    setInterval: (fn: () => void, ms?: number) => globalThis.setInterval(fn, ms),
    clearInterval: (id?: ReturnType<typeof setInterval>) => globalThis.clearInterval(id),
  },
});

const { AudioEngine, PLAY_START_TIMEOUT_MS, STALL_TIMEOUT_MS, STALL_CHECK_MS } = await import('./AudioEngine.ts');

const FADE_MS = 2500;

let engine: Engine;

beforeEach(() => {
  mock.timers.enable({ apis: ['setTimeout', 'setInterval', 'Date'] });
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

test('audible is true while a started backend track plays', async () => {
  await playFirstTrack();
  assert.equal(engine.audible, true);
});

test('audible is false once the active track ends, though playing stays set', async () => {
  const el = await playFirstTrack();
  el.paused = true;
  el.ended = true;
  el.dispatch('ended');
  assert.equal(engine.playing, true);
  assert.equal(engine.audible, false);
});

test('audible is false after an external pause, though playing stays set', async () => {
  const el = await playFirstTrack();
  el.pause();
  assert.equal(engine.playing, true);
  assert.equal(engine.audible, false);
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

test('a play() that never settles fails with TimeoutError and leaves nothing playing', async () => {
  holdNextPlay();
  const first = engine.playStream('/stream/a', 'A');
  const rejected = assert.rejects(first, { name: 'TimeoutError' });
  await flush();
  const incoming = activeElement();

  mock.timers.tick(PLAY_START_TIMEOUT_MS - 1);
  await flush();
  assert.equal(incoming.src, '/stream/a', 'no timeout before the limit');
  mock.timers.tick(1);
  await rejected;

  assert.equal(incoming.src, '');
  assert.equal(engine.playing, false);
  assert.equal(engine['_activeChannel'], null);
});

test('a stuck next stream times out and restores the outgoing track', async () => {
  const first = await playFirstTrack();
  holdNextPlay();
  const second = engine.playStream('/stream/b', 'B');
  const rejected = assert.rejects(second, { name: 'TimeoutError' });
  await flush();

  mock.timers.tick(PLAY_START_TIMEOUT_MS);
  await rejected;

  assert.equal(activeElement(), first);
  assert.equal(first.src, '/stream/a');
  assert.equal(first.paused, false);
  assert.equal(engine['_activeChannel']?.gain.gain.value, 1);
  assert.equal(engine.playing, true);
});

// Like the browser: the next channel's play() stays pending, and a pause()
// before it settles rejects it with AbortError.
function abortNextPlayOnPause() {
  const el = engine['_nextChannel']().audioEl as unknown as FakeAudioElement;
  let abort: (() => void) | null = null;
  const play = mock.method(el, 'play', () => {
    el.paused = false;
    return new Promise<void>((_, reject) => {
      abort = () => reject(new DOMException('aborted', 'AbortError'));
    });
  });
  const pause = el.pause.bind(el);
  mock.method(el, 'pause', () => {
    pause();
    abort?.();
    abort = null;
  });
  return { el, play };
}

test('a pause during the start window keeps the new track loaded and paused', async () => {
  const first = await playFirstTrack();
  const next = abortNextPlayOnPause();
  const states = recordPlayStates();
  const onStarted = mock.fn();
  const second = engine.playStream('/stream/b', 'B', { onStarted });
  await flush();

  engine.pause();
  await second;

  assert.equal(activeElement(), next.el);
  assert.equal(next.el.src, '/stream/b');
  assert.equal(next.el.paused, true);
  assert.equal(engine['_activeChannel']?.gain.gain.value, 1);
  assert.equal(first.paused, true);
  assert.equal(first.src, '');
  assert.equal(channelGains().filter(gain => gain === 1).length, 1, 'outgoing gain not restored');
  assert.equal(engine.playing, false);
  assert.equal(onStarted.mock.callCount(), 1);
  assert.equal(states.at(-1), 'paused');

  assert.equal(engine.play(), true);
  assert.equal(next.play.mock.callCount(), 2);
  assert.equal(activeElement(), next.el);
  assert.equal(next.el.paused, false);
});

test('a pause during a crossfade also silences the fading-out track', async () => {
  const first = await playFirstTrack();
  const second = engine.playStream('/stream/b', 'B');
  await flush();

  engine.pause();
  assert.equal(first.paused, true);
  assert.equal(activeElement().paused, true);
  mock.timers.tick(FADE_MS);
  await second;
});

test('a play() that settles before the limit clears the start timer', async () => {
  const settle = holdNextPlay();
  const first = engine.playStream('/stream/a', 'A');
  await flush();
  mock.timers.tick(PLAY_START_TIMEOUT_MS - 1);
  settle().resolve();
  await flush();
  mock.timers.tick(FADE_MS);
  await first;

  const states = recordPlayStates();
  mock.timers.tick(PLAY_START_TIMEOUT_MS);
  await flush();
  assert.equal(activeElement().src, '/stream/a');
  assert.equal(engine.playing, true);
  assert.deepEqual(states, []);
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

// Leaves the first track loaded and paused under a context in `state`.
async function pauseUnderContext(state: string) {
  const el = await playFirstTrack();
  engine.pause();
  ctx().state = state;
  ctx().resumeCalls = 0;
  return el;
}

for (const state of ['suspended', 'interrupted']) {
  test(`play() resumes the element and a context in state ${state}`, async () => {
    const el = await pauseUnderContext(state);
    const play = mock.method(el, 'play');

    assert.equal(engine.play(), true);
    assert.equal(ctx().resumeCalls, 1);
    assert.equal(play.mock.callCount(), 1);
    assert.equal(ctx().state, 'running');
  });
}

test('play() leaves a running context alone', async () => {
  await pauseUnderContext('running');

  assert.equal(engine.play(), true);
  assert.equal(ctx().resumeCalls, 0);
});

test('play() survives a rejected context resume without an unhandled rejection', async () => {
  const el = await pauseUnderContext('interrupted');
  ctx().resumeError = new DOMException('denied', 'NotAllowedError');
  const unhandled: unknown[] = [];
  const onUnhandled = (reason: unknown) => unhandled.push(reason);
  process.on('unhandledRejection', onUnhandled);
  try {
    assert.equal(engine.play(), true);
    await flush();
    await flush();
  } finally {
    process.off('unhandledRejection', onUnhandled);
  }

  assert.equal(ctx().resumeCalls, 1);
  assert.equal(el.paused, false);
  assert.deepEqual(unhandled, []);
});

test('playStream resumes an interrupted context before the element plays', async () => {
  await playFirstTrack();
  ctx().state = 'interrupted';
  ctx().resumeCalls = 0;
  const order: string[] = [];
  const resume = ctx().resume.bind(ctx());
  mock.method(ctx(), 'resume', () => {
    order.push('resume');
    return resume();
  });
  const next = inactiveElement();
  const elementPlay = next.play.bind(next);
  mock.method(next, 'play', () => {
    order.push('play');
    return elementPlay();
  });

  const second = engine.playStream('/stream/b', 'B');
  await flush();
  ctx().currentTime = 20;
  mock.timers.tick(FADE_MS);
  await second;

  assert.deepEqual(order, ['resume', 'play']);
  assert.equal(ctx().state, 'running');
});

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

test('playStream reloads a preloaded channel whose element errored', async () => {
  await playFirstTrack();
  engine.preloadNext('/stream/b');
  const next = inactiveElement();
  next.error = { code: 4 };
  const loadsBefore = next.loads;
  let errorAtPlay: FakeAudioElement['error'] | undefined;
  mock.method(next, 'play', () => {
    errorAtPlay = next.error;
    return Promise.resolve();
  });

  const second = engine.playStream('/stream/b', 'B');
  await flush();
  mock.timers.tick(FADE_MS);
  await second;

  assert.equal(next.loads, loadsBefore + 1);
  assert.equal(errorAtPlay, null);
});

test('a healthy preload with the same URL is not reloaded', async () => {
  await playFirstTrack();
  engine.preloadNext('/stream/b');
  const next = inactiveElement();
  const loadsBefore = next.loads;

  const second = engine.playStream('/stream/b', 'B');
  await flush();
  mock.timers.tick(FADE_MS);
  await second;

  assert.equal(next.loads, loadsBefore);
});

test('preloadNext reloads the same URL after the preload errored', async () => {
  await playFirstTrack();
  engine.preloadNext('/stream/b');
  const next = inactiveElement();
  const loadsBefore = next.loads;

  engine.preloadNext('/stream/b');
  assert.equal(next.loads, loadsBefore);

  next.error = { code: 2 };
  engine.preloadNext('/stream/b');
  assert.equal(next.loads, loadsBefore + 1);
  assert.equal(next.error, null);
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

function recordPlayStates() {
  const states: MediaSessionPlaybackState[] = [];
  engine.onPlayStateChange = state => states.push(state);
  return states;
}

test('the play state is playing once a track starts and paused after pause()', async () => {
  const states = recordPlayStates();
  await playFirstTrack();
  assert.deepEqual(states, ['playing']);
  engine.pause();
  assert.deepEqual(states, ['playing', 'paused']);
});

test('the play state is none once the active track ends', async () => {
  const states = recordPlayStates();
  const el = await playFirstTrack();
  el.paused = true;
  el.ended = true;
  el.dispatch('ended');
  assert.deepEqual(states, ['playing', 'none']);
});

test('play() does not restart an ended track', async () => {
  const el = await playFirstTrack();
  el.paused = true;
  el.ended = true;
  const play = mock.method(el, 'play');
  engine.play();
  assert.equal(play.mock.callCount(), 0);
});

test('the outgoing channel stopping after a crossfade does not report paused', async () => {
  const first = await playFirstTrack();
  const states = recordPlayStates();
  const second = engine.playStream('/stream/b', 'B');
  await flush();
  mock.timers.tick(FADE_MS);
  await second;

  assert.equal(first.paused, true);
  assert.deepEqual(states, []);
});

test('the same play state is reported once', async () => {
  const states = recordPlayStates();
  const el = await playFirstTrack();
  el.dispatch('play');
  engine.pause();
  engine.pause();
  el.dispatch('pause');
  assert.deepEqual(states, ['playing', 'paused']);
});

function recordPositions() {
  const positions: Array<MediaPositionState | null> = [];
  engine.onPositionChange = state => positions.push(state);
  return positions;
}

test('a duration change on the active track reports its position', async () => {
  const el = await playFirstTrack();
  const positions = recordPositions();
  el.duration = 180;
  el.currentTime = 5;
  el.dispatch('durationchange');
  assert.deepEqual(positions, [{ duration: 180, position: 5, playbackRate: 1 }]);
});

test('a duration change on the inactive channel reports nothing', async () => {
  await playFirstTrack();
  const positions = recordPositions();
  const other = inactiveElement();
  other.duration = 180;
  other.dispatch('durationchange');
  assert.deepEqual(positions, []);
});

test('dispose clears the position', async () => {
  await playFirstTrack();
  const positions = recordPositions();
  engine.dispose();
  assert.equal(positions.at(-1), null);
});

test('dispose swallows an AudioContext.close() rejection and still clears the position', async () => {
  await playFirstTrack();
  const positions = recordPositions();
  ctx().closeError = new DOMException('already closed', 'InvalidStateError');
  const unhandled: unknown[] = [];
  const onUnhandled = (reason: unknown) => unhandled.push(reason);
  process.on('unhandledRejection', onUnhandled);
  try {
    engine.dispose();
    await flush();
    assert.deepEqual(unhandled, []);
    assert.equal(positions.at(-1), null);
  } finally {
    process.off('unhandledRejection', onUnhandled);
  }
});

test('a play() that resolves after dispose does not start the disposed engine', async () => {
  await playFirstTrack();
  const settleSecond = holdNextPlay();
  const onStarted = mock.fn();
  const second = engine.playStream('/stream/b', 'B', { onStarted, onNearEnd: () => {} });
  await flush();
  const listen = mock.method(activeElement(), 'addEventListener');

  engine.dispose();
  settleSecond().resolve();
  await second;

  assert.equal(onStarted.mock.callCount(), 0);
  assert.equal(engine.playing, false);
  assert.equal(engine.mode, 'idle');
  assert.equal(listen.mock.callCount(), 0);
});

test('a play() that rejects after dispose reports no further play state and stays idle', async () => {
  await playFirstTrack();
  const settleSecond = holdNextPlay();
  const second = engine.playStream('/stream/b', 'B');
  await flush();
  const incoming = activeElement();

  engine.dispose();
  const states = recordPlayStates();
  const loadsAfterDispose = incoming.loads;
  settleSecond().reject(new DOMException('aborted', 'AbortError'));
  await assert.rejects(second, { name: 'AbortError' });

  assert.deepEqual(states, []);
  assert.equal(incoming.loads, loadsAfterDispose, 'released element left alone');
  assert.equal(engine.playing, false);
  assert.equal(engine.mode, 'idle');
});

test('dispose during a crossfade leaves the released elements alone', async () => {
  const first = await playFirstTrack();
  const second = engine.playStream('/stream/b', 'B');
  await flush();
  engine.preloadNext('/stream/c');

  engine.dispose();
  const loadsAfterDispose = first.loads;
  mock.timers.tick(FADE_MS);
  await second;

  assert.equal(first.src, '');
  assert.equal(first.loads, loadsAfterDispose);
});

// Plays a first track with an onStall spy; the context clock stays at 10 s.
async function playWithStallSpy() {
  const onStall = mock.fn();
  const first = engine.playStream('/stream/a', 'A', { onStall });
  await flush();
  ctx().currentTime = 10;
  mock.timers.tick(FADE_MS);
  await first;
  return { el: activeElement(), onStall };
}

// Ticks in check-sized steps so every watchdog check runs. A check sees a stall
// up to STALL_CHECK_MS late, so "before" asserts stop one step short of the limit
// and "after" asserts run one step past it.
function elapse(ms: number) {
  for (let elapsed = 0; elapsed < ms; elapsed += STALL_CHECK_MS) mock.timers.tick(STALL_CHECK_MS);
}
const JUST_BEFORE_STALL = STALL_TIMEOUT_MS - STALL_CHECK_MS;
const PAST_STALL = 2 * STALL_CHECK_MS;

test('a playing track whose position stays frozen fires onStall once', async () => {
  const { onStall } = await playWithStallSpy();

  elapse(JUST_BEFORE_STALL);
  assert.equal(onStall.mock.callCount(), 0, 'no stall before the limit');
  elapse(PAST_STALL);
  assert.equal(onStall.mock.callCount(), 1);
  elapse(STALL_TIMEOUT_MS * 2);
  assert.equal(onStall.mock.callCount(), 1);
});

test('a paused track never fires onStall', async () => {
  const { onStall } = await playWithStallSpy();
  engine.pause();
  elapse(STALL_TIMEOUT_MS * 2);
  assert.equal(onStall.mock.callCount(), 0);
});

test('paused time does not count toward a stall after resume', async () => {
  const { onStall } = await playWithStallSpy();
  engine.pause();
  elapse(STALL_TIMEOUT_MS * 2);
  engine.play();

  elapse(JUST_BEFORE_STALL);
  assert.equal(onStall.mock.callCount(), 0);
  elapse(PAST_STALL);
  assert.equal(onStall.mock.callCount(), 1);
});

test('a position that advances before the limit resets the stall timer', async () => {
  const { el, onStall } = await playWithStallSpy();
  elapse(JUST_BEFORE_STALL);
  el.currentTime = 25;
  el.dispatch('timeupdate');

  elapse(JUST_BEFORE_STALL);
  assert.equal(onStall.mock.callCount(), 0);
  elapse(PAST_STALL);
  assert.equal(onStall.mock.callCount(), 1);
});

test('dispose clears the stall watchdog', async () => {
  const { onStall } = await playWithStallSpy();
  const clearInterval = mock.method(window, 'clearInterval');
  engine.dispose();
  assert.equal(clearInterval.mock.callCount(), 1);
  elapse(STALL_TIMEOUT_MS * 2);
  assert.equal(onStall.mock.callCount(), 0);
});

test('a superseding track clears the old stall watchdog', async () => {
  const { el, onStall } = await playWithStallSpy();
  const clearInterval = mock.method(window, 'clearInterval');
  const second = engine.playStream('/stream/b', 'B');
  await flush();
  mock.timers.tick(FADE_MS);
  await second;
  assert.equal(el.src, '', 'old channel released');
  assert.equal(clearInterval.mock.callCount(), 1);

  elapse(STALL_TIMEOUT_MS * 2);
  assert.equal(onStall.mock.callCount(), 0);
});

test('a frozen channel fading out does not fire onStall', async () => {
  const { onStall } = await playWithStallSpy();
  elapse(JUST_BEFORE_STALL);
  const second = engine.playStream('/stream/b', 'B');
  await flush();

  elapse(PAST_STALL);
  assert.equal(onStall.mock.callCount(), 0);
  await second;
});
