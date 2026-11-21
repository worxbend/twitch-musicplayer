/* Tests for the player status pill, live region and message text.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';

const { playerStatus } = await import('./playerStatus.ts');

const OFFLINE_DETAIL = 'Waiting for the backend. Reconnecting automatically.';

test('disconnected reads Offline', () => {
  assert.deepEqual(playerStatus('disconnected', 'playing', false), { live: false, label: 'Offline', detail: OFFLINE_DETAIL });
});

test('reconnecting reads Reconnecting', () => {
  assert.deepEqual(playerStatus('reconnecting', 'idle', false), { live: false, label: 'Reconnecting', detail: OFFLINE_DETAIL });
});

test('connected but not playing reads Standby with the cause', () => {
  assert.deepEqual(playerStatus('connected', 'blocked', false), {
    live: false, label: 'Standby', detail: 'Your browser needs a click before it can play audio.',
  });
  assert.deepEqual(playerStatus('connected', 'error', false), {
    live: false, label: 'Standby', detail: 'Waiting for playable audio. Check your library; the player will retry automatically.',
  });
  assert.deepEqual(playerStatus('connected', 'idle', false), {
    live: false, label: 'Standby', detail: 'Connected. Waiting for the next track.',
  });
});

test('connected and playing reads On air', () => {
  assert.deepEqual(playerStatus('connected', 'playing', false), {
    live: true, label: 'On air', detail: 'Your library, on a continuous frequency.',
  });
});

test('a paused element while playing reads Paused, not live', () => {
  assert.deepEqual(playerStatus('connected', 'playing', true), {
    live: false, label: 'Paused', detail: 'Paused. Press play or a media key to resume.',
  });
});

test('paused does not override blocked or offline', () => {
  assert.equal(playerStatus('connected', 'blocked', true).label, 'Standby');
  assert.equal(playerStatus('disconnected', 'playing', true).label, 'Offline');
  assert.equal(playerStatus('reconnecting', 'playing', true).label, 'Reconnecting');
});
