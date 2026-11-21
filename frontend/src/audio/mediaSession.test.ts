/* Tests for the Media Session wiring.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 */
import { afterEach, beforeEach, test } from 'node:test';
import assert from 'node:assert/strict';
import {
  bindMediaSession,
  getMediaSession,
  mediaStateOf,
  setMediaPlayState,
  updateMediaMetadata,
  type MediaSessionLike,
} from './mediaSession.ts';

class FakeMediaMetadata {
  title: string;
  artist: string;
  album: string;
  constructor(init: { title: string; artist: string; album: string }) {
    this.title = init.title;
    this.artist = init.artist;
    this.album = init.album;
  }
}

class FakeSession {
  metadata: MediaMetadata | null = null;
  playbackState: MediaSessionPlaybackState = 'none';
  handlers = new Map<string, MediaSessionActionHandler | null>();
  setActionHandler(action: MediaSessionAction, handler: MediaSessionActionHandler | null) {
    this.handlers.set(action, handler);
  }
}

const g = globalThis as Record<string, unknown>;
let savedMetadata: unknown;
let session: FakeSession;

beforeEach(() => {
  savedMetadata = g.MediaMetadata;
  g.MediaMetadata = FakeMediaMetadata;
  session = new FakeSession();
});

afterEach(() => {
  if (savedMetadata === undefined) delete g.MediaMetadata;
  else g.MediaMetadata = savedMetadata;
});

function fire(action: MediaSessionAction) {
  const handler = session.handlers.get(action);
  assert.ok(handler, `no ${action} handler`);
  handler({ action });
}

test('updateMediaMetadata sets title, artist and album', () => {
  updateMediaMetadata(session, { title: 'Rain', artist: 'Kai' });
  assert.deepEqual({ ...session.metadata }, { title: 'Rain', artist: 'Kai', album: 'lofi radio' });
});

test('updateMediaMetadata falls back to the radio name for a missing artist', () => {
  updateMediaMetadata(session, { title: 'Rain', artist: '' });
  assert.equal(session.metadata?.artist, 'lofi radio');
});

test('updateMediaMetadata clears the metadata on null', () => {
  updateMediaMetadata(session, { title: 'Rain' });
  updateMediaMetadata(session, null);
  assert.equal(session.metadata, null);
});

test('updateMediaMetadata does nothing without MediaMetadata or a session', () => {
  delete g.MediaMetadata;
  assert.doesNotThrow(() => updateMediaMetadata(session, { title: 'Rain' }));
  assert.equal(session.metadata, null);
  assert.doesNotThrow(() => updateMediaMetadata(null, { title: 'Rain' }));
});

test('the play and pause handlers call onPlay and onPause and leave playbackState alone', () => {
  const calls: string[] = [];
  bindMediaSession(session, { onPlay: () => calls.push('play'), onPause: () => calls.push('pause') });
  session.playbackState = 'playing';
  fire('pause');
  assert.equal(session.playbackState, 'playing');
  session.playbackState = 'none';
  fire('play');
  assert.equal(session.playbackState, 'none');
  assert.deepEqual(calls, ['pause', 'play']);
});

test('unbind nulls both handlers and clears the metadata and state', () => {
  const unbind = bindMediaSession(session, { onPlay() {}, onPause() {} });
  updateMediaMetadata(session, { title: 'Rain' });
  session.playbackState = 'playing';
  unbind();
  assert.equal(session.handlers.get('play'), null);
  assert.equal(session.handlers.get('pause'), null);
  assert.equal(session.metadata, null);
  assert.equal(session.playbackState, 'none');
});

test('a setActionHandler that throws does not escape', () => {
  const throwing: MediaSessionLike = {
    metadata: null,
    playbackState: 'none',
    setActionHandler() { throw new TypeError('unsupported action'); },
  };
  assert.doesNotThrow(() => bindMediaSession(throwing, { onPlay() {}, onPause() {} })());
});

test('getMediaSession returns null without navigator.mediaSession', () => {
  // Node defines a navigator, but it has no mediaSession.
  assert.equal(getMediaSession(), null);
});

const stateCases: Array<[string, Parameters<typeof mediaStateOf>[0], MediaSessionPlaybackState]> = [
  ['no element', null, 'none'],
  ['no source', { paused: false, ended: false, hasSource: false }, 'none'],
  ['ended', { paused: true, ended: true, hasSource: true }, 'none'],
  ['paused', { paused: true, ended: false, hasSource: true }, 'paused'],
  ['playing', { paused: false, ended: false, hasSource: true }, 'playing'],
];

for (const [name, el, want] of stateCases) {
  test(`mediaStateOf: ${name} is ${want}`, () => {
    assert.equal(mediaStateOf(el), want);
  });
}

test('setMediaPlayState writes the state and ignores a null session', () => {
  setMediaPlayState(session, 'paused');
  assert.equal(session.playbackState, 'paused');
  assert.doesNotThrow(() => setMediaPlayState(null, 'playing'));
});
