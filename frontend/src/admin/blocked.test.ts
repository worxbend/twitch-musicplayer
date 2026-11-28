/* Tests for the library view of the blocklist.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { blockedIdSet, blockedReason } from './blocked.ts';

test('an empty blocklist blocks nothing', () => {
  assert.equal(blockedIdSet([]).size, 0);
});

test('duplicate entries collapse to one id', () => {
  const ids = blockedIdSet([{ id: 'a' }, { id: 'a' }, { id: 'b' }]);
  assert.deepEqual([...ids].sort(), ['a', 'b']);
});

test('a missing-file entry still counts as blocked by id', () => {
  const entries = [{ id: 'gone', missing: true as const }, { id: 'a' }];
  const ids = blockedIdSet(entries);
  assert.equal(ids.has('gone'), true);
  assert.equal(ids.has('a'), true);
  assert.equal(ids.has('b'), false);
});

test('the reason names the song and says it is out of rotation', () => {
  assert.equal(blockedReason('Midnight compiler'), 'Blocked: Midnight compiler is out of rotation');
});
