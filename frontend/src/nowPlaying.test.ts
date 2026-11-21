/* Tests for the /now-playing overlay reducer and its query options.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseNowPlayingOptions, reduceNowPlaying, type NowPlayingSong } from './nowPlaying.ts';

const prev: NowPlayingSong = { title: 'Old Song', artist: 'Old Artist' };

function frame(type: string, payload?: unknown): string {
  return JSON.stringify({ type, payload });
}

function song(fields: Record<string, unknown>) {
  return { id: 's1', filename: 'rainy-day.mp3', title: 'Rainy Day', artist: 'Lofi Cat', album: '', duration_secs: 120, ...fields };
}

test('state with a current song returns that song', () => {
  const raw = frame('state', { current_song: song({}), queue_length: 2, total_songs: 9 });
  assert.deepEqual(reduceNowPlaying(null, raw), { title: 'Rainy Day', artist: 'Lofi Cat' });
});

test('state without a current song returns null', () => {
  assert.equal(reduceNowPlaying(prev, frame('state', { queue_length: 0, total_songs: 9 })), null);
});

test('now_playing replaces the previous song', () => {
  const raw = frame('now_playing', { song: song({ title: 'Next', artist: 'Someone' }), queue_depth: 1 });
  assert.deepEqual(reduceNowPlaying(prev, raw), { title: 'Next', artist: 'Someone' });
});

test('an unknown message type keeps the previous song', () => {
  assert.equal(reduceNowPlaying(prev, frame('queue_updated', { queue_depth: 3 })), prev);
});

test('heartbeat_ack keeps the previous song', () => {
  assert.equal(reduceNowPlaying(prev, frame('heartbeat_ack')), prev);
});

test('malformed JSON keeps the previous song', () => {
  assert.equal(reduceNowPlaying(prev, '{"type":"now_playing",'), prev);
});

test('now_playing without a usable song keeps the previous song', () => {
  assert.equal(reduceNowPlaying(prev, frame('now_playing', { queue_depth: 1 })), prev);
  assert.equal(reduceNowPlaying(prev, frame('now_playing', { song: song({ title: '', filename: '' }) })), prev);
});

test('a whitespace title falls back to the filename without its extension', () => {
  const raw = frame('now_playing', { song: song({ title: '   ', filename: 'late.night.drive.flac' }) });
  assert.equal(reduceNowPlaying(null, raw)?.title, 'late.night.drive');
});

test('a missing artist becomes an empty string', () => {
  const { artist: _artist, ...noArtist } = song({});
  const raw = frame('now_playing', { song: noArtist });
  assert.deepEqual(reduceNowPlaying(null, raw), { title: 'Rainy Day', artist: '' });
});

test('options default to left and 32px', () => {
  assert.deepEqual(parseNowPlayingOptions(''), { align: 'left', size: 32 });
});

test('options accept valid align and size values', () => {
  assert.deepEqual(parseNowPlayingOptions('?align=center&size=40'), { align: 'center', size: 40 });
  assert.deepEqual(parseNowPlayingOptions('?align=right&size=12'), { align: 'right', size: 12 });
  assert.equal(parseNowPlayingOptions('?size=120').size, 120);
});

test('invalid options fall back to the defaults', () => {
  for (const search of ['?align=middle&size=11', '?align=LEFT&size=121', '?size=40.5', '?size=-20', '?size=abc', '?size=']) {
    assert.deepEqual(parseNowPlayingOptions(search), { align: 'left', size: 32 }, search);
  }
});
