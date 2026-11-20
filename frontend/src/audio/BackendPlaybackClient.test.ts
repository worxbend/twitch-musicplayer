/* Tests for the WebSocket playback controller.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 *
 * The browser pieces the client touches (window timers, WebSocket, the audio
 * engine) are replaced with small fakes so races can be driven step by step.
 */
import { afterEach, beforeEach, mock, test } from 'node:test';
import assert from 'node:assert/strict';
import type { AudioEngine } from './AudioEngine';
import type { BackendPlaybackClient as Client, RadioStatus, SongInfo } from './BackendPlaybackClient';

class FakeWebSocket {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;
  static instances: FakeWebSocket[] = [];

  readyState = FakeWebSocket.CONNECTING;
  sent: unknown[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;

  constructor() {
    FakeWebSocket.instances.push(this);
  }

  send(raw: string) {
    this.sent.push(JSON.parse(raw));
  }

  close() {
    if (this.readyState === FakeWebSocket.CLOSED) return;
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.();
  }

  open() {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen?.();
  }

  message(body: unknown) {
    this.onmessage?.({ data: typeof body === 'string' ? body : JSON.stringify(body) });
  }

  error() {
    this.onerror?.();
  }
}

interface Deferred {
  resolve: () => void;
  reject: (error: unknown) => void;
}

class FakeAudio {
  allowAutoplay = false;
  plays: { url: string; name: string; settle: Deferred }[] = [];

  playStream(url: string, name: string): Promise<void> {
    return new Promise((resolve, reject) => {
      this.plays.push({ url, name, settle: { resolve, reject } });
    });
  }

