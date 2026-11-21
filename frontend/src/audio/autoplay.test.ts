/* Tests for the autoplay decision.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 *
 * shouldAutoStartAudio reads `window` on every call, so one import plus a
 * mutable window stub covers every source.
 */
import { after, beforeEach, test } from 'node:test';
import assert from 'node:assert/strict';

interface WindowStub {
  location: { search: string };
  obsstudio?: unknown;
}

const hadWindow = 'window' in globalThis;
const originalWindow = (globalThis as { window?: unknown }).window;
const stub: WindowStub = { location: { search: '' } };
Object.defineProperty(globalThis, 'window', { value: stub, configurable: true, writable: true });

const { shouldAutoStartAudio } = await import('./autoplay.ts');

function usePage(search: string, inObs = false) {
  stub.location = { search };
  if (inObs) stub.obsstudio = {};
  else delete stub.obsstudio;
}

beforeEach(() => usePage(''));

after(() => {
  if (hadWindow) {
    Object.defineProperty(globalThis, 'window', { value: originalWindow, configurable: true, writable: true });
  } else {
    delete (globalThis as { window?: unknown }).window;
  }
});

test('truthy autoplay query values start audio', () => {
  for (const value of ['1', 'true', 'yes', 'on', 'TRUE', '%20TRUE%20']) {
    usePage(`?autoplay=${value}`);
    assert.equal(shouldAutoStartAudio(), true, value);
  }
});

test('falsy autoplay query values win even inside OBS', () => {
  for (const value of ['0', 'false', 'no', 'off']) {
    usePage(`?autoplay=${value}`, true);
    assert.equal(shouldAutoStartAudio(true), false, value);
  }
});

test('an unknown autoplay value falls through to the next source', () => {
  usePage('?autoplay=maybe');
  assert.equal(shouldAutoStartAudio(), false);
  usePage('?autoplay=maybe', true);
  assert.equal(shouldAutoStartAudio(), true);
});

test('inside OBS without a query audio starts', () => {
  usePage('', true);
  assert.equal(shouldAutoStartAudio(), true);
});

test('without a query or OBS the default is returned', () => {
  assert.equal(shouldAutoStartAudio(false), false);
  assert.equal(shouldAutoStartAudio(true), true);
});
