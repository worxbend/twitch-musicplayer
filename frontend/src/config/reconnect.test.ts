/* Tests for the shared reconnect backoff, stale-socket check and handshake timeout.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 */
import { afterEach, mock, test } from 'node:test';
import assert from 'node:assert/strict';
import { HANDSHAKE_TIMEOUT_MS, STALE_AFTER_MS, armHandshakeTimeout, isStale, reconnectDelay } from './reconnect.ts';

afterEach(() => mock.timers.reset());

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

test('a socket still CONNECTING times out after exactly 10000ms', () => {
  mock.timers.enable({ apis: ['setTimeout'] });
  let fired = 0;
  armHandshakeTimeout({ readyState: 0 }, () => fired++);
  mock.timers.tick(HANDSHAKE_TIMEOUT_MS - 1);
  assert.equal(fired, 0);
  mock.timers.tick(1);
  assert.equal(fired, 1);
});

test('an open or closed socket at the deadline does not time out', () => {
  mock.timers.enable({ apis: ['setTimeout'] });
  for (const readyState of [1, 3]) {
    let fired = 0;
    armHandshakeTimeout({ readyState }, () => fired++);
    mock.timers.tick(HANDSHAKE_TIMEOUT_MS);
    assert.equal(fired, 0, `readyState ${readyState}`);
  }
});

test('cancel before the deadline prevents the timeout and is safe to repeat', () => {
  mock.timers.enable({ apis: ['setTimeout'] });
  let fired = 0;
  const cancel = armHandshakeTimeout({ readyState: 0 }, () => fired++);
  mock.timers.tick(HANDSHAKE_TIMEOUT_MS - 1);
  cancel();
  cancel();
  mock.timers.tick(HANDSHAKE_TIMEOUT_MS);
  assert.equal(fired, 0);
});

test('a custom timeout is honored', () => {
  mock.timers.enable({ apis: ['setTimeout'] });
  let fired = 0;
  armHandshakeTimeout({ readyState: 0 }, () => fired++, 500);
  mock.timers.tick(499);
  assert.equal(fired, 0);
  mock.timers.tick(1);
  assert.equal(fired, 1);
});