  preloadNext() {}
}

Object.assign(globalThis, {
  window: {
    setTimeout: (fn: () => void, ms?: number) => globalThis.setTimeout(fn, ms),
    clearTimeout: (id?: ReturnType<typeof setTimeout>) => globalThis.clearTimeout(id),
    setInterval: (fn: () => void, ms?: number) => globalThis.setInterval(fn, ms),
    clearInterval: (id?: ReturnType<typeof setInterval>) => globalThis.clearInterval(id),
    addEventListener() {},
    removeEventListener() {},
    location: { protocol: 'http:', hostname: 'localhost' },
    __LOFI_CONFIG__: { backendUrl: 'http://localhost:8080' },
  },
});
// Node ships its own WebSocket global; replace it with the fake.
Object.defineProperty(globalThis, 'WebSocket', { value: FakeWebSocket, configurable: true, writable: true });

const { BackendPlaybackClient } = await import('./BackendPlaybackClient.ts');

let audio: FakeAudio;
let statuses: RadioStatus[];
let songs: (SongInfo | null)[];
let client: Client;

beforeEach(() => {
  mock.timers.enable({ apis: ['setTimeout', 'setInterval'] });
  FakeWebSocket.instances = [];
  audio = new FakeAudio();
  statuses = [];
  songs = [];
  client = new BackendPlaybackClient({
    audio: audio as unknown as AudioEngine,
    onStatus: status => statuses.push(status),
    onSongChange: song => songs.push(song),
  });
  client.start();
});

afterEach(() => {
  client.stop();
  mock.timers.reset();
});

function socket(index = FakeWebSocket.instances.length - 1) {
  return FakeWebSocket.instances[index];
}

function playSong(id: string, title = id) {
  return {
    type: 'play_song',
    payload: {
      song: { id, title, filename: `${id}.mp3` },
      stream_url: `/stream/${id}`,
      history_id: `h-${id}`,
    },
  };
}

function flush() {
  return new Promise<void>(resolve => setImmediate(resolve));
}

function lastPlayback() {
  return statuses.at(-1)?.playback;
}

function internals() {
  return client as unknown as { current: { history_id: string } | null; connecting: boolean };
}

test('a play that settles after a newer one is ignored', async () => {
  socket().open();
  socket().message(playSong('a', 'First'));
  socket().message(playSong('b', 'Second'));
  assert.equal(audio.plays.length, 2);

  audio.plays[1].settle.resolve();
  await flush();
  audio.plays[0].settle.resolve();
  await flush();

  assert.equal(internals().current?.history_id, 'h-b');
  assert.deepEqual(songs, [{ title: 'Second', artist: undefined }]);
  assert.equal(lastPlayback(), 'playing');
});

test('a stale play failure does not report over the newer play', async () => {
  socket().open();
  socket().message(playSong('a'));
  socket().message(playSong('b'));
  audio.plays[1].settle.resolve();
  await flush();
  audio.plays[0].settle.reject(new DOMException('x', 'NotAllowedError'));
  await flush();

  assert.equal(lastPlayback(), 'playing');
  assert.equal(internals().current?.history_id, 'h-b');
});

test('retryPendingPlay does nothing while playing or while a play is in flight', async () => {
  socket().open();
  socket().message(playSong('a'));
  client.retryPendingPlay();
  assert.equal(audio.plays.length, 1, 'in flight, not blocked');

  audio.plays[0].settle.resolve();
  await flush();
  socket().message(playSong('b'));
  client.retryPendingPlay();
  assert.equal(lastPlayback(), 'playing');
  assert.equal(audio.plays.length, 2, 'mid-crossfade click must not start a parallel play');
});

test('retryPendingPlay replays a blocked song once per click', async () => {
  socket().open();
  socket().message(playSong('a'));
  audio.plays[0].settle.reject(new DOMException('x', 'NotAllowedError'));
  await flush();
  assert.equal(lastPlayback(), 'blocked');

  client.retryPendingPlay();
  assert.equal(audio.plays.length, 2);
  assert.equal(audio.plays[1].url, 'http://localhost:8080/stream/a');
  client.retryPendingPlay();
  assert.equal(audio.plays.length, 2, 'second click while the retry is in flight');
});

test('NotSupportedError is a stream error that retries on its own', async () => {
  socket().open();
  socket().message(playSong('a'));
  audio.plays[0].settle.reject(new DOMException('x', 'NotSupportedError'));
  await flush();
  assert.equal(lastPlayback(), 'error');

  mock.timers.tick(4999);
  assert.equal(audio.plays.length, 1);
  mock.timers.tick(1);
  assert.equal(audio.plays.length, 2);
});

test('NotAllowedError waits for a click', async () => {
  socket().open();
  socket().message(playSong('a'));
  audio.plays[0].settle.reject(new DOMException('x', 'NotAllowedError'));
  await flush();
  assert.equal(lastPlayback(), 'blocked');

  mock.timers.tick(10000);
  assert.equal(audio.plays.length, 1);
});

test('malformed play_song payloads are ignored', () => {
  socket().open();
  socket().message({ type: 'play_song', payload: { song: { id: 1 }, stream_url: '/s', history_id: 'h' } });
  socket().message({ type: 'play_song' });
  socket().message('not json');
  assert.equal(audio.plays.length, 0);
});

test('an error followed by close schedules exactly one reconnect, with capped backoff', () => {
  const expected = [900, 1800, 3600, 7200, 8000, 8000];
  for (const delay of expected) {
    const before = FakeWebSocket.instances.length;
    socket().error();
    socket().close();
    mock.timers.tick(delay - 1);
    assert.equal(FakeWebSocket.instances.length, before, `no reconnect before ${delay}ms`);
    mock.timers.tick(1);
    assert.equal(FakeWebSocket.instances.length, before + 1, `one reconnect at ${delay}ms`);
    mock.timers.tick(20000);
    assert.equal(FakeWebSocket.instances.length, before + 1, 'no second reconnect');
  }

  socket().open();
  const before = FakeWebSocket.instances.length;
  socket().error();
  mock.timers.tick(900);
  assert.equal(FakeWebSocket.instances.length, before + 1, 'backoff resets after open');
});

test("a stale socket's late close does not reset connecting or reschedule", () => {
  const first = socket();
  const lateClose = first.onclose;
  const lateError = first.onerror;
  first.error();
  mock.timers.tick(900);
  assert.equal(FakeWebSocket.instances.length, 2);
  assert.equal(internals().connecting, true);
  const reported = statuses.length;

  lateClose?.();
  lateError?.();
  assert.equal(internals().connecting, true);
  assert.equal(statuses.length, reported);
  mock.timers.tick(20000);
  assert.equal(FakeWebSocket.instances.length, 2);
});
