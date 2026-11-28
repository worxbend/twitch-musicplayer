/* Tests for the shared radio wiring.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 */
import { afterEach, beforeEach, test } from 'node:test';
import assert from 'node:assert/strict';
import type { AudioEngine } from './AudioEngine';
import type { BackendPlaybackClient, ErrorCode, RadioStatus, SongInfo } from './BackendPlaybackClient';
import { startRadio, type RadioOptions } from './radioSession.ts';

class FakeMediaMetadata {
  title: string;
  artist: string;
  album: string;
  artwork: MediaImage[];
  constructor(init: { title: string; artist: string; album: string; artwork: MediaImage[] }) {
    this.title = init.title;
    this.artist = init.artist;
    this.album = init.album;
    this.artwork = init.artwork;
  }
}

// Throws like a browser that cannot parse an artwork src.
class BadArtworkMediaMetadata extends FakeMediaMetadata {
  constructor(init: { title: string; artist: string; album: string; artwork: MediaImage[] }) {
    if (init.artwork.length > 0) throw new TypeError('invalid artwork src');
    super(init);
  }
}

class FakeSession {
  metadata: MediaMetadata | null = null;
  playbackState: MediaSessionPlaybackState = 'none';
  handlers = new Map<string, MediaSessionActionHandler | null>();
  setActionHandler(action: MediaSessionAction, handler: MediaSessionActionHandler | null) {
    this.handlers.set(action, handler);
  }
  setPositionState() {}
}

class FakeAudio {
  plays = 0;
  pauses = 0;
  onPlayStateChange?: (state: MediaSessionPlaybackState) => void;
  onPositionChange?: (state: MediaPositionState | null) => void;
  play() { this.plays++; return true; }
  pause() { this.pauses++; return true; }
}

type ClientOptions = ConstructorParameters<typeof BackendPlaybackClient>[0];

class FakeClient {
  options: ClientOptions;
  started = 0;
  retries = 0;
  disposals = 0;
  constructor(options: ClientOptions) { this.options = options; }
  start() { this.started++; }
  retryPendingPlay() { this.retries++; return undefined; }
  dispose() { this.disposals++; }
  status(playback: RadioStatus['playback'], errorCode?: RadioStatus['errorCode']) {
    this.options.onStatus?.({ connection: 'connected', playback, message: '', errorCode });
  }
  song(song: SongInfo | null) { this.options.onSongChange?.(song); }
}

const g = globalThis as Record<string, unknown>;
let savedMetadata: unknown;
let session: FakeSession;
let audio: FakeAudio;
let client: FakeClient;
let tracks: Array<[string, string, string | undefined]>;
let statuses: Array<[string, string]>;
let mediaStates: MediaSessionPlaybackState[];

beforeEach(() => {
  savedMetadata = g.MediaMetadata;
  g.MediaMetadata = FakeMediaMetadata;
  session = new FakeSession();
  audio = new FakeAudio();
  tracks = [];
  statuses = [];
  mediaStates = [];
});

afterEach(() => {
  if (savedMetadata === undefined) delete g.MediaMetadata;
  else g.MediaMetadata = savedMetadata;
});

function start(overrides: Partial<RadioOptions> = {}) {
  return startRadio({
    audio: audio as unknown as AudioEngine,
    session,
    placeholder: 'waiting',
    untitled: 'untitled',
    artistFallback: 'radio',
    onTrack: (name, artist, cover) => tracks.push([name, artist, cover]),
    onStatus: (conn, playback) => statuses.push([conn, playback]),
    onMediaState: (state) => mediaStates.push(state),
    createClient: (options) => (client = new FakeClient(options)),
    ...overrides,
  });
}

function fire(action: MediaSessionAction) {
  const handler = session.handlers.get(action);
  assert.ok(handler, `no ${action} handler`);
  handler({ action });
}

test('startRadio starts the client', () => {
  start();
  assert.equal(client.started, 1);
});

test('a song change reports title, artist and cover and sets the session metadata', () => {
  start();
  client.song({ title: 'Rain', artist: 'Kai', coverUrl: '/cover.jpg' });
  assert.deepEqual(tracks, [['Rain', 'Kai', '/cover.jpg']]);
  assert.deepEqual(
    { ...session.metadata },
    { title: 'Rain', artist: 'Kai', album: 'lofi radio', artwork: [{ src: '/cover.jpg' }] },
  );
});

