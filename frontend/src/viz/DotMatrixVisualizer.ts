/* Constellation matrix — a curved frequency lattice suspended in a deeper
 * star field. Dots, rather than columns or filled waves, carry the signal. */
import type { AudioFrame } from './types';
import { WaveScene, type WaveSceneOptions } from './WaveScene';
import { rampByName, type Ramp } from './palette';
import * as PIXI from 'pixi.js';
import type { AudioEngine } from '../audio/AudioEngine';

const BAND_COUNT = 48;
const ROWS = 17;
const POINTS = BAND_COUNT * ROWS;
const IVORY = 0xe7f1ef;

export class DotMatrixVisualizer extends WaveScene {
  private ramp: Ramp;
  private colours = new Uint32Array(BAND_COUNT);
  private sky = new PIXI.Graphics();
  private x = new Float32Array(POINTS);
  private y = new Float32Array(POINTS);
  private radius = new Float32Array(POINTS);
  private threshold = new Float32Array(POINTS);
  private brightness = new Float32Array(POINTS);
  private displacement = new Float32Array(BAND_COUNT);
  private peakX = new Float32Array(BAND_COUNT);
  private peakY = new Float32Array(BAND_COUNT);
  private baseRadius = 1;

  constructor(app: PIXI.Application, audio: AudioEngine, options: WaveSceneOptions = {}) {
    super(app, audio, options, {
      bands: BAND_COUNT,
      wavePoints: 128,
      bloom: 0.55,
      bandHeight: 0.30,
    });
    this.ramp = rampByName(options.ramp ?? 'NEON');
    for (let i = 0; i < BAND_COUNT; i++) {
      this.colours[i] = this.ramp.hex(i / (BAND_COUNT - 1));
    }
    this.art.addChildAt(this.sky, 0);
  }

  protected onResize() {
    const g = this.sky.clear();
    const usable = Math.max(1, this.W - this.marginX * 2);
    const slot = usable / BAND_COUNT;
    this.baseRadius = Math.max(0.6, Math.min(3.4, slot * 0.16, this.halfBand * 0.016));

    // Deterministic deep stars: no random flicker or allocation during draw.
    for (let depth = 0; depth < 3; depth++) {
      for (let i = depth; i < 135; i += 3) {
        const u = ((i * 0.61803398875 + 0.17) % 1);
        const v = ((i * 0.41421356237 + 0.31) % 1);
        const x = this.marginX + u * usable;
        const y = this.cy + (v - 0.5) * this.halfBand * 2.8;
        g.circle(x, y, (0.5 + depth * 0.32) * Math.max(0.6, this.baseRadius * 0.45));
      }
      g.fill({ color: depth === 2 ? 0x79a8bc : IVORY, alpha: 0.10 + depth * 0.035 });
    }

    // Sparse star-chart connections live behind the main sheet, never a web
    // of hundreds of lines competing with the luminous dots.
    for (let i = 0; i < 12; i++) {
      const x = this.marginX + usable * (0.07 + i * 0.075);
      const y = this.cy + Math.sin(i * 1.91) * this.halfBand * 1.13;
      g.moveTo(x, y).lineTo(x + slot * 1.4, y - Math.cos(i * 2.1) * this.halfBand * 0.18)
        .lineTo(x + slot * 2.1, y + this.halfBand * 0.08);
    }
    g.stroke({ color: 0x81b9ca, alpha: 0.09, width: 0.7 });

    const tilt = Math.min(0.11, this.halfBand * 0.4 / usable);
    g.save().translateTransform(this.cx, this.cy).rotateTransform(-tilt);
    g.ellipse(0, 0, usable * 0.49, this.halfBand * 1.1)
      .ellipse(0, 0, usable * 0.43, this.halfBand * 0.94)
      .stroke({ color: IVORY, alpha: 0.045, width: 0.7 });
    g.restore();

    for (let col = 0; col < BAND_COUNT; col++) {
      const u = col / (BAND_COUNT - 1);
      const window = 0.38 + 0.62 * Math.pow(Math.sin(Math.PI * u), 0.6);
      const arc = Math.sin(u * Math.PI * 2) * this.halfBand * 0.13;
      for (let row = 0; row < ROWS; row++) {
        const index = col * ROWS + row;
        const v = (row - (ROWS - 1) / 2) / ((ROWS - 1) / 2);
        const depth = 0.58 + 0.42 * (0.5 + 0.5 * Math.sin(col * 2.39 + row * 1.71));
        this.x[index] = this.marginX + (col + 0.5) * slot
          + Math.sin(row * 1.7 + col * 0.6) * slot * 0.14 + v * slot * 0.15;
        this.y[index] = this.cy + arc + v * this.halfBand * window * 0.97;
        this.radius[index] = this.baseRadius * depth * (1 - Math.abs(v) * 0.27);
        this.threshold[index] = Math.pow(Math.abs(v), 1.3) * 0.88;
        g.circle(this.x[index], this.y[index], this.radius[index] * 0.42);
      }
    }
    // Unlit positions remain a delicate navigational lattice, not dead LEDs.
    g.fill({ color: 0x9bb9cc, alpha: 0.13 });

    for (let side = -1; side <= 1; side += 2) {
      const x = this.cx + side * usable * 0.49;
      g.moveTo(x - 4, this.cy).lineTo(x + 4, this.cy)
        .moveTo(x, this.cy - 4).lineTo(x, this.cy + 4);
    }
    g.stroke({ color: IVORY, alpha: 0.35, width: 0.7 });
  }

