import { onMount, onCleanup } from 'solid-js';
import * as PIXI from 'pixi.js';
import { AudioEngine } from '../audio/AudioEngine';
import { shouldAutoStartAudio } from '../audio/autoplay';
import { LofiRainVisualizer } from '../viz/LofiRainVisualizer';
import type { Visualizer } from '../viz/types';

export interface VisualizerOptions {
  showBackground: boolean;
  transparent: boolean;
  ramp?: string;
  fitToViewport?: boolean;
}

export type VisualizerFactory = (
  app: PIXI.Application,
  audio: AudioEngine,
  options: VisualizerOptions,
) => Visualizer;

const defaultVisualizer: VisualizerFactory = (app, audio, options) =>
  new LofiRainVisualizer(app, audio, options);

interface Props {
  onReady: (audio: AudioEngine) => void;
  transparent?: boolean;
  showBackground?: boolean;
  ramp?: string;
  /** Factory for the visualizer to mount; defaults to LofiRainVisualizer. */
  createVisualizer?: VisualizerFactory;
}

export function Stage(props: Props) {
  let stageRef!: HTMLDivElement;

  onMount(() => {
    const app = new PIXI.Application();
    let initialized = false;
    let disposed = false;
    let audio: AudioEngine | null = null;
    let viz: Visualizer | null = null;
    let ro: ResizeObserver | null = null;
    let tickerUpdate: (() => void) | null = null;
    const motionPreference = window.matchMedia('(prefers-reduced-motion: reduce)');
    let needsVisualFrame = true;
    const onMotionChange = () => {
      needsVisualFrame = true;
      if (initialized) app.ticker.maxFPS = motionPreference.matches ? 30 : 60;
    };
    motionPreference.addEventListener('change', onMotionChange);

    onCleanup(() => {
      disposed = true;
      motionPreference.removeEventListener('change', onMotionChange);
      ro?.disconnect();
      if (tickerUpdate && initialized) app.ticker.remove(tickerUpdate);
      viz?.dispose();
      audio?.dispose();
      if (initialized) {
        app.destroy(
          { removeView: true, releaseGlobalResources: true },
          { children: true, texture: true, textureSource: true },
        );
      }
    });

    void (async () => {
      // Opaque players reserve a real scene rectangle. Clean OBS sources have
      // no performance element and retain the full-viewport canvas.
      const sceneArea = !props.transparent
        ? document.querySelector<HTMLElement>('.performance-space') : null;
      const fitStage = () => {
        if (!sceneArea) return;
        const rect = sceneArea.getBoundingClientRect();
        const label = sceneArea.querySelector<HTMLElement>('.performance-coordinate');
        const reserved = (label?.offsetHeight ?? 0)
          + parseFloat(getComputedStyle(sceneArea).paddingBottom) + 6;
        Object.assign(stageRef.style, {
          inset: `${rect.top}px auto auto ${rect.left}px`,
          width: `${rect.width}px`, height: `${Math.max(1, rect.height - reserved)}px`,
        });
      };
      fitStage();
      await app.init({
        resizeTo: stageRef,
        antialias: true,
        backgroundAlpha: props.transparent ? 0 : 1,
        background: props.transparent ? 0x000000 : 0x080c18,
        resolution: Math.min(window.devicePixelRatio || 1, 2),
        autoDensity: true,
        powerPreference: 'high-performance',
      });
      initialized = true;

      if (disposed) {
        app.destroy({ removeView: true }, { children: true });
        return;
      }

      app.ticker.maxFPS = motionPreference.matches ? 30 : 60;
      app.ticker.minFPS = 30;
      stageRef.appendChild(app.canvas);

      audio = new AudioEngine({ allowAutoplay: shouldAutoStartAudio(false) });
      const makeViz = props.createVisualizer ?? defaultVisualizer;
      viz = makeViz(app, audio, {
        showBackground: props.showBackground ?? true,
        transparent: props.transparent ?? false,
        ramp: props.ramp,
        fitToViewport: sceneArea !== null,
      });

      props.onReady(audio);

      let last = performance.now();
      tickerUpdate = () => {
        if (!audio || !viz) return;
        const now = performance.now();
        const dt = Math.min((now - last) / 1000, 0.05);
        last = now;
        audio.update(dt, 1);
        // Keep playback running, but hold a still composition when motion is reduced.
        if (!motionPreference.matches || needsVisualFrame) {
          // One deterministic analysis step builds a complete still image;
          // dt=0 would leave envelope-following scenes at their zero buffers.
          viz.update(motionPreference.matches ? 1 / 60 : dt);
          needsVisualFrame = false;
        }
      };
      app.ticker.add(tickerUpdate);

      ro = new ResizeObserver(() => {
        fitStage();
        app.resize();
        viz?.resize();
        needsVisualFrame = true;
      });
      ro.observe(stageRef);
      if (sceneArea) ro.observe(sceneArea);
    })().catch(err => console.error('Stage init failed', err));
  });

  return <div id="stage" ref={stageRef!} aria-hidden="true" />;
}