test('a retag of the same song replaces the title and the versioned artwork', () => {
  start();
  client.song({ title: 'Rain', coverUrl: '/v1/songs/a/cover?v=old' });
  client.song({ title: 'Rain (remaster)', coverUrl: '/v1/songs/a/cover?v=new' });
  assert.equal(session.metadata?.title, 'Rain (remaster)');
  assert.deepEqual(session.metadata?.artwork, [{ src: '/v1/songs/a/cover?v=new' }]);
});

test('a next song without a cover clears the artwork', () => {
  start();
  client.song({ title: 'Rain', coverUrl: '/cover.jpg' });
  client.song({ title: 'Snow' });
  assert.equal(session.metadata?.title, 'Snow');
  assert.deepEqual(session.metadata?.artwork, []);
});

test('a cover the browser rejects still shows the title', () => {
  g.MediaMetadata = BadArtworkMediaMetadata;
  start();
  client.song({ title: 'Rain', coverUrl: 'http://[bad' });
  assert.equal(session.metadata?.title, 'Rain');
  assert.deepEqual(session.metadata?.artwork, []);
});

test('a null song reports the placeholder and the artist fallback and clears the metadata', () => {
  start();
  client.song({ title: 'Rain', artist: 'Kai' });
  client.song(null);
  assert.deepEqual(tracks.at(-1), ['waiting', 'radio', undefined]);
  assert.equal(session.metadata, null);
});

test('an empty title reports the untitled name and a missing artist the fallback', () => {
  start();
  client.song({ title: '' });
  assert.deepEqual(tracks, [['untitled', 'radio', undefined]]);
});

test('status reports reach onStatus', () => {
  start();
  client.status('playing');
  assert.deepEqual(statuses, [['connected', 'playing']]);
});

test('the error code reaches onStatus', () => {
  const codes: (ErrorCode | undefined)[] = [];
  start({ onStatus: (_conn, _playback, code) => codes.push(code) });
  client.status('error', 'queue_empty');
  client.status('playing');
  assert.deepEqual(codes, ['queue_empty', undefined]);
});

test('the play key retries a blocked play and plays otherwise', () => {
  start();
  fire('play');
  assert.deepEqual([audio.plays, client.retries], [1, 0]);
  client.status('blocked');
  fire('play');
  assert.deepEqual([audio.plays, client.retries], [1, 1]);
  client.status('playing');
  fire('play');
  assert.deepEqual([audio.plays, client.retries], [2, 1]);
});

test('the pause key pauses the engine', () => {
  start();
  fire('pause');
  assert.equal(audio.pauses, 1);
});

test('engine play-state changes reach onMediaState and the session', () => {
  start();
  audio.onPlayStateChange?.('playing');
  assert.deepEqual(mediaStates, ['playing']);
  assert.equal(session.playbackState, 'playing');
});

test('retryPendingPlay forwards to the client', () => {
  start().retryPendingPlay();
  assert.equal(client.retries, 1);
});

test('dispose clears the hooks, releases the session and disposes the client once', () => {
  const radio = start();
  client.song({ title: 'Rain' });
  audio.onPlayStateChange?.('playing');
  radio.dispose();
  assert.equal(audio.onPlayStateChange, undefined);
  assert.equal(audio.onPositionChange, undefined);
  assert.deepEqual(mediaStates, ['playing', 'none']);
  assert.equal(session.playbackState, 'none');
  assert.equal(session.metadata, null);
  assert.equal(session.handlers.get('play'), null);
  assert.equal(session.handlers.get('pause'), null);
  assert.equal(client.disposals, 1);
  radio.dispose();
  assert.equal(client.disposals, 1);
  assert.deepEqual(mediaStates, ['playing', 'none']);
});

test('a null session leaves the OS media session alone and never throws', () => {
  const radio = start({ session: null });
  client.song({ title: 'Rain', coverUrl: '/cover.jpg' });
  audio.onPlayStateChange?.('playing');
  audio.onPositionChange?.(null);
  radio.dispose();
  assert.deepEqual(tracks, [['Rain', 'radio', '/cover.jpg']]);
  assert.deepEqual(mediaStates, ['playing', 'none']);
  assert.equal(session.metadata, null);
  assert.equal(session.handlers.size, 0);
  assert.equal(client.disposals, 1);
});