  protected draw(frame: AudioFrame, _dt: number) {
    const g = this.gfx;
    for (let col = 0; col < BAND_COUNT; col++) {
      const level = Math.min(1, Math.max(0, frame.bands[col]));
      // A gently travelling sheet, with bass pushing the near plane and air
      // igniting outer points. Slow motion also gives silence a composed rest.
      const drift = Math.sin(this.time * 0.48 + col * 0.17) * this.halfBand * 0.018;
      this.displacement[col] = drift + Math.sin(col * 0.31 + this.time * 0.7)
        * this.halfBand * frame.bass * 0.025;
      for (let row = 0; row < ROWS; row++) {
        const index = col * ROWS + row;
        const scintillation = 0.88 + 0.12 * Math.sin(this.time * 1.1 + col * 0.9 + row * 2.1);
        const light = Math.max(0, Math.min(1, (level - this.threshold[index]) * 5 + 0.13));
        this.brightness[index] = light * scintillation;
      }

      // Three brightness/depth buckets per band, not one fill or stroke per dot.
      for (let layer = 0; layer < 3; layer++) {
        let count = 0;
        for (let row = 0; row < ROWS; row++) {
          const index = col * ROWS + row;
          const light = this.brightness[index];
          if (light <= 0.06 || Math.min(2, Math.floor(light * 3)) !== layer) continue;
          const v = (row - 8) / 8;
          const dy = this.displacement[col] * (1 + Math.abs(v));
          const r = this.radius[index] * (0.58 + light * 0.52);
          g.circle(this.x[index], this.y[index] + dy, r);
          count++;
        }
        if (count > 0) g.fill({ color: this.colours[col], alpha: 0.20 + layer * 0.29 });
      }

      const peakRow = Math.max(0, 8 - Math.round(Math.min(1, frame.bandPeaks[col]) * 8));
      const peakIndex = col * ROWS + peakRow;
      this.peakX[col] = this.x[peakIndex];
      this.peakY[col] = this.y[peakIndex] + this.displacement[col] * (1 + (8 - peakRow) / 8);

      // Only eight selected points receive a broad, translucent halo.
      if (col % 6 === 2) {
        const index = col * ROWS + 8;
        g.circle(this.x[index], this.y[index] + this.displacement[col],
          this.baseRadius * (2.2 + level * 1.5))
          .fill({ color: this.colours[col], alpha: 0.035 + level * 0.04 });
      }
    }

    // Near-plane ivory stars and peak satellites are batched across the sheet.
    for (let col = 0; col < BAND_COUNT; col++) {
      const index = col * ROWS + 8;
      if (col % 3 === 0) {
        g.circle(this.x[index], this.y[index] + this.displacement[col], this.baseRadius * 0.43);
      }
      if (col % 2 === 0 && frame.bandPeaks[col] > 0.04) {
        g.circle(this.peakX[col], this.peakY[col], this.baseRadius * 0.38);
      }
    }
    g.fill({ color: IVORY, alpha: 0.88 });

    // A few pin-sharp diffraction crosses give scale without overbloom.
    for (let col = 5; col < BAND_COUNT; col += 12) {
      const index = col * ROWS + 8;
      const x = this.x[index];
      const y = this.y[index] + this.displacement[col];
      const arm = this.baseRadius * (1.8 + frame.bands[col] * 1.7 + this.beatPulse * 0.35);
      g.moveTo(x - arm, y).lineTo(x + arm, y)
        .moveTo(x, y - arm).lineTo(x, y + arm);
    }
    g.stroke({ color: IVORY, alpha: 0.4, width: 0.65 });
  }
}
