/* Mirror — a woven interferometer: paired signal filaments inside fine envelopes. */
import type * as PIXI from 'pixi.js';
import type { AudioEngine } from '../audio/AudioEngine';
import type { AudioFrame } from './types';
import { WaveScene, type WaveSceneOptions } from './WaveScene';
import { rampByName } from './palette';

const POINTS = 160;
const STRANDS = 7;

export class MirrorWaveVisualizer extends WaveScene {
  private readonly level = new Float32Array(POINTS);
  private readonly envelope = new Float32Array(POINTS);
  private readonly signal = new Float32Array(POINTS);
  private readonly xs = new Float32Array(POINTS);
  private readonly ys = new Float32Array(POINTS);
  private readonly window = Float32Array.from({ length: POINTS }, (_, i) =>
    Math.pow(Math.sin(Math.PI * i / (POINTS - 1)), 0.72));
  private readonly colours: number[];
  private gain = 1;

  constructor(app: PIXI.Application, audio: AudioEngine, options: WaveSceneOptions = {}) {
    super(app, audio, options, { wavePoints: POINTS, bands: 48, bloom: 0.55, bandHeight: 0.30 });
    const ramp = rampByName(options.ramp ?? 'NEON');
    this.colours = Array.from({ length: STRANDS }, (_, i) => ramp.hex(i / (STRANDS - 1)));
  }

  protected onResize() {
    const span = Math.max(1, this.W - 2 * this.marginX);
    for (let i = 0; i < POINTS; i++) this.xs[i] = this.marginX + span * i / (POINTS - 1);
  }

  protected draw(frame: AudioFrame, dt: number) {
    const g = this.gfx;
    const activity = this.audio.playing ? 1 : 0.65;
    const attack = 1 - Math.exp(-dt / 0.025);
    const release = 1 - Math.exp(-dt / 0.18);
    const follow = 1 - Math.exp(-dt / 0.045);
    const amp = this.halfBand * (0.9 + Math.min(1, this.beatPulse) * 0.04);
    // Capped peak-followed gain keeps quiet masters legible without pumping.
    const targetGain = Math.min(4, Math.max(1, 0.32 / Math.max(0.04, frame.peak)));
    this.gain += (targetGain - this.gain) * (1 - Math.exp(-dt / 0.4));

    for (let i = 0; i < POINTS; i++) {
      const peak = Math.max(Math.abs(frame.waveMin[i] ?? 0), Math.abs(frame.waveMax[i] ?? 0));
      const target = Math.min(1, (peak * 0.68 + (frame.waveRms[i] ?? 0) * 0.8) * this.gain) * activity;
      this.level[i] += (target - this.level[i]) * (target > this.level[i] ? attack : release);
      const sample = ((frame.waveMax[i] ?? 0) + (frame.waveMin[i] ?? 0)) * activity;
      this.signal[i] += (sample * 0.5 - this.signal[i]) * follow;
    }
    for (let i = 0; i < POINTS; i++) {
      const mean = (this.level[Math.max(0, i - 1)] + this.level[i] * 2
        + this.level[Math.min(POINTS - 1, i + 1)]) * 0.25;
      this.envelope[i] = (0.04 + mean * 0.94) * this.window[i] * amp;
    }

    // An almost transparent signal veil; never a wall of vertical bars.
    g.moveTo(this.xs[0], this.cy);
    for (let i = 1; i < POINTS; i++) g.lineTo(this.xs[i], this.cy - this.envelope[i]);
    for (let i = POINTS - 1; i >= 0; i--) g.lineTo(this.xs[i], this.cy + this.envelope[i]);
    g.closePath().fill({ color: this.colours[0], alpha: 0.035 });

    for (let strand = 0; strand < STRANDS; strand++) {
      const depth = strand / (STRANDS - 1);
      const band = (frame.bands[Math.floor(depth * (frame.bands.length - 1))] ?? 0) * activity;
      for (let i = 0; i < POINTS; i++) {
        const t = i / (POINTS - 1);
        const phase = t * Math.PI * (5 + strand * 0.72)
          - this.time * (0.17 + depth * 0.09) + strand * 0.85;
        // Nested helices exchange places: local excursion and spectral energy
        // control the weave, while the very slow carrier keeps silence restrained.
        const weave = Math.sin(phase + this.signal[i] * 1.4 + band * 0.65);
        this.ys[i] = this.envelope[i] * (0.13 + depth * 0.64)
          * weave + this.signal[i] * this.window[i] * amp * 0.12;
      }
      for (const side of [-1, 1]) {
        g.moveTo(this.xs[0], this.cy + this.ys[0] * side);
        for (let i = 1; i < POINTS; i++) g.lineTo(this.xs[i], this.cy + this.ys[i] * side);
        g.stroke({ color: this.colours[strand], width: depth > 0.8 ? 1.3 : 0.85,
          alpha: 0.3 + depth * 0.3, join: 'round', cap: 'round' });
      }
    }

    // Fine mirrored bounds make the measured envelope legible above the weave.
    for (const side of [-1, 1]) {
      g.moveTo(this.xs[0], this.cy);
      for (let i = 1; i < POINTS; i++) g.lineTo(this.xs[i], this.cy + side * this.envelope[i]);
      g.stroke({ color: 0xe9f5ee, width: 0.8, alpha: 0.35 });
    }
    g.moveTo(this.marginX, this.cy).lineTo(this.W - this.marginX, this.cy)
      .stroke({ color: 0xc9e9ed, width: 0.65, alpha: 0.18 });

    // Sparse paired metrology points, collected into a single fill.
    for (let i = 12; i < POINTS - 12; i += 17) {
      const h = this.envelope[i];
      g.circle(this.xs[i], this.cy - h, 1.15);
      g.circle(this.xs[i], this.cy + h, 1.15);
    }
    g.fill({ color: 0xf7c4b6, alpha: 0.55 });
  }
}
