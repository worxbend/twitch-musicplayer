/* Tests for the admin library sort and the "New" badge window.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { NEW_WINDOW_MS, isNew, sortSongs } from './librarySort.ts';

const now = Date.parse('2026-11-28T12:00:00Z');
const ago = (ms: number) => new Date(now - ms).toISOString();
const HOUR = 60 * 60 * 1000;
const MINUTE = 60 * 1000;

const songs = [
  { id: 'a', added_at: ago(3 * 24 * HOUR) },
  { id: 'b', added_at: ago(HOUR) },
  { id: 'c', added_at: 'not a date' },
  { id: 'd', added_at: ago(2 * HOUR) },
  { id: 'e', added_at: ago(HOUR) },
];
const ids = (list: { id: string }[]) => list.map(s => s.id);

test('title keeps server order in a new array', () => {
  const sorted = sortSongs(songs, 'title');
  assert.deepEqual(ids(sorted), ['a', 'b', 'c', 'd', 'e']);
  assert.notEqual(sorted, songs);
});

test('recent puts newest first, keeps ties in server order and invalid dates last', () => {
  assert.deepEqual(ids(sortSongs(songs, 'recent')), ['b', 'e', 'd', 'a', 'c']);
});

test('sorting does not mutate the input', () => {
  const before = ids(songs);
  sortSongs(songs, 'recent');
  assert.deepEqual(ids(songs), before);
});

test('isNew covers the last 24 hours', () => {
  assert.equal(isNew(ago(0), now), true);
  assert.equal(isNew(ago(23 * HOUR + 59 * MINUTE), now), true);
  assert.equal(isNew(ago(NEW_WINDOW_MS), now), false);
  assert.equal(isNew(ago(3 * 24 * HOUR), now), false);
});

test('isNew allows five minutes of clock skew', () => {
  assert.equal(isNew(ago(-5 * MINUTE), now), true);
  assert.equal(isNew(ago(-5 * MINUTE - 1), now), false);
  assert.equal(isNew(ago(-2 * HOUR), now), false);
});

test('isNew is false for an invalid or empty date', () => {
  assert.equal(isNew('not a date', now), false);
  assert.equal(isNew('', now), false);
});
