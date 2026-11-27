/* Tests for the admin connection labels and announcements.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { connectionAnnouncement, connectionLabel, type AdminConnection } from './connection.ts';

const states: AdminConnection[] = ['connecting', 'connected', 'reconnecting', 'offline'];

test('every state has its visible label', () => {
  assert.equal(connectionLabel('connecting'), 'Connecting to backend');
  assert.equal(connectionLabel('connected'), 'Backend connected');
  assert.equal(connectionLabel('reconnecting'), 'Reconnecting to backend');
  assert.equal(connectionLabel('offline'), 'Backend offline');
});

test('an unchanged state is not announced', () => {
  for (const state of states) assert.equal(connectionAnnouncement(state, state), null);
});

test('the first connect on page load is silent', () => {
  assert.equal(connectionAnnouncement('connecting', 'connected'), null);
});

test('every later change is announced with its new state', () => {
  const changes: [AdminConnection, AdminConnection, RegExp][] = [
    ['connected', 'reconnecting', /Reconnecting/],
    ['connected', 'offline', /offline/],
    ['reconnecting', 'offline', /offline/],
    ['reconnecting', 'connected', /Backend connected/],
    ['offline', 'connected', /Backend connected/],
    ['offline', 'reconnecting', /Reconnecting/],
  ];
  for (const [prev, next, text] of changes) {
    assert.match(connectionAnnouncement(prev, next) ?? '', text, `${prev} -> ${next}`);
  }
});
