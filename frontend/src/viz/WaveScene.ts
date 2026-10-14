/* WaveScene.ts — shared scaffolding for the waveform and spectrum scenes.
 *
 * Every scene in this family wants the same things: a dark backdrop (or a
 * transparent one for OBS), a bloom-wrapped drawing layer, a centred band to
 * draw into, and the display-ready analysis frame. Putting that here keeps each
 * visualizer to the part that actually differs — how it turns the frame into
 * shapes.
 *
 * Subclasses implement draw(); the base owns the Pixi lifecycle.
 */
import * as PIXI from 'pixi.js';
import { AdvancedBloomFilter } from 'pixi-filters';
import { AudioAnalysisEngine } from '../audio/AudioAnalysisEngine';
import type { AnalysisOptions } from '../audio/AudioAnalysisEngine';
import type { AudioEngine } from '../audio/AudioEngine';
import type { AudioFrame, Visualizer } from './types';

export interface WaveSceneOptions {
  /** Paint the dark backdrop. False for transparent OBS overlays. */
  showBackground?: boolean;
  /** Transparent page: skips the backdrop entirely. */
  transparent?: boolean;
  /** Colour ramp name, from palette.ts. */
  ramp?: string;
  /** Canvas already occupies the player's safe performance rectangle. */
  fitToViewport?: boolean;
}

interface SceneConfig extends AnalysisOptions {
  /** Bloom strength. 0 disables the filter for this scene. */
  bloom?: number;
  /** Fraction of the display height the drawing band occupies. */
  bandHeight?: number;
}

export abstract class WaveScene implements Visualizer {
  protected app: PIXI.Application;
  protected audio: AudioEngine;
  protected analysis: AudioAnalysisEngine;
  protected showBackground: boolean;

  protected root = new PIXI.Container();
  protected bg = new PIXI.Graphics();
  /** Bloom is applied to this container, so add reactive art here. */
  protected art = new PIXI.Container();
  protected gfx = new PIXI.Graphics();

  /** Display metrics, refreshed on resize. */
  protected W = 1920;
  protected H = 1080;
  protected cx = 960;
  protected cy = 540;
  /** Half-height of the band the waveform is drawn into. */
  protected halfBand = 260;
  /** Horizontal margin, so shapes do not touch the display edges. */
  protected marginX = 100;

  protected time = 0;
  /** Beat envelope, rises on a transient and decays smoothly. */
  protected beatPulse = 0;

  private readonly bandHeightFraction: number;
  private readonly fitToViewport: boolean;
  private disposed = false;
  private bloomFilter: AdvancedBloomFilter | null = null;
  /** onResize() has run at least once with subclass state in place. */
  private primed = false;

  constructor(
    app: PIXI.Application,
    audio: AudioEngine,
    options: WaveSceneOptions,
    config: SceneConfig = {},
  ) {
    this.app = app;
    this.audio = audio;
    this.analysis = new AudioAnalysisEngine(config);
    this.showBackground = (options.showBackground ?? true) && !options.transparent;
    this.bandHeightFraction = config.bandHeight ?? 0.30;
    this.fitToViewport = options.fitToViewport === true;

    this.art.addChild(this.gfx);

    const bloom = config.bloom ?? 0.55;
    if (bloom > 0) {
      try {
        this.bloomFilter = new AdvancedBloomFilter({
          threshold: 0.35,
          bloomScale: bloom,
          brightness: 1,
          blur: 5,
          quality: 3,
        });
        this.art.filters = [this.bloomFilter];
        // Keep translucent surfaces readable instead of bleaching every
        // intersection with container-wide additive blending.
      } catch {
        // Filters unavailable (old GPU, software renderer). The scene still
        // draws correctly, just without the halo.
      }
    }

    this.root.addChild(this.bg, this.art);
    app.stage.addChild(this.root);

    // Metrics and backdrop only. onResize() is deliberately NOT called here:
    // a subclass's field initializers and constructor body have not run yet at
    // this point, so an override touching subclass state would see undefined.
    // The first update() primes it instead, by which time the subclass is whole.
    this.measure();
    this.drawBackground();
  }

