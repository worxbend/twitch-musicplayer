/* Tests for the shared track-time formatter.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { formatDuration, formatTrackTime } from './trackTime.ts';

test('formatTrackTime shows m:ss under an hour', () => {
  assert.equal(formatTrackTime(0), '0:00');
  assert.equal(formatTrackTime(59.9), '0:59');
  assert.equal(formatTrackTime(60), '1:00');
  assert.equal(formatTrackTime(3599), '59:59');
});

test('formatTrackTime shows h:mm:ss from an hour on', () => {
  assert.equal(formatTrackTime(3600), '1:00:00');
  assert.equal(formatTrackTime(3661), '1:01:01');
  assert.equal(formatTrackTime(4500), '1:15:00');
});

test('formatTrackTime reads non-finite or negative input as 0:00', () => {
  assert.equal(formatTrackTime(NaN), '0:00');
  assert.equal(formatTrackTime(-5), '0:00');
  assert.equal(formatTrackTime(Infinity), '0:00');
});

test('formatDuration marks an unknown duration', () => {
  assert.equal(formatDuration(0), '--:--');
  assert.equal(formatDuration(NaN), '--:--');
  assert.equal(formatDuration(-1), '--:--');
  assert.equal(formatDuration(Infinity), '--:--');
});

test('formatDuration formats a known duration', () => {
  assert.equal(formatDuration(125), '2:05');
  assert.equal(formatDuration(3725), '1:02:05');
});
