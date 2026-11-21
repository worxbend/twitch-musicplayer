/* Tests for the shared reconnect backoff and stale-socket check.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { STALE_AFTER_MS, isStale, reconnectDelay } from './reconnect.ts';

test('reconnect delay doubles from 900ms', () => {
  assert.equal(reconnectDelay(0), 900);
  assert.equal(reconnectDelay(1), 1800);
  assert.equal(reconnectDelay(2), 3600);
});

test('reconnect delay caps at 8000ms', () => {
  assert.equal(reconnectDelay(4), 8000);
  assert.equal(reconnectDelay(50), 8000);
});

test('a socket is stale only after the timeout has passed', () => {
  assert.equal(isStale(1000, 1000 + STALE_AFTER_MS), false);
  assert.equal(isStale(1000, 1000 + STALE_AFTER_MS + 1), true);
  assert.equal(isStale(0, 10, 5), true);
  assert.equal(isStale(0, 5, 5), false);
});
