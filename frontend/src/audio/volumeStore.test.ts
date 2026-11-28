/* Tests for master volume persistence and keyboard rules.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { VOLUME_KEY, loadVolume, parseVolume, saveVolume, shouldIgnoreVolumeKey, stepVolume, volumeValueText } from './volumeStore.ts';

const throwingStorage = {
  getItem(): string | null { throw new DOMException('denied', 'SecurityError'); },
  setItem(): void { throw new DOMException('denied', 'SecurityError'); },
};

test('parseVolume defaults to full volume for missing or unreadable values', () => {
  assert.equal(parseVolume(null), 1);
  assert.equal(parseVolume(''), 1);
  assert.equal(parseVolume('abc'), 1);
});

test('parseVolume keeps in-range values and clamps the rest', () => {
  assert.equal(parseVolume('0.4'), 0.4);
  assert.equal(parseVolume('0'), 0);
  assert.equal(parseVolume('7'), 1);
  assert.equal(parseVolume('-1'), 0);
});

test('stepVolume clamps at both ends and rounds to two decimals', () => {
  assert.equal(stepVolume(0.02, -0.05), 0);
  assert.equal(stepVolume(0.98, 0.05), 1);
  assert.equal(stepVolume(0.1, 0.2), 0.3);
  assert.equal(stepVolume(0.55, -0.05), 0.5);
});

test('volumeValueText names the rounded percent and the mute state', () => {
  assert.equal(volumeValueText(1, false), '100%');
  assert.equal(volumeValueText(0, false), '0%');
  assert.equal(volumeValueText(0.405, false), '41%');
  assert.equal(volumeValueText(0.4, true), '40%, muted');
});

test('volumeValueText clamps out-of-range and non-finite volumes', () => {
  assert.equal(volumeValueText(Number.NaN, false), '0%');
  assert.equal(volumeValueText(Number.POSITIVE_INFINITY, true), '0%, muted');
  assert.equal(volumeValueText(1.5, false), '100%');
  assert.equal(volumeValueText(-0.2, false), '0%');
});

test('loadVolume reads the stored value and survives a throwing or missing storage', () => {
  assert.equal(loadVolume({ getItem: key => (key === VOLUME_KEY ? '0.25' : null) }), 0.25);
  assert.equal(loadVolume(throwingStorage), 1);
  assert.equal(loadVolume(undefined), 1);
});

test('saveVolume writes the value and survives a throwing or missing storage', () => {
  const saved = new Map<string, string>();
  saveVolume({ setItem: (key, value) => { saved.set(key, value); } }, 0.35);
  assert.equal(saved.get(VOLUME_KEY), '0.35');
  assert.doesNotThrow(() => saveVolume(throwingStorage, 0.5));
  assert.doesNotThrow(() => saveVolume(undefined, 0.5));
});

test('shouldIgnoreVolumeKey ignores modifier chords and typing targets', () => {
  const plain = { ctrlKey: false, metaKey: false, altKey: false };
  assert.equal(shouldIgnoreVolumeKey({ ...plain, target: { tagName: 'BODY' } }), false);
  assert.equal(shouldIgnoreVolumeKey({ ...plain, target: null }), false);
  assert.equal(shouldIgnoreVolumeKey({ ...plain, ctrlKey: true, target: null }), true);
  assert.equal(shouldIgnoreVolumeKey({ ...plain, metaKey: true, target: null }), true);
  assert.equal(shouldIgnoreVolumeKey({ ...plain, altKey: true, target: null }), true);
  for (const tagName of ['INPUT', 'textarea', 'SELECT']) {
    assert.equal(shouldIgnoreVolumeKey({ ...plain, target: { tagName } }), true, tagName);
  }
  assert.equal(shouldIgnoreVolumeKey({ ...plain, target: { tagName: 'DIV', isContentEditable: true } }), true);
});

test('shouldIgnoreVolumeKey lets shortcuts through on a range slider but not on text inputs', () => {
  const plain = { ctrlKey: false, metaKey: false, altKey: false };
  assert.equal(shouldIgnoreVolumeKey({ ...plain, target: { tagName: 'INPUT', type: 'range' } }), false);
  assert.equal(shouldIgnoreVolumeKey({ ...plain, target: { tagName: 'input', type: 'RANGE' } }), false);
  assert.equal(shouldIgnoreVolumeKey({ ...plain, target: { tagName: 'INPUT', type: 'text' } }), true);
  assert.equal(shouldIgnoreVolumeKey({ ...plain, target: { tagName: 'INPUT' } }), true);
  assert.equal(shouldIgnoreVolumeKey({ ...plain, ctrlKey: true, target: { tagName: 'INPUT', type: 'range' } }), true);
});
