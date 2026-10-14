/* Spectral towers — calibrated, segmented meters with suspended peak flags,
 * a shallow reflection and a separate three-band telemetry strip. */
import type { AudioFrame } from './types';
import { WaveScene, type WaveSceneOptions } from './WaveScene';
import { rampByName, type Ramp } from './palette';
import * as PIXI from 'pixi.js';
import type { AudioEngine } from '../audio/AudioEngine';

const BAND_COUNT = 56;
const SEGMENTS = 24;
const IVORY = 0xe7f1ef;

export class BarSpectrumVisualizer extends WaveScene {
  private ramp: Ramp;
  private colours = new Uint32Array(BAND_COUNT);
  private highlights = new Uint32Array(BAND_COUNT);
  private x = new Float32Array(BAND_COUNT);
  private instrument = new PIXI.Graphics();
  private barWidth = 1;
  private baseline = 0;
  private meterHeight = 1;
  private segmentStep = 1;
  private segmentHeight = 1;

  constructor(app: PIXI.Application, audio: AudioEngine, options: WaveSceneOptions = {}) {
    super(app, audio, options, {
      bands: BAND_COUNT,
      wavePoints: 128,
      bloom: 0.48,
      bandHeight: 0.30,
    });
    this.ramp = rampByName(options.ramp ?? 'AURORA');
    for (let i = 0; i < BAND_COUNT; i++) {
      const [r, g, b] = this.ramp.sample(i / (BAND_COUNT - 1));
      this.colours[i] = this.ramp.hex(i / (BAND_COUNT - 1));
      this.highlights[i] = (Math.round(r * 0.55 + 231 * 0.45) << 16)
        | (Math.round(g * 0.55 + 241 * 0.45) << 8)
        | Math.round(b * 0.55 + 239 * 0.45);
    }
    // Calibrations stay tessellated; only the signal is rebuilt each frame.
    this.art.addChildAt(this.instrument, 0);
  }

  protected onResize() {
    const g = this.instrument.clear();
    const usable = Math.max(1, this.W - this.marginX * 2);
    const slot = usable / BAND_COUNT;
    this.barWidth = Math.max(0.6, slot * 0.62);
    this.baseline = this.cy + this.halfBand * 0.52;
    this.meterHeight = this.halfBand * 1.43;
    this.segmentStep = this.meterHeight / SEGMENTS;
    this.segmentHeight = this.segmentStep * 0.65;

    for (let i = 0; i < BAND_COUNT; i++) {
      // Eight-channel banks have a subtly wider break, not a wall of pillars.
      this.x[i] = this.marginX + (i + 0.5) * slot - this.barWidth / 2;
      if (i % 8 === 0) {
        const x = this.x[i] - slot * 0.28;
        g.moveTo(x, this.baseline + 7).lineTo(x, this.baseline + 12);
      }
    }
    g.stroke({ color: IVORY, alpha: 0.32, width: 1 });

    for (let row = 0; row <= SEGMENTS; row += 6) {
      const y = this.baseline - row * this.segmentStep;
      // Short ruler marks at the sides; deliberately no enclosing panel.
      g.moveTo(this.marginX - 10, y).lineTo(this.marginX - 4, y)
        .moveTo(this.W - this.marginX + 4, y).lineTo(this.W - this.marginX + 10, y);
      if (row > 0) {
        for (let bank = 0; bank < 7; bank++) {
          const x = this.marginX + bank * slot * 8;
          g.moveTo(x + slot * 0.25, y).lineTo(x + slot * 7.65, y);
        }
      }
    }
    g.stroke({ color: IVORY, alpha: 0.07, width: 0.7 });
    g.moveTo(this.marginX, this.baseline + 2)
      .lineTo(this.W - this.marginX, this.baseline + 2)
      .stroke({ color: IVORY, alpha: 0.23, width: 0.8 });

    // Empty meter cells are one batched path, giving silence an intentional form.
    for (let i = 0; i < BAND_COUNT; i++) {
      for (let row = 0; row < SEGMENTS; row++) {
        g.rect(this.x[i], this.baseline - (row + 1) * this.segmentStep,
          this.barWidth, this.segmentHeight);
      }
    }
    g.fill({ color: IVORY, alpha: 0.025 });

    const telemetryY = this.cy + this.halfBand * 1.02;
    const subWidth = usable * 0.21;
    for (let channel = 0; channel < 3; channel++) {
      const x = this.marginX + channel * usable * 0.395;
      for (let j = 0; j < 28; j++) {
        g.rect(x + j * subWidth / 28, telemetryY, subWidth / 28 * 0.58, 3);
      }
      g.moveTo(x, telemetryY - 6).lineTo(x + subWidth, telemetryY - 6);
    }
    g.fill({ color: IVORY, alpha: 0.09 });
    g.stroke({ color: IVORY, alpha: 0.13, width: 0.7 });
  }

