/* Tests for the client router that picks the page for a URL.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';

const { resolveRoute } = await import('./routes.ts');

const SLUGS = ['spectrum', 'bars'];
const route = (pathname: string, search = '') => resolveRoute(pathname, search, SLUGS);

test('the root is the player', () => {
  assert.deepEqual(route('/'), { kind: 'player' });
  assert.deepEqual(route(''), { kind: 'player' });
  assert.deepEqual(route('/', '?x=1'), { kind: 'player' });
});

test('overlay accepts a trailing slash', () => {
  assert.deepEqual(route('/overlay'), { kind: 'overlay' });
  assert.deepEqual(route('/overlay/'), { kind: 'overlay' });
});

test('logo and logo-overlay accept a trailing slash', () => {
  for (const pathname of ['/logo', '/logo/', '/logo-overlay', '/logo-overlay/']) {
    assert.deepEqual(route(pathname), { kind: 'logo-overlay' }, pathname);
  }
});

test('scenes index accepts a trailing slash', () => {
  assert.deepEqual(route('/scenes'), { kind: 'scenes' });
  assert.deepEqual(route('/scenes/'), { kind: 'scenes' });
});

test('now-playing accepts a trailing slash', () => {
  assert.deepEqual(route('/now-playing'), { kind: 'now-playing' });
  assert.deepEqual(route('/now-playing/'), { kind: 'now-playing' });
});

test('a scene slug is the opaque scene', () => {
  assert.deepEqual(route('/bars'), { kind: 'scene', slug: 'bars', transparent: false });
  assert.deepEqual(route('/bars/'), { kind: 'scene', slug: 'bars', transparent: false });
});

test('a scene slug with -overlay is transparent', () => {
  assert.deepEqual(route('/bars-overlay'), { kind: 'scene', slug: 'bars', transparent: true });
  assert.deepEqual(route('/bars-overlay/'), { kind: 'scene', slug: 'bars', transparent: true });
});

test('?transparent=1 forces transparency on the opaque scene', () => {
  assert.deepEqual(route('/bars', '?transparent=1'), { kind: 'scene', slug: 'bars', transparent: true });
});

test('?transparent=0 does not make an overlay scene opaque', () => {
  assert.deepEqual(route('/bars-overlay', '?transparent=0'), { kind: 'scene', slug: 'bars', transparent: true });
});

test('unknown paths fall back to the player', () => {
  assert.deepEqual(route('/nope'), { kind: 'player' });
  assert.deepEqual(route('/overlay/extra'), { kind: 'player' });
});
