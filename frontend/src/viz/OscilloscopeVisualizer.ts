/* Scope — a precise phosphor instrument with triggered dual traces and a phase portrait.
 * The analyser is mono: the second trace is a quarter-period-delayed view of
 * that same real signal, not invented stereo or an ornamental oscillator.
 */
import * as PIXI from 'pixi.js';
import type { AudioEngine } from '../audio/AudioEngine';
import type { AudioFrame } from './types';
import { WaveScene, type WaveSceneOptions } from './WaveScene';
import { rampByName } from './palette';

const POINTS = 256;
const TRAILS = 3;
const PORTRAIT_POINTS = 160;

export class OscilloscopeVisualizer extends WaveScene {
  private readonly graticule = new PIXI.Graphics();
  private readonly a = new Float32Array(POINTS);
  private readonly b = new Float32Array(POINTS);
  private readonly trailA = new Float32Array(POINTS * TRAILS);
  private readonly trailB = new Float32Array(POINTS * TRAILS);
  private readonly xs = new Float32Array(POINTS);
  private readonly colourA: number;
  private readonly colourB: number;
  private gain = 1;
  private trailHead = 0;
  private trailClock = 0;
  private xEnd = 0;
  private portraitX = 0;
  private portraitRadius = 1;

  constructor(app: PIXI.Application, audio: AudioEngine, options: WaveSceneOptions = {}) {
    super(app, audio, options, { wavePoints: POINTS, bands: 48, bloom: 0.45, bandHeight: 0.30 });
    const ramp = rampByName(options.ramp ?? 'AURORA');
    this.colourA = ramp.hex(0);
    this.colourB = ramp.hex(1);
    // Static, unfiltered instrument geometry sits beneath the luminous signal.
    this.root.addChildAt(this.graticule, 1);
  }

  protected onResize() {
    const span = Math.max(1, this.W - this.marginX * 2);
    const side = Math.min(span * 0.23, this.halfBand * 1.5);
    const gap = span * 0.045;
    this.xEnd = this.W - this.marginX - side - gap;
    this.portraitX = this.W - this.marginX - side * 0.5;
    this.portraitRadius = Math.max(1, Math.min(side * 0.44, this.halfBand * 0.64));
    for (let i = 0; i < POINTS; i++) this.xs[i] = this.marginX + (this.xEnd - this.marginX) * i / (POINTS - 1);

    const grid = this.graticule;
    grid.clear();
    const top = this.cy - this.halfBand * 0.83;
    const bottom = this.cy + this.halfBand * 0.83;
    const width = this.xEnd - this.marginX;
    for (let col = 0; col <= 10; col++) {
      const x = this.marginX + width * col / 10;
      grid.moveTo(x, top).lineTo(x, bottom);
    }
    for (let row = 0; row <= 6; row++) {
      const y = top + (bottom - top) * row / 6;
      grid.moveTo(this.marginX, y).lineTo(this.xEnd, y);
    }
    grid.stroke({ color: 0x84b9c9, alpha: 0.075, width: 0.65 });
    grid.moveTo(this.marginX, this.cy).lineTo(this.xEnd, this.cy);
    for (let tick = 1; tick < 50; tick++) {
      const x = this.marginX + width * tick / 50;
      const h = tick % 5 === 0 ? 3.5 : 1.7;
      grid.moveTo(x, this.cy - h).lineTo(x, this.cy + h);
    }
    grid.stroke({ color: 0xb5d4d9, alpha: 0.18, width: 0.75 });

    // Open registration corners rather than a filled panel or UI frame.
    const corner = Math.min(12, width * 0.04);
    for (const x of [this.marginX, this.xEnd]) {
      const sx = x === this.marginX ? 1 : -1;
      for (const y of [top, bottom]) {
        const sy = y === top ? 1 : -1;
        grid.moveTo(x, y + sy * corner).lineTo(x, y).lineTo(x + sx * corner, y);
      }
    }
    grid.stroke({ color: 0xc5e1df, alpha: 0.25, width: 0.9 });

    const r = this.portraitRadius;
    grid.circle(this.portraitX, this.cy, r).stroke({ color: 0x84b9c9, alpha: 0.13, width: 0.7 });
    grid.circle(this.portraitX, this.cy, r * 0.5).stroke({ color: 0x84b9c9, alpha: 0.07, width: 0.65 });
    grid.moveTo(this.portraitX - r, this.cy).lineTo(this.portraitX + r, this.cy);
    grid.moveTo(this.portraitX, this.cy - r).lineTo(this.portraitX, this.cy + r);
    for (let i = 0; i < 16; i++) {
      const angle = i * Math.PI / 8;
      const c = Math.cos(angle);
      const s = Math.sin(angle);
      grid.moveTo(this.portraitX + c * r * 0.95, this.cy + s * r * 0.95)
        .lineTo(this.portraitX + c * r, this.cy + s * r);
    }
    grid.stroke({ color: 0xc1dde0, alpha: 0.14, width: 0.7 });
  }

