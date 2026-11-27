/* Tests for the client router that picks the page for a URL.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';

const { resolveRoute, sceneHref } = await import('./routes.ts');

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

test('a scene link keeps ?ramp=', () => {
  assert.equal(sceneHref('bars', '?ramp=EMBER'), '/bars?ramp=EMBER');
});

test('a scene link keeps ramp and autoplay in a fixed order', () => {
  assert.equal(sceneHref('bars', '?autoplay=1&ramp=EMBER'), '/bars?ramp=EMBER&autoplay=1');
});

test('a scene link keeps autoplay=0', () => {
  assert.equal(sceneHref('bars', '?autoplay=0'), '/bars?autoplay=0');
});

test('a scene link drops transparent, unknown and empty params', () => {
  assert.equal(sceneHref('bars', '?transparent=1&x=2&ramp='), '/bars');
});

test('a scene link with no search is the bare path', () => {
  assert.equal(sceneHref('bars', ''), '/bars');
});

test('a scene link encodes a value that needs it', () => {
  const href = sceneHref('bars', '?ramp=a%20b');
  assert.equal(new URL(href, 'http://x').searchParams.get('ramp'), 'a b');
});

test('the overlay scene link uses -overlay', () => {
  assert.equal(sceneHref('bars', '?ramp=EMBER', true), '/bars-overlay?ramp=EMBER');
});

test('the ambient link is / or /overlay', () => {
  assert.equal(sceneHref('', '?ramp=EMBER&junk=1'), '/?ramp=EMBER');
  assert.equal(sceneHref('', '', true), '/overlay');
});
