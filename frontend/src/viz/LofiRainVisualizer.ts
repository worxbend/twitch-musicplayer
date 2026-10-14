/* The station's signature: an orbital record under a fine meteor shower.
 * Static grooves are rebuilt only on resize. The outer needles, orbital
 * particles and two wave ribbons share the display-ready audio frame.
 */
import * as PIXI from 'pixi.js';
import { WaveScene, type WaveSceneOptions } from './WaveScene';
import { rampByName, type Ramp } from './palette';
import type { AudioEngine } from '../audio/AudioEngine';
import type { AudioFrame } from './types';

const TAU = Math.PI * 2;
const NEEDLES = 96;
const POINTS = 112;

export class LofiRainVisualizer extends WaveScene {
  private readonly platter = new PIXI.Graphics();
  private readonly ramp: Ramp;
  private readonly colors: number[];
  private radius = 160;
  private mounted = false;

  constructor(app: PIXI.Application, audio: AudioEngine, options: WaveSceneOptions = {}) {
    super(app, audio, options, { bands: NEEDLES, wavePoints: POINTS, bloom: 0.45, bandHeight: 0.28 });
    this.ramp = rampByName(options.ramp ?? 'NEON');
    this.colors = Array.from({ length: NEEDLES }, (_, i) => this.ramp.hex(i / (NEEDLES - 1)));
  }

  protected onResize() {
    if (!this.mounted) {
      this.art.addChildAt(this.platter, 0);
      this.mounted = true;
    }
    this.radius = Math.min(this.W * 0.21, this.halfBand * 0.78);
    const r = this.radius;
    const g = this.platter.clear();
    g.circle(this.cx, this.cy, r).fill({ color: 0x0e1727, alpha: 0.85 });
    for (let ring = 0; ring < 20; ring++) {
      g.circle(this.cx, this.cy, r * (0.27 + ring * 0.034));
    }
    g.stroke({ color: 0x8198ad, alpha: 0.15, width: 1 });
    g.circle(this.cx, this.cy, r * 0.97).stroke({ color: 0xa3e9e5, alpha: 0.36, width: 1 });
    g.circle(this.cx, this.cy, r * 0.26).fill({ color: 0x112c38, alpha: 0.9 })
      .stroke({ color: 0x97e6db, alpha: 0.65, width: 1 });
    g.circle(this.cx, this.cy, r * 0.15).stroke({ color: 0x97e6db, alpha: 0.24, width: 1 });
    g.circle(this.cx, this.cy, 3).fill(0xe9f4ed);
    // A sweep of engraved marks gives the record a physical, directional face.
    for (let i = 0; i < 36; i++) {
      const a = i * TAU / 36;
      const inner = r * (i % 3 === 0 ? 0.89 : 0.93);
      g.moveTo(this.cx + Math.cos(a) * inner, this.cy + Math.sin(a) * inner)
        .lineTo(this.cx + Math.cos(a) * r * 0.96, this.cy + Math.sin(a) * r * 0.96);
    }
    g.stroke({ color: 0xc9ddd9, alpha: 0.35, width: 1 });
  }

  protected draw(frame: AudioFrame, _dt: number) {
    const g = this.gfx;
    const r = this.radius;
    const t = this.time;
    // Bent stereo ribbons thread behind the record, not through its label.
    for (let strand = 0; strand < 3; strand++) {
      const color = this.ramp.hex(strand / 2);
      for (const side of [-1, 1]) {
        for (let i = 0; i < POINTS; i++) {
          const u = i / (POINTS - 1);
          const x = this.marginX + u * (this.W - this.marginX * 2);
          const outside = Math.min(1, Math.abs(x - this.cx) / Math.max(1, r * 1.25));
          const envelope = Math.sin(u * Math.PI);
          const wave = frame.waveRms[i] * r * 0.55;
          const y = this.cy + side * ((r * (0.35 + strand * 0.13) + wave) * envelope)
            + Math.sin(u * TAU * 1.4 + t * 0.32 + strand * 0.7) * r * 0.1;
          if (i === 0 || outside < 0.83) g.moveTo(x, y);
          else g.lineTo(x, y);
        }
        g.stroke({ color, alpha: 0.25 + strand * 0.1, width: strand === 2 ? 1.4 : 1 });
      }
    }
    // Each needle reads a logarithmic frequency band, with a fine peak marker.
    for (let i = 0; i < NEEDLES; i++) {
      const a = i / NEEDLES * TAU - Math.PI / 2;
      const energy = frame.bands[i];
      const inner = r * 1.055;
      const outer = inner + r * (0.025 + energy * 0.32);
      const x = Math.cos(a), y = Math.sin(a);
      g.moveTo(this.cx + x * inner, this.cy + y * inner)
        .lineTo(this.cx + x * outer, this.cy + y * outer)
        .stroke({ color: this.colors[i], alpha: 0.5 + energy * 0.45, width: Math.max(1.2, r * 0.012) });
      const peak = inner + r * (0.035 + frame.bandPeaks[i] * 0.32);
      g.circle(this.cx + x * peak, this.cy + y * peak, 1).fill({ color: this.colors[i], alpha: 0.65 });
    }
    // Tilted orbital trajectories: crisp paths with sparse moving satellites.
    for (let orbit = 0; orbit < 2; orbit++) {
      const rx = r * (1.7 + orbit * 0.3);
      const ry = r * (0.52 + orbit * 0.14);
      const tilt = orbit === 0 ? -0.30 : 0.25;
      const color = orbit === 0 ? 0xa5e9dc : 0xffa88b;
      const cos = Math.cos(tilt), sin = Math.sin(tilt);
      for (let i = 0; i <= 100; i++) {
        const a = i / 100 * TAU;
        const px = Math.cos(a) * rx, py = Math.sin(a) * ry;
        const x = this.cx + px * cos - py * sin;
        const y = this.cy + px * sin + py * cos;
        if (i === 0) g.moveTo(x, y); else g.lineTo(x, y);
      }
      g.stroke({ color, alpha: 0.24, width: 1 });
      const a = t * (orbit === 0 ? 0.15 : -0.11) + orbit * 2.3;
      const px = Math.cos(a) * rx, py = Math.sin(a) * ry;
      const x = this.cx + px * cos - py * sin, y = this.cy + px * sin + py * cos;
      g.circle(x, y, 3 + frame.bass * 2).fill({ color, alpha: 0.9 });
      g.circle(x, y, 7 + frame.bass * 3).stroke({ color, alpha: 0.25, width: 1 });
    }
    // Fine rain lines are sparse and deterministic, with no particle allocation.
    for (let i = 0; i < 28; i++) {
      const x = this.marginX + ((i * 0.6180339) % 1) * (this.W - this.marginX * 2);
      const phase = (i * 0.371 + t * 0.025) % 1;
      const y = this.cy - this.halfBand + phase * this.halfBand * 2;
      if (Math.abs(x - this.cx) < r * 1.45) continue;
      const length = 4 + (i % 4) * 3;
      g.moveTo(x, y).lineTo(x - length * 0.3, y + length);
    }
    g.stroke({ color: 0x8ba5bb, alpha: 0.18, width: 1 });
    // Label grooves remain still; a tiny rotating indicator supplies record motion.
    const a = t * 0.16;
    g.circle(this.cx + Math.cos(a) * r * 0.2, this.cy + Math.sin(a) * r * 0.2, 2)
      .fill({ color: 0xe5f3e9, alpha: 0.8 });
  }
}
