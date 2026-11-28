/* analysisMath.ts — the two reductions that turn analyser output into
 * something worth drawing. Kept as pure functions so they can be tested
 * without a browser or an audio device.
 */

export interface LogBandTable {
  /** First FFT bin of each band, inclusive. */
  lo: Int32Array;
  /** Last FFT bin of each band, exclusive. */
  hi: Int32Array;
  /** Per-band gain correcting music's natural roll-off at high frequencies. */
  tilt: Float32Array;
}

/**
 * Maps FFT bins onto bands spaced by octave.
 *
 * FFT bins are linear in frequency: with a 2048-point transform at 44.1 kHz,
 * bin 0 covers 0–21 Hz and bin 1023 covers 21.5–21.6 kHz. Drawing one bar per
 * bin puts every musically interesting frequency in the leftmost sliver of the
 * display and wastes the other 90% on inaudible detail. Spacing the bands
 * logarithmically gives each octave the same width, which is how the ear hears.
 *
 * Bands are widened to at least one bin. At the bottom of the range an octave
 * is narrower than a single bin, so several bands would otherwise share a bin
 * and move in lockstep.
 */
export function buildLogBands(
  sampleRate: number,
  fftSize: number,
  nBands: number,
  minHz: number,
  maxHz: number,
): LogBandTable {
  const binCount = Math.floor(fftSize / 2);
  const hzPerBin = sampleRate / fftSize;
  const ratio = maxHz / minHz;

  const lo = new Int32Array(nBands);
  const hi = new Int32Array(nBands);
  const tilt = new Float32Array(nBands);

  for (let i = 0; i < nBands; i++) {
    const loHz = minHz * Math.pow(ratio, i / nBands);
    const hiHz = minHz * Math.pow(ratio, (i + 1) / nBands);

    const l = Math.max(0, Math.min(Math.floor(loHz / hzPerBin), binCount - 1));
    const h = Math.max(l + 1, Math.min(Math.ceil(hiHz / hzPerBin), binCount));

    lo[i] = l;
    hi[i] = h;
    // Roughly +4.5 dB per octave across the range.
    tilt[i] = 1 + 1.35 * (i / nBands);
  }

  return { lo, hi, tilt };
}

/**
 * Reduces a time-domain signal to per-bucket minimum, maximum and RMS.
 *
 * This is the difference between a waveform that looks solid and one that
 * flickers. Taking every Nth sample instead lands at an arbitrary point in each
 * oscillation, so a steady loud tone renders as a thin wandering line whose
 * height depends on nothing but luck. Taking the extremes of each bucket
 * reproduces the envelope, which is what an audio editor draws.
 *
 * Output arrays are written in place and must each hold `buckets` entries.
 */
export function reduceEnvelope(
  samples: Float32Array,
  buckets: number,
  outMin: Float32Array,
  outMax: Float32Array,
  outRms: Float32Array,
): void {
  const n = samples.length;
  if (buckets <= 0) return;

  const perBucket = n / buckets;

  for (let i = 0; i < buckets; i++) {
    const start = Math.floor(i * perBucket);
    const end = Math.min(n, Math.floor((i + 1) * perBucket));

    if (end <= start) {
      outMin[i] = 0;
      outMax[i] = 0;
      outRms[i] = 0;
      continue;
    }

    let lo = Infinity;
    let hi = -Infinity;
    let sumSq = 0;
    for (let s = start; s < end; s++) {
      const v = samples[s];
      if (v < lo) lo = v;
      if (v > hi) hi = v;
      sumSq += v * v;
    }

    outMin[i] = lo;
    outMax[i] = hi;
    outRms[i] = Math.sqrt(sumSq / (end - start));
  }
}

/**
 * Moves `cur` toward `target` by an exponential envelope measured in seconds.
 *
 * A constant per-frame lerp ties the feel to the frame rate: at 30 fps a bar
 * releases at half the speed it does at 60 fps, and at 144 Hz it snaps more
 * than twice as fast. Scaling the step by 1 - exp(-dt / tau) makes the curve
 * depend on elapsed time only. `attackSecs` applies when rising and
 * `releaseSecs` when falling; pass the same value for a symmetric follower.
 */
export function followEnvelope(
  cur: number,
  target: number,
  attackSecs: number,
  releaseSecs: number,
  dt: number,
): number {
  if (dt <= 0) return cur;
  const tau = target > cur ? attackSecs : releaseSecs;
  return cur + (target - cur) * (1 - Math.exp(-dt / tau));
}

/**
 * Time constant that reproduces a per-frame lerp factor `alpha` at 60 fps.
 *
 * One frame of followEnvelope at dt = 1/60 moves by 1 - exp(-1 / (60 tau)).
 * Setting that equal to alpha and solving gives tau = -1 / (60 ln(1 - alpha)).
 */
export function tauFor60fps(alpha: number): number {
  return -1 / (60 * Math.log(1 - alpha));
}
