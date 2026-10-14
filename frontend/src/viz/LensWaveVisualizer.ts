/* Prism array — separated, oblique glass blades with triangular caps,
 * refracted seams and a second, quieter depth plane. */
import type { AudioFrame } from './types';
import { WaveScene, type WaveSceneOptions } from './WaveScene';
import { rampByName, type Ramp } from './palette';
import * as PIXI from 'pixi.js';
import type { AudioEngine } from '../audio/AudioEngine';

const SEGMENTS = 32;
const IVORY = 0xe7f1ef;

export class LensWaveVisualizer extends WaveScene {
  private ramp: Ramp;
  private colours = new Uint32Array(SEGMENTS);
  private edges = new Uint32Array(SEGMENTS);
  private smooth = new Float32Array(SEGMENTS);
  private x = new Float32Array(SEGMENTS);
  private axis = new Float32Array(SEGMENTS);
  private tops = new Float32Array(SEGMENTS);
  private bottoms = new Float32Array(SEGMENTS);
  private bench = new PIXI.Graphics();
  private slot = 1;

  constructor(app: PIXI.Application, audio: AudioEngine, options: WaveSceneOptions = {}) {
    super(app, audio, options, {
      bands: SEGMENTS,
      wavePoints: SEGMENTS,
      bloom: 0.42,
      bandHeight: 0.30,
    });
    this.ramp = rampByName(options.ramp ?? 'NEON');
    for (let i = 0; i < SEGMENTS; i++) {
      const t = i / (SEGMENTS - 1);
      const [r, g, b] = this.ramp.sample(t);
      this.colours[i] = this.ramp.hex(t);
      this.edges[i] = (Math.round(r * 0.5 + 231 * 0.5) << 16)
        | (Math.round(g * 0.5 + 241 * 0.5) << 8)
        | Math.round(b * 0.5 + 239 * 0.5);
    }
    this.art.addChildAt(this.bench, 0);
  }

  protected onResize() {
    const g = this.bench.clear();
    const usable = Math.max(1, this.W - this.marginX * 2);
    this.slot = usable / SEGMENTS;
    for (let i = 0; i < SEGMENTS; i++) {
      const t = i / (SEGMENTS - 1);
      this.x[i] = this.marginX + (i + 0.5) * this.slot;
      this.axis[i] = this.cy + (0.5 - t) * this.halfBand * 0.18
        + Math.sin(t * Math.PI * 2) * this.halfBand * 0.055;
      // Small feet communicate an ordered optical array without boxing it in.
      const y = this.cy + this.halfBand * 1.18;
      g.moveTo(this.x[i] - this.slot * 0.16, y)
        .lineTo(this.x[i] + this.slot * 0.16, y);
      if (i % 4 === 0) {
        g.moveTo(this.x[i], y + 4).lineTo(this.x[i], y + 8);
      }
    }
    g.stroke({ color: IVORY, alpha: 0.20, width: 0.7 });

    // Perspective calibration rays are sparse and stay behind the crystals.
    const horizon = this.cy + this.halfBand * 0.92;
    for (let i = 0; i < 5; i++) {
      const x = this.marginX + usable * i / 4;
      g.moveTo(x, horizon + this.halfBand * 0.21)
        .lineTo(this.cx + (x - this.cx) * 0.84, horizon - this.halfBand * 0.07);
    }
    g.moveTo(this.marginX, horizon)
      .lineTo(this.W - this.marginX, horizon - this.halfBand * 0.06)
      .stroke({ color: 0x9bbbd1, alpha: 0.075, width: 0.7 });
    for (let side = -1; side <= 1; side += 2) {
      const x = side < 0 ? this.marginX - 9 : this.W - this.marginX + 9;
      g.moveTo(x, this.cy - 5).lineTo(x, this.cy + 5)
        .moveTo(x - 3, this.cy).lineTo(x + 3, this.cy);
    }
    g.stroke({ color: IVORY, alpha: 0.38, width: 0.7 });
  }

