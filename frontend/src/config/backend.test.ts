/* Tests for backend URL resolution.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 *
 * getBackendURL reads `window` on every call, so one import plus a mutable
 * window stub covers every source.
 */
import { after, beforeEach, test } from 'node:test';
import assert from 'node:assert/strict';

interface WindowStub {
  __LOFI_CONFIG__?: { backendUrl?: string };
  location: { protocol: string; hostname: string };
}

const hadWindow = 'window' in globalThis;
const originalWindow = (globalThis as { window?: unknown }).window;
const stub: WindowStub = { location: { protocol: 'http:', hostname: 'example.test' } };
Object.defineProperty(globalThis, 'window', { value: stub, configurable: true, writable: true });

const { coverURL, getBackendURL, getWebSocketURL, resolveStreamURL } = await import('./backend.ts');

function useBackend(backendUrl: string | undefined) {
  stub.__LOFI_CONFIG__ = backendUrl === undefined ? undefined : { backendUrl };
}

beforeEach(() => {
  delete stub.__LOFI_CONFIG__;
  stub.location = { protocol: 'http:', hostname: 'example.test' };
});

after(() => {
  if (hadWindow) {
    Object.defineProperty(globalThis, 'window', { value: originalWindow, configurable: true, writable: true });
  } else {
    delete (globalThis as { window?: unknown }).window;
  }
});

test('the ws URL keeps a reverse-proxy path prefix', () => {
  useBackend('https://host/radio/');
  assert.equal(getWebSocketURL(), 'wss://host/radio/ws');
});

test('a root-hosted http backend maps to ws on the same port', () => {
  useBackend('http://host:8080');
  assert.equal(getWebSocketURL(), 'ws://host:8080/ws');
});

test('a root-hosted https backend maps to wss', () => {
  useBackend('https://host');
  assert.equal(getWebSocketURL(), 'wss://host/ws');
});

test('the ws URL drops the backend query string', () => {
  useBackend('http://host:8080/?x=1');
  assert.equal(getWebSocketURL(), 'ws://host:8080/ws');
});

test('the player ws URL carries role=player and keeps the path prefix', () => {
  useBackend('https://host/radio/');
  assert.equal(getWebSocketURL('player'), 'wss://host/radio/ws?role=player');
});

test('the player ws URL over http maps to ws with role=player', () => {
  useBackend('http://host:8080');
  assert.equal(getWebSocketURL('player'), 'ws://host:8080/ws?role=player');
});

test('the player ws URL replaces the backend query string with role=player', () => {
  useBackend('http://host:8080/?x=1');
  assert.equal(getWebSocketURL('player'), 'ws://host:8080/ws?role=player');
});

test('the runtime backend URL is trimmed of spaces and trailing slashes', () => {
  useBackend('  https://host/radio//  ');
  assert.equal(getBackendURL(), 'https://host/radio');
});

test('without runtime or build config the page host on port 8080 is used', () => {
  useBackend(undefined);
  assert.equal(getBackendURL(), 'http://example.test:8080');
});

test('relative stream URLs resolve against the backend, absolute ones pass through', () => {
  useBackend('http://host:8080');
  assert.equal(resolveStreamURL('/v1/songs/1/content'), 'http://host:8080/v1/songs/1/content');
  assert.equal(resolveStreamURL('https://cdn.test/a.mp3'), 'https://cdn.test/a.mp3');
});

test('the cover URL keeps the backend path prefix and encodes the song id', () => {
  useBackend('https://host/radio/');
  assert.equal(coverURL('a b/c'), 'https://host/radio/v1/songs/a%20b%2Fc/cover');
});

test('a cover version is appended as an encoded ?v=, and an empty one adds nothing', () => {
  useBackend('http://host:8080');
  assert.equal(coverURL('a', '3f2a9c1d8e7b6a50'), 'http://host:8080/v1/songs/a/cover?v=3f2a9c1d8e7b6a50');
  assert.equal(coverURL('a', 'x y&z'), 'http://host:8080/v1/songs/a/cover?v=x%20y%26z');
  assert.equal(coverURL('a', ''), 'http://host:8080/v1/songs/a/cover');
  assert.equal(coverURL('a', undefined), 'http://host:8080/v1/songs/a/cover');
});
