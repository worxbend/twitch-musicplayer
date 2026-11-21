/* Tests for the admin latest-request gate.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createLatest } from './latestRequest.ts';

test('a single request is the latest', () => {
  const gate = createLatest();
  const id = gate.begin();
  assert.equal(gate.isLatest(id), true);
});

test('an earlier request is stale once a newer one begins', () => {
  const gate = createLatest();
  const first = gate.begin();
  const second = gate.begin();
  assert.equal(gate.isLatest(first), false);
  assert.equal(gate.isLatest(second), true);
});

test('request ids increase monotonically', () => {
  const gate = createLatest();
  const ids = [gate.begin(), gate.begin(), gate.begin()];
  assert.deepEqual(ids, [1, 2, 3]);
});

test('separate gates do not invalidate each other', () => {
  const queue = createLatest();
  const history = createLatest();
  const id = queue.begin();
  history.begin();
  assert.equal(queue.isLatest(id), true);
});
