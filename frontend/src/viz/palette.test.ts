/* Tests for the colour ramps shared by the scenes.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 *
 * Scenes feed sample() positions computed from audio levels, so a bad level
 * (NaN, out of range) must still land on a defined colour.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { AURORA, EMBER, Ramp, SPECTRUM, rampByName, type RGB } from './palette.ts';

const BLACK: RGB = [0, 0, 0];
const ORANGE: RGB = [200, 100, 50];

/** Two-stop ramp from black at 0 to orange at 1. */
const twoStop = () => new Ramp([{ at: 0, rgb: BLACK }, { at: 1, rgb: ORANGE }]);

test('sample returns the end stops at 0 and 1', () => {
  const ramp = twoStop();
  assert.deepEqual(ramp.sample(0), BLACK);
  assert.deepEqual(ramp.sample(1), ORANGE);
});

test('sample clamps positions outside 0..1', () => {
  const ramp = twoStop();
  assert.deepEqual(ramp.sample(-1), BLACK);
  assert.deepEqual(ramp.sample(-Infinity), BLACK);
  assert.deepEqual(ramp.sample(2), ORANGE);
  assert.deepEqual(ramp.sample(Infinity), ORANGE);
});

test('sample(NaN) gives the colour at 0, not the last stop', () => {
  assert.deepEqual(twoStop().sample(NaN), BLACK);
  assert.deepEqual(SPECTRUM.sample(NaN), SPECTRUM.sample(0));
});

test('sample interpolates linearly between two stops', () => {
  assert.deepEqual(twoStop().sample(0.5), [100, 50, 25]);
});

test('the constructor sorts unsorted stops', () => {
  const ramp = new Ramp([{ at: 1, rgb: ORANGE }, { at: 0, rgb: BLACK }]);
  assert.deepEqual(ramp.sample(0), BLACK);
  assert.deepEqual(ramp.sample(0.5), [100, 50, 25]);
});

test('duplicate stop positions never produce NaN', () => {
  const ramps = [
    new Ramp([{ at: 0.5, rgb: BLACK }, { at: 0.5, rgb: ORANGE }]),
    new Ramp([
      { at: 0, rgb: BLACK },
      { at: 0.5, rgb: ORANGE },
      { at: 0.5, rgb: BLACK },
      { at: 1, rgb: ORANGE },
    ]),
  ];
  for (const ramp of ramps) {
    for (const t of [0, 0.25, 0.5, 0.75, 1]) {
      assert.ok(ramp.sample(t).every(Number.isFinite), `sample(${t}) = ${ramp.sample(t)}`);
    }
  }
});

test('the constructor rejects an empty stop list', () => {
  assert.throws(() => new Ramp([]), /Ramp needs at least one stop/);
});

test('hex packs rounded channels as 0xRRGGBB', () => {
  const ramp = new Ramp([{ at: 0, rgb: [1.4, 2.5, 254.6] }]);
  assert.equal(ramp.hex(0), 0x0103ff);
  assert.equal(twoStop().hex(1), 0xc86432);
});

test('css rounds channels and formats alpha', () => {
  const ramp = new Ramp([{ at: 0, rgb: [1.4, 2.5, 254.6] }]);
  assert.equal(ramp.css(0), 'rgba(1,3,255,1)');
  assert.equal(ramp.css(0, 0.5), 'rgba(1,3,255,0.5)');
});

test('cssStops(1) gives one stop at offset 0', () => {
  assert.deepEqual(twoStop().cssStops(1), [{ offset: 0, color: 'rgba(0,0,0,1)' }]);
});

test('cssStops(n) spans offsets 0..1 evenly', () => {
  const stops = twoStop().cssStops(3, 0.5);
  assert.deepEqual(stops.map((s) => s.offset), [0, 0.5, 1]);
  assert.equal(stops[0].color, 'rgba(0,0,0,0.5)');
  assert.equal(stops[2].color, 'rgba(200,100,50,0.5)');
});

test('rampByName looks names up case-insensitively, falling back to SPECTRUM', () => {
  assert.equal(rampByName(undefined), SPECTRUM);
  assert.equal(rampByName(''), SPECTRUM);
  assert.equal(rampByName('EMBER'), EMBER);
  assert.equal(rampByName('aurora'), AURORA);
  assert.equal(rampByName('nope'), SPECTRUM);
});