  protected draw(frame: AudioFrame, dt: number) {
    const td = frame.timeDomain;
    const activity = this.audio.playing ? 1 : 0.65;
    const length = td.length;
    let start = 0;
    let period = Math.max(16, Math.floor(length / 12));
    // Rising-edge triggering reduces arbitrary frame-to-frame phase flicker.
    if (frame.peak > 0.008) {
      const limit = Math.floor(length * 0.3);
      for (let i = 1; i < limit; i++) {
        if (td[i - 1] <= 0 && td[i] > 0) { start = i; break; }
      }
      for (let i = start + 8; i < Math.min(length - 1, start + length * 0.25); i++) {
        if (td[i - 1] <= 0 && td[i] > 0) { period = i - start; break; }
      }
    }
    const delay = Math.max(2, Math.min(Math.floor(period / 4), Math.floor(length * 0.06)));
    const span = Math.max(1, Math.min(length - start - delay - 2, Math.max(128, period * 6)));
    const targetGain = Math.min(2.2, Math.max(1, 0.75 / Math.max(0.05, frame.peak)));
    this.gain += (targetGain - this.gain) * (1 - Math.exp(-dt / 0.4));
    const follow = 1 - Math.exp(-dt / 0.022);
    const idle = Math.max(0, 1 - frame.rms * 80) * 0.004;
    for (let i = 0; i < POINTS; i++) {
      const t = i / (POINTS - 1);
      const index = start + t * span;
      const rawA = sampleAt(td, index) * this.gain * activity;
      const rawB = sampleAt(td, index + delay) * this.gain * activity;
      const drift = Math.sin(t * Math.PI * 2 - this.time * 0.16) * idle;
      this.a[i] += (Math.max(-1, Math.min(1, rawA)) + drift - this.a[i]) * follow;
      this.b[i] += (Math.max(-1, Math.min(1, rawB)) - drift - this.b[i]) * follow;
    }

    this.trailClock += dt;
    if (this.trailClock >= 0.055) {
      this.trailClock %= 0.055;
      this.trailHead = (this.trailHead + 1) % TRAILS;
      this.trailA.set(this.a, this.trailHead * POINTS);
      this.trailB.set(this.b, this.trailHead * POINTS);
    }
    const g = this.gfx;
    const amp = this.halfBand * 0.56;
    const centreA = this.cy - this.halfBand * 0.17;
    const centreB = this.cy + this.halfBand * 0.17;
    for (let age = TRAILS - 1; age >= 0; age--) {
      const slot = (this.trailHead - age + TRAILS) % TRAILS;
      this.trace(this.trailA, slot * POINTS, centreA, amp);
      g.stroke({ color: this.colourA, alpha: 0.025 + (TRAILS - age) * 0.018, width: 0.8 });
      this.trace(this.trailB, slot * POINTS, centreB, amp);
      g.stroke({ color: this.colourB, alpha: 0.02 + (TRAILS - age) * 0.014, width: 0.8 });
    }
    this.trace(this.a, 0, centreA, amp);
    g.stroke({ color: this.colourA, width: 3.2, alpha: 0.13, join: 'round' });
    this.trace(this.b, 0, centreB, amp);
    g.stroke({ color: this.colourB, width: 2.8, alpha: 0.1, join: 'round' });
    this.trace(this.b, 0, centreB, amp);
    g.stroke({ color: this.colourB, width: 1.05, alpha: 0.78, join: 'round' });
    this.trace(this.a, 0, centreA, amp);
    g.stroke({ color: this.colourA, width: 1.3, alpha: 0.95, join: 'round' });
    this.trace(this.a, 0, centreA, amp);
    g.stroke({ color: 0xeaf9f0, width: 0.55, alpha: 0.72, join: 'round' });

    // A delay-derived Lissajous/phase portrait is plotted from the same samples.
    for (let pass = 0; pass < 2; pass++) {
      for (let i = 0; i < PORTRAIT_POINTS; i++) {
        const p = Math.floor(i / (PORTRAIT_POINTS - 1) * (POINTS - 1));
        const x = this.portraitX + this.a[p] * this.portraitRadius * 0.87;
        const y = this.cy - this.b[p] * this.portraitRadius * 0.87;
        if (i === 0) g.moveTo(x, y); else g.lineTo(x, y);
      }
      g.stroke({ color: this.colourA, alpha: pass === 0 ? 0.12 : 0.64,
        width: pass === 0 ? 2.8 : 0.85, join: 'round' });
    }
    // A small, crisp acquisition point — no drifting particle cloud.
    const p = Math.floor(POINTS * 0.72);
    g.circle(this.xs[p], centreA - this.a[p] * amp, 1.6)
      .fill({ color: 0xf4f8e9, alpha: 0.85 });
  }

  private trace(values: Float32Array, offset: number, centre: number, amp: number) {
    this.gfx.moveTo(this.xs[0], centre - values[offset] * amp);
    for (let i = 1; i < POINTS; i++) this.gfx.lineTo(this.xs[i], centre - values[offset + i] * amp);
  }
}

function sampleAt(values: Float32Array, index: number): number {
  if (!values.length) return 0;
  const p = Math.max(0, Math.min(values.length - 1, index));
  const i = Math.floor(p);
  const a = values[i];
  return a + ((values[Math.min(values.length - 1, i + 1)] ?? a) - a) * (p - i);
}
