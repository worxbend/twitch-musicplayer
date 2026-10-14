import { For, Show } from 'solid-js';
import { rampByName } from '../viz/palette';

/** Lightweight diagrams, not extra audio clients or WebGL contexts. */
export function SignalPreview(props: { slug: string; ramp?: string }) {
  const gradientId = `signal-${props.slug}`;
  const heights = Array.from({ length: 43 }, (_, i) =>
    8 + Math.abs(Math.sin(i * .72) * Math.cos(i * .23)) * 60 + 18 * Math.sin(i / 42 * Math.PI));
  const points = Array.from({ length: 100 }, (_, i) => {
    const y = 80 + Math.sin(i * .45) * Math.sin(i / 99 * Math.PI) * 30 + Math.sin(i * .14) * 8;
    return `${12 + i * 3.17},${y}`;
  }).join(' ');
  return (
    <svg class="signal-preview" viewBox="0 0 340 160" fill="none" aria-hidden="true">
      <defs>
        <linearGradient id={gradientId} x1="0" y1="0" x2="1" y2="0">
          <For each={rampByName(props.slug === 'spectrum' ? 'NEON' : props.ramp).cssStops(8)}>
            {stop => <stop offset={stop.offset} stop-color={stop.color} />}
          </For>
        </linearGradient>
      </defs>
      <path d="M12 80H328 M170 12V148" stroke="currentColor" opacity=".18" stroke-dasharray="2 5" />
      <g stroke={`url(#${gradientId})`} fill={`url(#${gradientId})`}>
        <Show when={['bars', 'mirror', 'dots'].includes(props.slug)}>
          <For each={heights}>{(h, i) => {
            const x = 18 + i() * 7.2;
            return props.slug === 'dots'
              ? <For each={Array.from({ length: Math.ceil(h / 9) }, (_, n) => n)}>{n => <><circle cx={x} cy={80 - n * 7} r="1.8" stroke="none" /><circle cx={x} cy={80 + n * 7} r="1.8" stroke="none" /></>}</For>
              : <rect x={x} y={props.slug === 'bars' ? 119 - h : 80 - h / 2} width={props.slug === 'bars' ? 4 : 2.5} height={h} stroke="none" />;
          }}</For>
        </Show>
        <Show when={['spectrum', 'line'].includes(props.slug)}>
          <polyline points={points} fill="none" stroke-width={props.slug === 'spectrum' ? 2.5 : 1.5} />
        </Show>
        <Show when={props.slug === 'ribbon'}>
          <path d="M14 80C45 79 42 38 84 45S115 95 150 51S191 33 216 62S285 37 326 80C285 123 249 92 216 98S185 127 150 109S116 108 84 115S40 82 14 80Z" stroke="none" opacity=".8" />
        </Show>
        <Show when={props.slug === 'lens'}>
          <For each={[25, 50, 75, 100, 125, 150, 175, 200, 225, 250, 275]}>{(x, i) => {
            const h = 15 + Math.sin(i() / 10 * Math.PI) * 38;
            return <path d={`M${x} 80l25 -${h} 25 ${h}-25 ${h}Z`} fill-opacity=".12" stroke-width="1.5" />;
          }}</For>
        </Show>
      </g>
    </svg>
  );
}
