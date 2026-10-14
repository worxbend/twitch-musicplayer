/* Ribbon — translucent aurora silk, suspended as five independently folded surfaces. */
import * as PIXI from 'pixi.js';
import type { AudioEngine } from '../audio/AudioEngine';
import type { AudioFrame } from './types';
import { WaveScene, type WaveSceneOptions } from './WaveScene';
import { rampByName, type Ramp } from './palette';

const POINTS = 112;
const SURFACES = 5;

export class RibbonVisualizer extends WaveScene {
  private readonly ramp: Ramp;
  private readonly level = new Float32Array(POINTS);
  private readonly shaped = new Float32Array(POINTS);
  private readonly signal = new Float32Array(POINTS);
  private readonly xs = new Float32Array(POINTS);
  private readonly top = new Float32Array(POINTS);
  private readonly bottom = new Float32Array(POINTS);
  private readonly window = Float32Array.from({ length: POINTS }, (_, i) =>
    Math.pow(Math.sin(Math.PI * i / (POINTS - 1)), 0.8));
  private readonly colours: number[];
  private gradient: PIXI.FillGradient | null = null;
  private gain = 1;

  constructor(app: PIXI.Application, audio: AudioEngine, options: WaveSceneOptions = {}) {
    super(app, audio, options, { wavePoints: POINTS, bands: 40, bloom: 0.5, bandHeight: 0.30 });
    this.ramp = rampByName(options.ramp ?? 'AURORA');
    this.colours = Array.from({ length: SURFACES }, (_, i) => this.ramp.hex(i / (SURFACES - 1)));
  }

  protected onResize() {
    const span = Math.max(1, this.W - this.marginX * 2);
    for (let i = 0; i < POINTS; i++) this.xs[i] = this.marginX + span * i / (POINTS - 1);
    const old = this.gradient;
    this.gradient = new PIXI.FillGradient({
      type: 'linear',
      start: { x: this.marginX, y: this.cy },
      end: { x: this.W - this.marginX, y: this.cy },
      textureSpace: 'global',
      colorStops: this.ramp.cssStops(9),
    });
    old?.destroy();
  }

  protected draw(frame: AudioFrame, dt: number) {
    const g = this.gfx;
    const activity = this.audio.playing ? 1 : 0.65;
    const follow = 1 - Math.exp(-dt / 0.11);
    const fast = 1 - Math.exp(-dt / 0.05);
    const amp = this.halfBand * (0.96 + Math.min(1, this.beatPulse) * 0.025);
    const targetGain = Math.min(4, Math.max(1, 0.32 / Math.max(0.04, frame.peak)));
    this.gain += (targetGain - this.gain) * (1 - Math.exp(-dt / 0.4));
    for (let i = 0; i < POINTS; i++) {
      const peak = Math.max(Math.abs(frame.waveMin[i] ?? 0), Math.abs(frame.waveMax[i] ?? 0));
      const target = Math.min(1, ((frame.waveRms[i] ?? 0) * 1.9 + peak * 0.18) * this.gain) * activity;
      this.level[i] += (target - this.level[i]) * follow;
      this.signal[i] += (((frame.waveMax[i] ?? 0) + (frame.waveMin[i] ?? 0)) * 0.5 * activity
        - this.signal[i]) * fast;
    }
    // Fixed triangular neighbourhood, in reusable storage, softens the cloth
    // without losing changes of loudness along the actual waveform.
    for (let i = 0; i < POINTS; i++) {
      let sum = 0;
      for (let k = -3; k <= 3; k++) {
        sum += this.level[Math.max(0, Math.min(POINTS - 1, i + k))] * (4 - Math.abs(k));
      }
      this.shaped[i] = sum / 16;
    }

    for (let layer = 0; layer < SURFACES; layer++) {
      const depth = layer / (SURFACES - 1);
      const band = (frame.bands[Math.floor((0.12 + depth * 0.75) * (frame.bands.length - 1))] ?? 0) * activity;
      const phase = layer * 1.12;
      for (let i = 0; i < POINTS; i++) {
        const t = i / (POINTS - 1);
        const env = this.window[i];
        const energy = this.shaped[i];
        const carrier = Math.sin(t * Math.PI * (2.4 + depth * 1.7) - this.time * 0.19 + phase);
        const fold = Math.sin(t * Math.PI * 6 + phase + this.time * 0.13 + band);
        const centre = ((depth - 0.5) * 0.55 * (0.16 + energy)
          + carrier * (0.022 + energy * 0.39)
          + this.signal[i] * 0.17 + fold * band * 0.07) * env * amp;
        const width = (0.018 + energy * (0.13 + depth * 0.06)) * env * amp;
        this.top[i] = this.cy + centre - width * (0.8 + fold * 0.22);
        this.bottom[i] = this.cy + centre + width * (0.8 - fold * 0.22);
      }

      g.moveTo(this.xs[0], this.top[0]);
      this.edge(this.top, false);
      g.lineTo(this.xs[POINTS - 1], this.bottom[POINTS - 1]);
      this.edge(this.bottom, true);
      g.closePath().fill(this.gradient
        ? { fill: this.gradient, alpha: 0.07 + depth * 0.065 }
        : { color: this.colours[layer], alpha: 0.12 });

      // Only one edge catches bright light; the opposite edge is a fine shadow
      // contour. Overlapping, asymmetrical sheets read as silk, not a filled blob.
      g.moveTo(this.xs[0], this.top[0]);
      this.edge(this.top, false);
      g.stroke({ color: this.colours[layer], alpha: 0.36 + depth * 0.16,
        width: 0.85 + depth * 0.35, cap: 'round', join: 'round' });
      g.moveTo(this.xs[0], this.bottom[0]);
      this.edge(this.bottom, false);
      g.stroke({ color: 0xd9f3ee, alpha: 0.13 + depth * 0.07, width: 0.65 });

      if (layer === SURFACES - 1) {
        for (let i = 20; i < POINTS - 15; i += 24) g.circle(this.xs[i], this.top[i], 1);
        g.fill({ color: 0xf5f4df, alpha: 0.6 });
      }
    }
  }

  /** Midpoint quadratics in either direction, without allocating point objects. */
  private edge(values: Float32Array, reverse: boolean) {
    const step = reverse ? -1 : 1;
    const end = reverse ? 0 : POINTS - 1;
    for (let i = reverse ? POINTS - 2 : 1; i !== end; i += step) {
      const next = i + step;
      this.gfx.quadraticCurveTo(this.xs[i], values[i],
        (this.xs[i] + this.xs[next]) * 0.5, (values[i] + values[next]) * 0.5);
    }
    this.gfx.lineTo(this.xs[end], values[end]);
  }

  dispose() {
    // Release the context's references before destroying its gradient texture.
    super.dispose();
    this.gradient?.destroy();
    this.gradient = null;
  }
}
