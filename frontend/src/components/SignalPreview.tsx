import { For, Switch, Match, createUniqueId } from 'solid-js';
import { rampByName } from '../viz/palette';

/** Compositional studies: SVG only, with no audio clients or WebGL contexts. */
export function SignalPreview(props: { slug: string; ramp?: string }) {
  const id = `signal-${createUniqueId()}`;
  const ink = () => `url(#${id})`;
  const scopeColour = (end: number) => rampByName(props.ramp).css(end);
  const wave = (offset: number, amplitude: number, phase = 0) => Array.from({ length: 121 }, (_, i) => {
    const x = 22 + i * 3.63;
    const envelope = Math.sin(i / 120 * Math.PI);
    const y = offset + Math.sin(i * .085 + phase) * amplitude * envelope + Math.cos(i * .17 + phase) * amplitude * .25 * envelope;
    return `${i ? 'L' : 'M'}${x.toFixed(1)} ${y.toFixed(1)}`;
  }).join(' ');
  return (
    <svg class="signal-preview" viewBox="0 0 480 240" fill="none" aria-hidden="true">
      <defs>
        <linearGradient id={id} x1="0" y1="0" x2="1" y2=".4">
          <For each={rampByName(props.ramp).cssStops(10)}>{stop => <stop offset={stop.offset} stop-color={stop.color} />}</For>
        </linearGradient>
        <radialGradient id={`${id}-light`}><stop stop-color="#334e78" stop-opacity=".35" /><stop offset="1" stop-color="#080c18" stop-opacity="0" /></radialGradient>
        <linearGradient id={`${id}-floor`} x2="0" y2="1"><stop stop-color="#a5f3df" stop-opacity=".16" /><stop offset="1" stop-color="#8ed8f8" stop-opacity="0" /></linearGradient>
      </defs>
      <ellipse cx="240" cy="124" rx="226" ry="116" fill={`url(#${id}-light)`} />
      <g stroke="#7692b6" stroke-width=".5" opacity=".2"><path d="M22 200H458M22 120H458M22 40H458" /><path d="M80 24V216M240 24V216M400 24V216" stroke-dasharray="2 7" /></g>
      <Switch>
        <Match when={props.slug === 'bars'}>
          <ellipse cx="240" cy="186" rx="211" ry="29" fill={`url(#${id}-floor)`} />
          <For each={Array.from({ length: 32 }, (_, i) => i)}>{i => {
            const h = 16 + Math.abs(Math.sin(i * .39) * Math.cos(i * .12)) * 100 + 22 * Math.sin(i / 31 * Math.PI);
            const x = 37 + i * 12.8;
            return <g><path d={`M${x} 178v-${h}l8 -4v${h}Z`} fill={ink()} fill-opacity=".75" /><path d={`M${x + 8} ${174 - h}l4 5v${h}l-4 -1Z`} fill={ink()} fill-opacity=".27" /><path d={`M${x} ${168 - h}h8`} stroke={ink()} stroke-width="2" /><path d={`M${x + 3} 187v${h * .18}`} stroke={ink()} stroke-width="6" opacity=".12" /></g>;
          }}</For>
        </Match>
        <Match when={props.slug === 'mirror'}>
          <For each={Array.from({ length: 36 }, (_, i) => i)}>{i => <path d={wave(120, 18 + i * 1.65, i * .16)} stroke={ink()} stroke-width=".85" opacity={.25 + Math.sin(i / 36 * Math.PI) * .6} />}</For>
          <ellipse cx="240" cy="120" rx="185" ry="72" stroke="#a5f3df" stroke-opacity=".18" transform="rotate(-12 240 120)" />
        </Match>
        <Match when={props.slug === 'dots'}>
          <path d="M70 162L136 84 225 112 295 54 359 130 408 77M136 84L184 178 225 112 327 181 359 130" stroke={ink()} opacity=".3" />
          <For each={Array.from({ length: 240 }, (_, i) => i)}>{i => {
            const angle = i * 2.39996;
            const radius = Math.sqrt(i / 240);
            const x = 240 + Math.cos(angle) * radius * 211;
            const y = 120 + Math.sin(angle) * radius * 89;
            return <circle cx={x} cy={y} r={i % 19 === 0 ? 2.8 : i % 5 === 0 ? 1.7 : .9} fill={ink()} opacity={.22 + (i % 7) * .11} />;
          }}</For>
          <For each={[[136, 84], [225, 112], [295, 54], [359, 130]]}>{p => <circle cx={p[0]} cy={p[1]} r="8" stroke="#a5f3df" opacity=".4" />}</For>
        </Match>
        <Match when={props.slug === 'ribbon'}>
          <For each={Array.from({ length: 32 }, (_, i) => i)}>{i => <path d={`M24 ${145 + i * .4}C105 ${155 - i * 2.8} 100 ${28 + i * 2.6} 205 ${89 + i * 1.5}S330 ${205 - i * 5} 456 ${72 + i * 2.6}`} stroke={ink()} stroke-width={i % 8 === 0 ? 2 : .85} opacity={.22 + Math.sin(i / 32 * Math.PI) * .55} />}</For>
          <path d="M24 145C105 155 100 28 205 89S330 205 456 72L456 155C330 50 310 184 205 136S105 66 24 158Z" fill={ink()} fill-opacity=".07" />
        </Match>
        <Match when={props.slug === 'line'}>
          <For each={Array.from({ length: 19 }, (_, i) => i)}>{i => {
            const path = Array.from({ length: 100 }, (_, j) => {
              const x = 28 + j * 4.3;
              const mountain = Math.exp(-Math.pow((j - 38 - i * 1.4) / 18, 2)) * (30 + i * 2.6);
              return `${j ? 'L' : 'M'}${x} ${(50 + i * 7.2 - mountain + Math.sin(j * .13 + i * .23) * 8).toFixed(1)}`;
            }).join(' ');
            return <path d={path} stroke={ink()} stroke-width={i % 5 === 0 ? 1.8 : .8} opacity={.35 + i / 35} />;
          }}</For>
        </Match>
        <Match when={props.slug === 'lens'}>
          <For each={Array.from({ length: 13 }, (_, i) => i)}>{i => {
            const x = 48 + i * 31;
            const y = 116 + Math.sin(i * .65) * 21;
            const h = 26 + Math.sin(i / 12 * Math.PI) * 39;
            return <g stroke={ink()} stroke-width=".9"><path d={`M${x} ${y - h}l25 ${h - 9}-25 ${h + 9}-25 -${h - 9}Z`} fill={ink()} fill-opacity=".12" /><path d={`M${x} ${y - h}v${h * 2}M${x - 25} ${y + 9}l50 -18M${x} ${y}l25 -9`} opacity=".65" /><path d={`M${x - 25} ${y + 9}L${x} ${y}l0 ${h}`} fill={ink()} fill-opacity=".13" /></g>;
          }}</For>
        </Match>
        <Match when={props.slug === 'spectrum'}>
          <rect x="26" y="35" width="428" height="165" rx="12" stroke="#6a8ba4" stroke-opacity=".35" />
          <For each={[60, 90, 120, 150, 180]}>{y => <path d={`M38 ${y}H442`} stroke="#a5f3df" stroke-opacity=".1" />}</For>
          <path d={wave(117, 38, 2)} stroke={scopeColour(0)} stroke-width="7" opacity=".08" /><path d={wave(117, 38, 2)} stroke={scopeColour(0)} stroke-width="1.7" />
          <path d={wave(139, 21, 2.8)} stroke={scopeColour(1)} stroke-width="1" opacity=".6" />
          <rect x="338" y="48" width="102" height="76" rx="6" fill="#080c18" stroke="#354d60" />
          <path d={Array.from({ length: 180 }, (_, i) => `${i ? 'L' : 'M'}${389 + Math.sin(i / 179 * Math.PI * 6) * 35} ${86 + Math.sin(i / 179 * Math.PI * 4 + .6) * 27}`).join(' ')} stroke={scopeColour(0)} stroke-width="1" />
          <path d="M43 185h25m8 0h9m14 0h9" stroke={scopeColour(1)} stroke-width="2" />
        </Match>
        <Match when={true}>
          <For each={Array.from({ length: 45 }, (_, i) => i)}>{i => <path d={`M${(i * 73) % 480} ${(i * 31) % 220}l-5 13`} stroke="#8ed8f8" stroke-opacity={.08 + (i % 3) * .05} />}</For>
          <ellipse cx="240" cy="120" rx="159" ry="62" transform="rotate(-22 240 120)" stroke="#a5f3df" stroke-opacity=".4" />
          <circle cx="240" cy="120" r="77" fill="#0c1323" stroke="#4f6b85" />
          <For each={[32, 39, 46, 53, 60, 67, 72]}>{r => <circle cx="240" cy="120" r={r} stroke="#8ed8f8" stroke-opacity=".14" />}</For>
          <circle cx="240" cy="120" r="24" fill={ink()} fill-opacity=".5" /><circle cx="240" cy="120" r="5" fill="#080c18" />
          <circle cx="369" cy="68" r="5" fill="#ff9a88" /><path d="M325 45l-12 78-38 27" stroke="#c5d3e4" stroke-width="3" stroke-linecap="round" />
        </Match>
      </Switch>
    </svg>
  );
}