  protected draw(frame: AudioFrame, _dt: number) {
    const g = this.gfx;
    const step = this.segmentStep;
    const baseline = this.baseline;

    for (let i = 0; i < BAND_COUNT; i++) {
      const x = this.x[i];
      const level = Math.min(1, Math.max(0.015, frame.bands[i]));
      const lit = level * SEGMENTS;
      const whole = Math.floor(lit);
      const colour = this.colours[i];

      // All solid cells in a tower share one fill operation.
      for (let row = 0; row < whole; row++) {
        g.rect(x, baseline - (row + 1) * step, this.barWidth, this.segmentHeight);
      }
      if (whole > 0) g.fill({ color: colour, alpha: 0.73 });
      const fraction = lit - whole;
      if (fraction > 0.01 && whole < SEGMENTS) {
        g.rect(x, baseline - (whole + 1) * step, this.barWidth, this.segmentHeight)
          .fill({ color: this.highlights[i], alpha: fraction * 0.85 });
      }

      // A narrow luminous spine gives each stack depth without a bloom slab.
      if (whole > 2) {
        g.rect(x, baseline - whole * step, Math.max(0.5, this.barWidth * 0.12), whole * step)
          .fill({ color: this.highlights[i], alpha: 0.13 });
      }
      const peak = Math.min(1, Math.max(level, frame.bandPeaks[i]));
      const peakY = baseline - peak * this.meterHeight - 5;
      g.rect(x - 0.5, peakY, this.barWidth + 1, 1.6)
        .fill({ color: this.highlights[i], alpha: 0.88 });
      if (i % 8 === 0) {
        g.rect(x + this.barWidth + 1.5, peakY, 1.3, 4)
          .fill({ color: IVORY, alpha: 0.55 });
      }

      // Three stepped echoes, fading quickly into the ink below the rail.
      const reflection = level * this.meterHeight * 0.18;
      if (reflection > 1) {
        g.rect(x, baseline + 7, this.barWidth, reflection * 0.45)
          .fill({ color: colour, alpha: 0.105 });
        g.rect(x, baseline + 9 + reflection * 0.45, this.barWidth, reflection * 0.28)
          .fill({ color: colour, alpha: 0.045 });
        g.rect(x, baseline + 11 + reflection * 0.73, this.barWidth, reflection * 0.2)
          .fill({ color: colour, alpha: 0.018 });
      }
    }

    // Low / mid / air strips remain separate from the main frequency towers.
    const usable = Math.max(1, this.W - this.marginX * 2);
    const subWidth = usable * 0.21;
    const telemetryY = this.cy + this.halfBand * 1.02;
    for (let channel = 0; channel < 3; channel++) {
      const energy = channel === 0 ? frame.bass : channel === 1 ? frame.mid : frame.treble;
      const count = Math.ceil(Math.min(1, Math.max(0, energy)) * 28);
      const x = this.marginX + channel * usable * 0.395;
      for (let j = 0; j < count; j++) {
        g.rect(x + j * subWidth / 28, telemetryY, subWidth / 28 * 0.58, 3);
      }
      if (count > 0) g.fill({ color: this.colours[channel * 27], alpha: 0.72 });
    }
    // One beat beacon, not a full-stage flash.
    g.circle(this.cx, telemetryY + 14, 1.5 + this.beatPulse * 1.2)
      .fill({ color: IVORY, alpha: 0.25 + Math.min(0.65, this.beatPulse * 0.5) });
  }
}
