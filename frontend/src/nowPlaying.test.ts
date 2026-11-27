/* Tests for the /now-playing overlay reducer and its query options.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  clearsOnClose,
  nextShown,
  nowPlayingFonts,
  nowPlayingPath,
  nowPlayingThemes,
  parseNowPlayingOptions,
  progressTiming,
  reduceNowPlaying,
  reduceUpNext,
  type NowPlayingOptions,
  type NowPlayingSong,
  type ShownKey,
} from './nowPlaying.ts';

const prev: NowPlayingSong = { title: 'Old Song', artist: 'Old Artist' };

function frame(type: string, payload?: unknown): string {
  return JSON.stringify({ type, payload });
}

function song(fields: Record<string, unknown>) {
  return { id: 's1', filename: 'rainy-day.mp3', title: 'Rainy Day', artist: 'Lofi Cat', album: '', ...fields };
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

test('clearsOnClose is true only for 1001 Going Away', () => {
  assert.equal(clearsOnClose(1001), true);
  for (const code of [1000, 1006, 1011]) assert.equal(clearsOnClose(code), false, String(code));
});

test('nextShown shows the same title again after a Going Away clear', () => {
  const rain: NowPlayingSong = { title: 'Rainy Day', artist: '' };
  const first = nextShown({ key: '', title: '' }, rain);
  assert.equal(first.show, true);
  const cleared = nextShown(first.shown, null);
  assert.equal(nextShown(cleared.shown, rain).show, true);
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
  assert.deepEqual(parseNowPlayingOptions(''), { align: 'left', size: 32, hold: 0, art: false, theme: 'shadow', font: 'sans', next: false, progress: false });
});

test('options accept valid align and size values', () => {
  assert.deepEqual(parseNowPlayingOptions('?align=center&size=40'), { align: 'center', size: 40, hold: 0, art: false, theme: 'shadow', font: 'sans', next: false, progress: false });
  assert.deepEqual(parseNowPlayingOptions('?align=right&size=12'), { align: 'right', size: 12, hold: 0, art: false, theme: 'shadow', font: 'sans', next: false, progress: false });
  assert.equal(parseNowPlayingOptions('?size=120').size, 120);
});

test('invalid options fall back to the defaults', () => {
  for (const search of ['?align=middle&size=11', '?align=LEFT&size=121', '?size=40.5', '?size=-20', '?size=abc', '?size=']) {
    assert.deepEqual(parseNowPlayingOptions(search), { align: 'left', size: 32, hold: 0, art: false, theme: 'shadow', font: 'sans', next: false, progress: false }, search);
  }
});

test('hold accepts whole seconds from 1 to 600', () => {
  assert.equal(parseNowPlayingOptions('?hold=5').hold, 5);
  assert.equal(parseNowPlayingOptions('?hold=1').hold, 1);
  assert.equal(parseNowPlayingOptions('?hold=600').hold, 600);
  assert.deepEqual(parseNowPlayingOptions('?align=center&size=40&hold=10'), { align: 'center', size: 40, hold: 10, art: false, theme: 'shadow', font: 'sans', next: false, progress: false });
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
    { align: 'center', size: 48, hold: 8, art: true, theme: 'shadow', font: 'sans', next: false, progress: false },
    { align: 'right', size: 12, hold: 600, art: false, theme: 'outline', font: 'sans', next: false, progress: false },
    { align: 'left', size: 120, hold: 1, art: false, theme: 'pill', font: 'mono', next: false, progress: false },
    { align: 'left', size: 32, hold: 0, art: false, theme: 'shadow', font: 'sans', next: false, progress: false },
    { align: 'left', size: 32, hold: 0, art: true, theme: 'shadow', font: 'mono', next: false, progress: false },
    { align: 'left', size: 32, hold: 0, art: false, theme: 'shadow', font: 'sans', next: true, progress: false },
    { align: 'left', size: 32, hold: 0, art: false, theme: 'shadow', font: 'sans', next: false, progress: true },
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
      const opts: NowPlayingOptions = { align: 'center', size: 40, hold: 10, art: true, theme, font, next: true, progress: false };
      assert.deepEqual(parseNowPlayingOptions(new URL(nowPlayingPath(opts), 'http://x').search), opts, `${theme}/${font}`);
    }
  }
});

test('next is on only for next=1', () => {
  assert.equal(parseNowPlayingOptions('?next=1').next, true);
  for (const search of ['', '?next=', '?next=0', '?next=true']) {
    assert.equal(parseNowPlayingOptions(search).next, false, search);
  }
});

test('nowPlayingPath emits next=1 only when next is on, after the other params', () => {
  assert.equal(nowPlayingPath({ next: true, progress: false }), '/now-playing?next=1');
  assert.equal(nowPlayingPath({ next: false, progress: false }), '/now-playing');
  assert.equal(nowPlayingPath({ align: 'center', next: true, progress: false }), '/now-playing?align=center&next=1');
});

test('reduceUpNext takes next_song from queue_updated', () => {
  const raw = frame('queue_updated', { queue_depth: 2, reason: 'song_added', next_song: song({ title: 'Snow' }) });
  assert.deepEqual(reduceUpNext(null, raw), { title: 'Snow', artist: 'Lofi Cat' });
});

test('reduceUpNext clears on queue_updated without next_song', () => {
  assert.equal(reduceUpNext(prev, frame('queue_updated', { queue_depth: 0, reason: 'song_started' })), null);
});

test('reduceUpNext reads next_song from state and clears on a state without it', () => {
  const raw = frame('state', { current_song: song({}), queue_length: 1, total_songs: 9, next_song: song({ title: 'Snow' }) });
  assert.deepEqual(reduceUpNext(null, raw), { title: 'Snow', artist: 'Lofi Cat' });
  assert.equal(reduceUpNext(prev, frame('state', { queue_length: 0, total_songs: 9 })), null);
});

test('reduceUpNext keeps prev on now_playing and on garbage', () => {
  assert.equal(reduceUpNext(prev, frame('now_playing', { song: song({}), queue_depth: 1 })), prev);
  assert.equal(reduceUpNext(prev, 'not json'), prev);
  assert.equal(reduceUpNext(prev, frame('queue_updated')), prev);
});

test('progress is on only for progress=1', () => {
  assert.equal(parseNowPlayingOptions('?progress=1').progress, true);
  for (const search of ['', '?progress=', '?progress=0', '?progress=true']) {
    assert.equal(parseNowPlayingOptions(search).progress, false, search);
  }
});

test('nowPlayingPath emits progress=1 only when progress is on, after next', () => {
  assert.equal(nowPlayingPath({ progress: true }), '/now-playing?progress=1');
  assert.equal(nowPlayingPath({ progress: false }), '/now-playing');
  assert.equal(nowPlayingPath({ next: true, progress: true }), '/now-playing?next=1&progress=1');
});

test('duration_secs and elapsed_secs become durationSecs and elapsedSecs', () => {
  const now = frame('now_playing', { song: song({ duration_secs: 180 }), history_id: 'h1', elapsed_secs: 0 });
  assert.deepEqual(reduceNowPlaying(null, now), { title: 'Rainy Day', artist: 'Lofi Cat', startId: 'h1', durationSecs: 180, elapsedSecs: 0 });
  const state = frame('state', { current_song: song({ duration_secs: 180 }), elapsed_secs: 42.5 });
  assert.deepEqual(reduceNowPlaying(null, state), { title: 'Rainy Day', artist: 'Lofi Cat', durationSecs: 180, elapsedSecs: 42.5 });
});

test('unusable duration_secs and elapsed_secs are left out', () => {
  // JSON has no NaN; an out-of-range literal such as 1e999 parses to Infinity.
  for (const bad of ['-1', '"42"', 'null', '1e999']) {
    const raw = `{"type":"state","payload":{"current_song":{"title":"t","duration_secs":${bad}},"elapsed_secs":${bad}}}`;
    const got = reduceNowPlaying(null, raw);
    assert.equal(got?.durationSecs, undefined, bad);
    assert.equal(got?.elapsedSecs, undefined, bad);
  }
  const missing = reduceNowPlaying(null, frame('now_playing', { song: song({}) }));
  assert.equal(missing?.durationSecs, undefined);
  assert.equal(missing?.elapsedSecs, undefined);
  const zero = reduceNowPlaying(null, frame('state', { current_song: song({ duration_secs: 0 }), elapsed_secs: 0 }));
  assert.equal(zero?.durationSecs, undefined, 'a zero duration is unknown');
  assert.equal(zero?.elapsedSecs, 0, 'a zero elapsed is a fresh start');
});

test('progressTiming is null without a duration or an elapsed time', () => {
  assert.equal(progressTiming(null, 0, 0), null);
  assert.equal(progressTiming({ title: 't', artist: '', elapsedSecs: 3 }, 0, 0), null);
  assert.equal(progressTiming({ title: 't', artist: '', durationSecs: 120 }, 0, 0), null);
});

test('progressTiming advances with local time and clamps to 0..duration', () => {
  const base: NowPlayingSong = { title: 't', artist: '', durationSecs: 120, elapsedSecs: 42 };
  assert.deepEqual(progressTiming(base, 1_000, 1_000), { durationSecs: 120, elapsedSecs: 42 });
  assert.deepEqual(progressTiming(base, 1_000, 3_500), { durationSecs: 120, elapsedSecs: 44.5 });
  assert.deepEqual(progressTiming({ ...base, elapsedSecs: 130 }, 0, 0), { durationSecs: 120, elapsedSecs: 120 });
  assert.deepEqual(progressTiming(base, 1_000, 1_000 + 200_000), { durationSecs: 120, elapsedSecs: 120 });
  assert.deepEqual(progressTiming({ ...base, elapsedSecs: 0 }, 5_000, 1_000), { durationSecs: 120, elapsedSecs: 0 });
});
