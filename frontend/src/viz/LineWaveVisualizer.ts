/* Trace — a receding topographic survey of recent audio, not a single zigzag. */
import type * as PIXI from 'pixi.js';
import type { AudioEngine } from '../audio/AudioEngine';
import type { AudioFrame } from './types';
import { WaveScene, type WaveSceneOptions } from './WaveScene';
import { rampByName } from './palette';

const POINTS = 144;
const ROWS = 15;
const HISTORY_STEP = 0.075;

export class LineWaveVisualizer extends WaveScene {
  private readonly smooth = new Float32Array(POINTS);
  private readonly spatial = new Float32Array(POINTS);
  private readonly history = new Float32Array(POINTS * ROWS);
  private readonly xs = new Float32Array(POINTS * ROWS);
  private readonly ys = new Float32Array(POINTS * ROWS);
  private readonly window = Float32Array.from({ length: POINTS }, (_, i) =>
    Math.pow(Math.sin(Math.PI * i / (POINTS - 1)), 0.7));
  private readonly colours: number[];
  private head = 0;
  private clock = 0;
  private ready = false;

  constructor(app: PIXI.Application, audio: AudioEngine, options: WaveSceneOptions = {}) {
    super(app, audio, options, { wavePoints: POINTS, bands: 48, bloom: 0.45, bandHeight: 0.30 });
    const ramp = rampByName(options.ramp ?? 'NEON');
    this.colours = Array.from({ length: ROWS }, (_, i) => ramp.hex(0.85 - i / (ROWS - 1) * 0.85));
  }

  protected onResize() {
    const span = Math.max(1, this.W - this.marginX * 2);
    for (let row = 0; row < ROWS; row++) {
      const depth = row / (ROWS - 1);
      const width = span * (0.68 + depth * 0.32);
      for (let i = 0; i < POINTS; i++) {
        this.xs[row * POINTS + i] = this.cx + (i / (POINTS - 1) - 0.5) * width;
      }
    }
  }

  protected draw(frame: AudioFrame, dt: number) {
    const g = this.gfx;
    const activity = this.audio.playing ? 1 : 0.65;
    const follow = 1 - Math.exp(-dt / 0.06);
    const td = frame.timeDomain;
    for (let i = 0; i < POINTS; i++) {
      const t = i / (POINTS - 1);
      const peak = Math.max(Math.abs(frame.waveMin[i] ?? 0), Math.abs(frame.waveMax[i] ?? 0));
      const rms = frame.waveRms[i] ?? 0;
      const sample = td[Math.floor(t * Math.max(0, td.length - 1))] ?? 0;
      const band = frame.bands[Math.floor(t * Math.max(0, frame.bands.length - 1))] ?? 0;
      const target = (Math.min(1, rms * 1.65 + peak * 0.24 + band * 0.13) + sample * 0.12) * activity;
      this.smooth[i] += (target - this.smooth[i]) * follow;
    }
    for (let i = 0; i < POINTS; i++) {
      this.spatial[i] = (this.smooth[Math.max(0, i - 1)] + this.smooth[i] * 2
        + this.smooth[Math.min(POINTS - 1, i + 1)]) * 0.25;
    }

    // A bounded circular history, sampled by elapsed time rather than frame count.
    // Pause gaps never cause an unbounded catch-up loop.
    this.clock += dt;
    if (!this.ready) {
      for (let row = 0; row < ROWS; row++) this.history.set(this.spatial, row * POINTS);
      this.ready = true;
    } else if (this.clock >= HISTORY_STEP) {
      this.clock %= HISTORY_STEP;
      this.head = (this.head + 1) % ROWS;
      this.history.set(this.spatial, this.head * POINTS);
    }

    const blend = Math.min(1, this.clock / HISTORY_STEP);
    for (let row = 0; row < ROWS; row++) {
      const depth = row / (ROWS - 1);
      const age = ROWS - 1 - row;
      const slot = (this.head - age + ROWS) % ROWS;
      const next = (slot + 1) % ROWS;
      const base = this.cy + this.halfBand * (-0.4 + depth * 0.98);
      for (let i = 0; i < POINTS; i++) {
        const old = this.history[slot * POINTS + i];
        const newer = age === 0 ? this.spatial[i] : this.history[next * POINTS + i];
        const value = old + (newer - old) * blend;
        const idle = 0.008 * Math.sin(i / (POINTS - 1) * Math.PI * 4 + row * 0.22 - this.time * 0.12);
        this.ys[row * POINTS + i] = base - (value + idle) * this.window[i]
          * this.halfBand * (0.28 + depth * 0.53);
      }
    }

    // Sparse meridians describe depth without turning the survey into a wire wall.
    for (let i = 12; i < POINTS - 10; i += 20) {
      g.moveTo(this.xs[i], this.ys[i]);
      for (let row = 1; row < ROWS; row++) {
        const p = row * POINTS + i;
        g.lineTo(this.xs[p], this.ys[p]);
      }
    }
    g.stroke({ color: this.colours[ROWS - 1], alpha: 0.07, width: 0.6 });

    for (let row = 0; row < ROWS; row++) {
      const depth = row / (ROWS - 1);
      this.contour(row);
      g.stroke({ color: this.colours[row], alpha: 0.16 + depth * 0.56,
        width: 0.65 + depth * 0.65, join: 'round', cap: 'round' });
    }
    this.contour(ROWS - 1);
    g.stroke({ color: 0xe7f6ef, alpha: 0.58, width: 0.7, join: 'round' });

    // Just a few survey nodes, grouped into one fill (not a particle field).
    for (let row = 6; row < ROWS; row += 4) {
      for (let i = 22; i < POINTS - 15; i += 31) {
        const p = row * POINTS + i;
        g.circle(this.xs[p], this.ys[p], row === ROWS - 1 ? 1.15 : 0.8);
      }
    }
    g.fill({ color: 0xf6c0ad, alpha: 0.5 });
  }

  private contour(row: number) {
    const offset = row * POINTS;
    this.gfx.moveTo(this.xs[offset], this.ys[offset]);
    for (let i = 1; i < POINTS; i++) {
      this.gfx.lineTo(this.xs[offset + i], this.ys[offset + i]);
    }
  }
}