  protected draw(frame: AudioFrame, dt: number) {
    const g = this.gfx;
    const attack = 1 - Math.exp(-dt / 0.025);
    const release = 1 - Math.exp(-dt / 0.19);

    for (let i = 0; i < SEGMENTS; i++) {
      const target = Math.min(1, frame.bands[i] * 0.83 + frame.waveRms[i] * 0.95);
      const current = this.smooth[i];
      const level = current + (target - current) * (target > current ? attack : release);
      this.smooth[i] = level;
      const t = i / (SEGMENTS - 1);
      const window = 0.65 + 0.35 * Math.sin(t * Math.PI);
      const h = this.halfBand * (0.09 + level * 1.02) * window;
      const asymmetry = Math.sin(this.time * 0.65 + i * 0.42) * 0.07;
      const upper = h * (1.03 + asymmetry);
      const lower = h * (0.73 - asymmetry);
      const x = this.x[i];
      const y = this.axis[i];
      const w = this.slot * (0.32 + level * 0.09);
      const bevel = Math.min(h * 0.27, this.slot * 0.42);
      const colour = this.colours[i];
      const edge = this.edges[i];
      this.tops[i] = y - upper;
      this.bottoms[i] = y + lower;

      // A displaced rear face gives parallax/depth, not a heavy outer halo.
      const dx = this.slot * 0.13;
      const dy = -this.halfBand * 0.085;
      g.moveTo(x - w + dx, y - upper + dy)
        .lineTo(x + w + dx, y - upper - bevel * 0.2 + dy)
        .lineTo(x + w + dx, y + lower - bevel + dy)
        .lineTo(x - w + dx, y + lower - bevel * 0.3 + dy)
        .closePath().fill({ color: colour, alpha: 0.035 });

      // Broad front plane, cool darker side, and a narrow luminous top cap.
      // These are extruded triangular prisms, not mirrored diamond silhouettes.
      g.moveTo(x - w, y - upper)
        .lineTo(x + w * 0.18, y - upper + bevel)
        .lineTo(x + w * 0.18, y + lower)
        .lineTo(x - w, y + lower - bevel * 0.3)
        .closePath().fill({ color: colour, alpha: 0.105 + level * 0.075 });
      g.moveTo(x + w * 0.18, y - upper + bevel)
        .lineTo(x + w, y - upper - bevel * 0.2)
        .lineTo(x + w, y + lower - bevel)
        .lineTo(x + w * 0.18, y + lower)
        .closePath().fill({ color: colour, alpha: 0.055 + level * 0.035 });
      g.moveTo(x - w, y - upper)
        .lineTo(x + w * 0.18, y - upper + bevel)
        .lineTo(x + w, y - upper - bevel * 0.2)
        .closePath().fill({ color: edge, alpha: 0.22 + level * 0.12 });
      g.moveTo(x - w, y + lower - bevel * 0.3)
        .lineTo(x + w * 0.18, y + lower)
        .lineTo(x + w, y + lower - bevel)
        .closePath().fill({ color: colour, alpha: 0.09 });

      // Silhouette, ridge and cap edges share a single fine-stroke operation.
      g.moveTo(x - w, y - upper)
        .lineTo(x + w, y - upper - bevel * 0.2)
        .lineTo(x + w, y + lower - bevel)
        .lineTo(x + w * 0.18, y + lower)
        .lineTo(x - w, y + lower - bevel * 0.3)
        .closePath()
        .moveTo(x - w, y - upper)
        .lineTo(x + w * 0.18, y - upper + bevel)
        .lineTo(x + w, y - upper - bevel * 0.2)
        .moveTo(x + w * 0.18, y - upper + bevel)
        .lineTo(x + w * 0.18, y + lower)
        .stroke({ color: edge, alpha: 0.43 + level * 0.21, width: 0.8, join: 'bevel' });

      // A hairline internal reflection is oblique instead of a white outline.
      g.moveTo(x - w * 0.7, y - upper + bevel * 0.5)
        .lineTo(x + w * 0.03, y + lower - bevel * 0.4)
        .stroke({ color: IVORY, alpha: 0.10 + level * 0.08, width: 0.6 });

      // Each blade bends a small portion of the carrier beam.
      g.moveTo(x - this.slot * 0.48, y + bevel * 0.12)
        .lineTo(x - w, y + bevel * 0.12)
        .lineTo(x + w * 0.18, y - bevel * (0.3 + level * 0.3))
        .lineTo(x + this.slot * 0.48, y - bevel * 0.16)
        .stroke({ color: colour, alpha: 0.15 + level * 0.12, width: 0.7 });
    }

    // Spectral envelopes float off the glass, joining the separated blades.
    g.moveTo(this.x[0], this.tops[0] - 9);
    for (let i = 1; i < SEGMENTS; i++) g.lineTo(this.x[i], this.tops[i] - 9);
    g.stroke({ color: IVORY, alpha: 0.12, width: 0.65 });
    g.moveTo(this.x[0], this.bottoms[0] + 9);
    for (let i = 1; i < SEGMENTS; i++) g.lineTo(this.x[i], this.bottoms[i] + 9);
    g.stroke({ color: 0x91bfd0, alpha: 0.065, width: 0.65 });
    for (let i = 0; i < SEGMENTS; i += 4) {
      g.circle(this.x[i], this.tops[i] - 9, 1.1 + Math.min(1, this.beatPulse) * 0.5);
    }
    g.fill({ color: IVORY, alpha: 0.62 });
  }
}