  resize() {
    this.measure();
    this.drawBackground();
    // Before the first update the subclass is not fully constructed, so a
    // resize arriving that early is answered by the priming call in update().
    if (this.primed) this.onResize();
  }

  private measure() {
    const r = this.app.renderer;
    this.W = r.width / r.resolution;
    this.H = r.height / r.resolution;
    this.cx = this.W / 2;
    this.cy = this.H * (this.showBackground ? 0.48 : 0.5);
    // Fitted players use the available rectangle, including room for fixed-size
    // calibration marks. Standalone sources retain their full-screen framing.
    this.halfBand = this.fitToViewport
      ? Math.min(this.H * 0.30, Math.max(2, (this.H * 0.48 - 18) / 1.4))
      : this.H * Math.min(this.bandHeightFraction, this.showBackground ? 0.22 : 0.30);
    this.marginX = Math.max(24, this.W * 0.085);

    // Pin the filter to the whole display. Left to itself, a filter is applied
    // over the art's bounding box only, so a bloom halo is clipped wherever the
    // art reaches the edge of that box — a bar at full height loses the glow
    // above it. Covering the display costs one full-screen pass and keeps the
    // halo intact wherever the art happens to reach.
    this.art.filterArea = new PIXI.Rectangle(0, 0, this.W, this.H);
  }

  update(dt: number) {
    if (this.disposed) return;

    if (!this.primed) {
      this.primed = true;
      this.onResize();
    }

    this.time += dt;

    const frame = this.analysis.update(this.audio, dt);

    if (frame.beat) {
      this.beatPulse = Math.min(1.5, this.beatPulse + frame.beatStrength * 0.9);
    }
    // Frame-rate independent decay, so the pulse feels the same at 30 and 60 fps.
    this.beatPulse *= Math.exp(-7 * dt);

    this.gfx.clear();
    this.draw(frame, dt);
  }

  dispose() {
    if (this.disposed) return;
    this.disposed = true;
    this.analysis.dispose();
    this.art.filters = [];
    this.bloomFilter?.destroy();
    this.bloomFilter = null;
    this.root.destroy({ children: true });
  }

  /** Paints the backdrop. Transparent scenes leave it empty. */
  protected drawBackground() {
    this.bg.clear();
    if (!this.showBackground) return;
    this.bg.rect(0, 0, this.W, this.H).fill({ color: 0x080c18 });
    // Static instrument graticule: drawn only on resize, never on overlays.
    const left = this.marginX;
    const right = this.W - left;
    const top = this.cy - this.halfBand;
    const bottom = this.cy + this.halfBand;
    for (let i = 0; i <= 12; i++) {
      const x = left + (right - left) * i / 12;
      this.bg.moveTo(x, top).lineTo(x, bottom);
    }
    for (let i = 0; i <= 6; i++) {
      const y = top + (bottom - top) * i / 6;
      this.bg.moveTo(left, y).lineTo(right, y);
    }
    this.bg.stroke({ color: 0x63768f, alpha: 0.07, width: 1 });
    // Calibration marks establish scale without decorating the browser source.
    for (let i = 0; i <= 24; i++) {
      const x = left + (right - left) * i / 24;
      this.bg.moveTo(x, bottom + 18).lineTo(x, bottom + (i % 4 === 0 ? 25 : 21));
    }
    this.bg.stroke({ color: 0x9ab5c8, alpha: 0.22, width: 1 });
    this.bg.moveTo(left, this.cy).lineTo(right, this.cy)
      .stroke({ color: 0x8bdbd5, alpha: 0.12, width: 1 });
  }

  /** Hook for subclasses that cache geometry across frames. */
  protected onResize() { /* optional */ }

  /** Turns one analysis frame into shapes on this.gfx. */
  protected abstract draw(frame: AudioFrame, dt: number): void;
}
