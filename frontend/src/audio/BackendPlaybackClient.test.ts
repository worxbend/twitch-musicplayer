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

interface PlayCallbacks {
  onEnded: () => void;
  onError: () => void;
  onNearEnd: () => void;
}

class FakeAudio {
  allowAutoplay = false;
  plays: { url: string; name: string; settle: Deferred; callbacks: PlayCallbacks }[] = [];

  playStream(url: string, name: string, callbacks: PlayCallbacks): Promise<void> {
    return new Promise((resolve, reject) => {
      this.plays.push({ url, name, settle: { resolve, reject }, callbacks });
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

function needSongCount(ws: FakeWebSocket) {
  return ws.sent.filter(m => (m as { type: string }).type === 'need_song').length;
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

async function failPlay(index: number, name = 'NotSupportedError') {
  audio.plays[index].settle.reject(new DOMException('x', name));
  await flush();
}

test('a stream that never plays is skipped after 3 retries with one need_song', async () => {
  const ws = socket();
  ws.open();
  ws.message(playSong('a'));
  await failPlay(0);
  for (let retry = 1; retry <= 3; retry += 1) {
    assert.equal(needSongCount(ws), 1, `no skip before retry ${retry}`);
    mock.timers.tick(5000);
    assert.equal(audio.plays.length, retry + 1);
    await failPlay(retry);
  }

  assert.equal(needSongCount(ws), 2);
  assert.equal(statuses.at(-1)?.message, 'stream unplayable, skipping');
  assert.equal(lastPlayback(), 'error');
  mock.timers.tick(5000);
  assert.equal(audio.plays.length, 4, 'no fifth attempt');
  assert.ok(!ws.sent.some(m => (m as { type: string }).type === 'song_finished'));
});

test('onError and a rejection from the same attempt count as one retry', async () => {
  const ws = socket();
  ws.open();
  ws.message(playSong('a'));
  for (let attempt = 0; attempt <= 3; attempt += 1) {
    if (attempt > 0) mock.timers.tick(5000);
    assert.equal(audio.plays.length, attempt + 1);
    assert.equal(needSongCount(ws), 1, `no skip before attempt ${attempt}`);
    audio.plays[attempt].callbacks.onError();
    await failPlay(attempt);
  }

  assert.equal(needSongCount(ws), 2);
  mock.timers.tick(5000);
  assert.equal(audio.plays.length, 4);
});

test('a successful retry resets the count for a later failure', async () => {
  const ws = socket();
  ws.open();
  ws.message(playSong('a'));
  await failPlay(0);
  mock.timers.tick(5000);
  await failPlay(1);
  mock.timers.tick(5000);
  audio.plays[2].settle.resolve();
  await flush();
  assert.equal(lastPlayback(), 'playing');

  // The same song errors mid-playback: it gets a fresh 3 retries.
  audio.plays[2].callbacks.onError();
  for (let retry = 1; retry <= 3; retry += 1) {
    assert.equal(needSongCount(ws), 1, `no skip before retry ${retry}`);
    mock.timers.tick(5000);
    assert.equal(audio.plays.length, retry + 3);
    await failPlay(retry + 2);
  }
  assert.equal(needSongCount(ws), 2);
});

test('NotAllowedError does not count as a stream retry or skip the song', async () => {
  const ws = socket();
  ws.open();
  ws.message(playSong('a'));
  await failPlay(0, 'NotAllowedError');
  mock.timers.tick(20000);
  assert.equal(audio.plays.length, 1);
  assert.equal(needSongCount(ws), 1);

  client.retryPendingPlay();
  await failPlay(1);
  for (let retry = 1; retry <= 3; retry += 1) {
    assert.equal(needSongCount(ws), 1, `no skip before retry ${retry}`);
    mock.timers.tick(5000);
    await failPlay(retry + 1);
  }
  assert.equal(needSongCount(ws), 2);
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

test('a need_song lost with its socket is re-sent on the next socket', () => {
  socket(0).open();
  assert.equal(needSongCount(socket(0)), 1);

  socket(0).close();
  mock.timers.tick(900);
  socket(1).open();
  assert.equal(needSongCount(socket(1)), 1);
});

test('a song_finished lost with its socket is followed by need_song on the next socket', async () => {
  socket(0).open();
  socket(0).message(playSong('a'));
  audio.plays[0].settle.resolve();
  await flush();
  audio.plays[0].callbacks.onNearEnd();
  assert.ok(socket(0).sent.some(m => (m as { type: string }).type === 'song_finished'));

  socket(0).close();
  mock.timers.tick(900);
  socket(1).open();
  assert.equal(needSongCount(socket(1)), 1);
});

test('a need_song still in the outbox is sent once on reconnect', () => {
  socket(0).open();
  socket(0).message({ type: 'error', payload: { message: 'queue empty' } });
  socket(0).close();
  // The 900 ms error retry fires while disconnected and queues need_song.
  for (let ms = 0; ms < 5000; ms += 100) mock.timers.tick(100);
  const reopened = socket();
  assert.notEqual(reopened, socket(0));
  reopened.open();
  assert.equal(needSongCount(reopened), 1);
});

test('an unanswered need_song is re-sent after 15s', () => {
  socket().open();
  assert.equal(needSongCount(socket()), 1);
  mock.timers.tick(14999);
  assert.equal(needSongCount(socket()), 1);
  mock.timers.tick(1);
  assert.equal(needSongCount(socket()), 2);
});

test('a play_song before 15s cancels the reply timeout', () => {
  socket().open();
  mock.timers.tick(10000);
  socket().message(playSong('a'));
  mock.timers.tick(10000);
  assert.equal(needSongCount(socket()), 1);
});

test('error retries back off to 30s and reset after a successful play', async () => {
  const ws = socket();
  ws.open();
  for (const delay of [900, 1800, 3600, 7200, 14400, 28800, 30000, 30000]) {
    const before = needSongCount(ws);
    ws.message({ type: 'error', payload: { message: 'queue empty' } });
    mock.timers.tick(delay - 1);
    assert.equal(needSongCount(ws), before, `no retry before ${delay}ms`);
    mock.timers.tick(1);
    assert.equal(needSongCount(ws), before + 1, `retry at ${delay}ms`);
  }

  ws.message(playSong('a'));
  audio.plays[0].settle.resolve();
  await flush();
  audio.plays[0].callbacks.onEnded();
  const before = needSongCount(ws);
  ws.message({ type: 'error', payload: { message: 'queue empty' } });
  mock.timers.tick(900);
  assert.equal(needSongCount(ws), before + 1, 'backoff resets after a play');
});

test('the reply timeout does not pile up need_song while disconnected', () => {
  socket(0).open();
  socket(0).close();
  // No socket opens for a minute; reconnects keep backing off.
  for (let ms = 0; ms < 58000; ms += 100) mock.timers.tick(100);
  const reopened = socket();
  reopened.open();
  const sent = FakeWebSocket.instances.slice(1).reduce((n, ws) => n + needSongCount(ws), 0);
  assert.equal(sent, 1);
});
