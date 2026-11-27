/* Tests for the /now-playing overlay reducer and its query options.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  nextShown,
  nowPlayingFonts,
  nowPlayingPath,
  nowPlayingThemes,
  parseNowPlayingOptions,
  reduceNowPlaying,
  type NowPlayingOptions,
  type NowPlayingSong,
  type ShownKey,
} from './nowPlaying.ts';

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

test('now_playing copies history_id into startId', () => {
  const raw = frame('now_playing', { song: song({}), queue_depth: 0, history_id: ' h1 ' });
  assert.deepEqual(reduceNowPlaying(null, raw), { title: 'Rainy Day', artist: 'Lofi Cat', startId: 'h1' });
});

test('a state frame gives no startId', () => {
  const started: NowPlayingSong = { title: 'Rainy Day', artist: 'Lofi Cat', startId: 'h1' };
  const raw = frame('state', { current_song: song({}), queue_length: 0, total_songs: 1 });
  assert.equal(reduceNowPlaying(started, raw)?.startId, undefined);
});

test('a song with has_cover carries its id as coverId', () => {
  const raw = frame('now_playing', { song: song({ has_cover: true }), history_id: 'h1' });
  assert.deepEqual(reduceNowPlaying(null, raw), { title: 'Rainy Day', artist: 'Lofi Cat', coverId: 's1', startId: 'h1' });
  const state = frame('state', { current_song: song({ has_cover: true }) });
  assert.equal(reduceNowPlaying(null, state)?.coverId, 's1');
});

test('coverId needs has_cover true and a song id', () => {
  for (const fields of [{ has_cover: false }, { has_cover: 'true' }, {}, { has_cover: true, id: '' }, { has_cover: true, id: 7 }]) {
    const raw = frame('now_playing', { song: song(fields) });
    assert.equal(reduceNowPlaying(null, raw)?.coverId, undefined, JSON.stringify(fields));
  }
});

const shownRain: ShownKey = { key: 'h1', title: 'Rainy Day' };

test('nextShown re-shows the same title on a new start id', () => {
  const next = nextShown(shownRain, { title: 'Rainy Day', artist: '', startId: 'h2' });
  assert.deepEqual(next, { show: true, shown: { key: 'h2', title: 'Rainy Day' } });
});

test('nextShown keeps the same start id hidden and takes its new title', () => {
  const next = nextShown(shownRain, { title: 'Rainy Day (Remaster)', artist: '', startId: 'h1' });
  assert.deepEqual(next, { show: false, shown: { key: 'h1', title: 'Rainy Day (Remaster)' } });
});

test('nextShown keeps a same-title retag hidden', () => {
  assert.deepEqual(nextShown(shownRain, { title: 'Rainy Day', artist: 'New Artist' }), { show: false, shown: shownRain });
});

test('nextShown shows a retag that changes the title', () => {
  const next = nextShown(shownRain, { title: 'Sunny Day', artist: '' });
  assert.deepEqual(next, { show: true, shown: { key: 'Sunny Day', title: 'Sunny Day' } });
});

test('nextShown re-arms after an idle clear', () => {
  const cleared = nextShown(shownRain, null);
  assert.deepEqual(cleared, { show: false, shown: { key: '', title: '' } });
  assert.equal(nextShown(cleared.shown, { title: 'Rainy Day', artist: '' }).show, true);
});

test('nextShown keeps a hidden title hidden on a reconnect state frame', () => {
  const raw = frame('state', { current_song: song({}), queue_length: 0, total_songs: 1 });
  assert.equal(nextShown(shownRain, reduceNowPlaying(null, raw)).show, false);
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

test('art is on only for art=1', () => {
  assert.equal(parseNowPlayingOptions('?art=1').art, true);
  for (const search of ['', '?art=', '?art=0', '?art=true', '?art=yes']) {
    assert.equal(parseNowPlayingOptions(search).art, false, search);
  }
});

test('nowPlayingPath emits art=1 only when art is on', () => {
  assert.equal(nowPlayingPath({ art: true }), '/now-playing?art=1');
  assert.equal(nowPlayingPath({ art: false }), '/now-playing');
});

test('options default to left and 32px', () => {
  assert.deepEqual(parseNowPlayingOptions(''), { align: 'left', size: 32, hold: 0, art: false, theme: 'shadow', font: 'sans' });
});

test('options accept valid align and size values', () => {
  assert.deepEqual(parseNowPlayingOptions('?align=center&size=40'), { align: 'center', size: 40, hold: 0, art: false, theme: 'shadow', font: 'sans' });
  assert.deepEqual(parseNowPlayingOptions('?align=right&size=12'), { align: 'right', size: 12, hold: 0, art: false, theme: 'shadow', font: 'sans' });
  assert.equal(parseNowPlayingOptions('?size=120').size, 120);
});

test('invalid options fall back to the defaults', () => {
  for (const search of ['?align=middle&size=11', '?align=LEFT&size=121', '?size=40.5', '?size=-20', '?size=abc', '?size=']) {
    assert.deepEqual(parseNowPlayingOptions(search), { align: 'left', size: 32, hold: 0, art: false, theme: 'shadow', font: 'sans' }, search);
  }
});

test('hold accepts whole seconds from 1 to 600', () => {
  assert.equal(parseNowPlayingOptions('?hold=5').hold, 5);
  assert.equal(parseNowPlayingOptions('?hold=1').hold, 1);
  assert.equal(parseNowPlayingOptions('?hold=600').hold, 600);
  assert.deepEqual(parseNowPlayingOptions('?align=center&size=40&hold=10'), { align: 'center', size: 40, hold: 10, art: false, theme: 'shadow', font: 'sans' });
});

test('invalid or missing hold means always visible', () => {
  for (const search of ['', '?hold=', '?hold=0', '?hold=601', '?hold=-3', '?hold=abc', '?hold=1.5']) {
    assert.equal(parseNowPlayingOptions(search).hold, 0, search);
  }
});

test('nowPlayingPath leaves defaults out', () => {
  assert.equal(nowPlayingPath({}), '/now-playing');
  assert.equal(nowPlayingPath({ align: 'left', size: 32, hold: 0, art: false }), '/now-playing');
});

test('nowPlayingPath orders params align, size, hold', () => {
  assert.equal(nowPlayingPath({ hold: 8, size: 48, align: 'center' }), '/now-playing?align=center&size=48&hold=8');
});

test('nowPlayingPath round-trips through parseNowPlayingOptions', () => {
  const cases: NowPlayingOptions[] = [
    { align: 'center', size: 48, hold: 8, art: true, theme: 'shadow', font: 'sans' },
    { align: 'right', size: 12, hold: 600, art: false, theme: 'outline', font: 'sans' },
    { align: 'left', size: 120, hold: 1, art: false, theme: 'pill', font: 'mono' },
    { align: 'left', size: 32, hold: 0, art: false, theme: 'shadow', font: 'sans' },
    { align: 'left', size: 32, hold: 0, art: true, theme: 'shadow', font: 'mono' },
  ];
  for (const opts of cases) {
    assert.deepEqual(parseNowPlayingOptions(new URL(nowPlayingPath(opts), 'http://x').search), opts);
  }
});

test('nowPlayingPath clamps out-of-range values', () => {
  assert.equal(nowPlayingPath({ size: 5 }), '/now-playing?size=12');
  assert.equal(nowPlayingPath({ size: 500 }), '/now-playing?size=120');
  assert.equal(nowPlayingPath({ hold: 9999, art: false }), '/now-playing?hold=600');
  assert.equal(nowPlayingPath({ hold: -3 }), '/now-playing');
});

test('nowPlayingPath rounds non-integers and ignores bad input', () => {
  assert.equal(nowPlayingPath({ size: 47.6, hold: 7.5 }), '/now-playing?size=48&hold=8');
  assert.equal(nowPlayingPath({ size: NaN, hold: NaN }), '/now-playing');
  assert.equal(nowPlayingPath({ align: 'middle' as never }), '/now-playing');
});

test('theme and font accept each preset by exact match', () => {
  for (const theme of nowPlayingThemes) assert.equal(parseNowPlayingOptions(`?theme=${theme}`).theme, theme);
  for (const font of nowPlayingFonts) assert.equal(parseNowPlayingOptions(`?font=${font}`).font, font);
});

test('invalid, empty or wrong-case theme and font fall back to shadow and sans', () => {
  for (const search of ['', '?theme=', '?theme=Pill', '?theme=OUTLINE', '?theme=neon', '?theme=pill%20', '?font=', '?font=Mono', '?font=serif']) {
    const opts = parseNowPlayingOptions(search);
    assert.deepEqual([opts.theme, opts.font], ['shadow', 'sans'], search);
  }
});

test('nowPlayingPath emits theme and font only when not the default', () => {
  assert.equal(nowPlayingPath({ theme: 'shadow', font: 'sans' }), '/now-playing');
  assert.equal(nowPlayingPath({ theme: 'pill', font: 'mono' }), '/now-playing?theme=pill&font=mono');
  assert.equal(nowPlayingPath({ theme: 'neon' as never, font: 'serif' as never }), '/now-playing');
});

test('every theme and font round-trips with the other params', () => {
  for (const theme of nowPlayingThemes) {
    for (const font of nowPlayingFonts) {
      const opts: NowPlayingOptions = { align: 'center', size: 40, hold: 10, art: true, theme, font };
      assert.deepEqual(parseNowPlayingOptions(new URL(nowPlayingPath(opts), 'http://x').search), opts, `${theme}/${font}`);
    }
  }
});
