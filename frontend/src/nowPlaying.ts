/**
 * Pure reducer for the /now-playing text overlay.
 *
 * Kept free of config, Vite env and Pixi imports so `node --test` can load it.
 * It reads the two server frames that carry the current song: `state`, sent
 * once on connect, and `now_playing`, broadcast whenever a song starts. Frames
 * are `{type, payload}` envelopes (backend/internal/websocket/protocol.go).
 */

export type NowPlayingSong = { title: string; artist: string };
export type NowPlayingAlign = 'left' | 'center' | 'right';
/** `hold` is seconds the title stays visible after each change; 0 keeps it visible. */
export type NowPlayingOptions = { align: NowPlayingAlign; size: number; hold: number };

type JsonObject = Record<string, unknown>;

function isObject(value: unknown): value is JsonObject {
  return typeof value === 'object' && value !== null;
}

function asText(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}

function stripExtension(filename: string): string {
  const dot = filename.lastIndexOf('.');
  return dot > 0 ? filename.slice(0, dot) : filename;
}

/** A song needs a title or a filename to be shown; otherwise it is unusable. */
function toSong(raw: unknown): NowPlayingSong | null {
  if (!isObject(raw)) return null;
  const title = asText(raw.title) || stripExtension(asText(raw.filename));
  return title ? { title, artist: asText(raw.artist) } : null;
}

function parse(raw: string): JsonObject | null {
  try {
    const msg: unknown = JSON.parse(raw);
    return isObject(msg) ? msg : null;
  } catch {
    return null;
  }
}

/** Applies one raw socket frame to the current song; anything unusable keeps `prev`. */
export function reduceNowPlaying(prev: NowPlayingSong | null, raw: string): NowPlayingSong | null {
  const msg = parse(raw);
  if (!msg || !isObject(msg.payload)) return prev;

  if (msg.type === 'state') return toSong(msg.payload.current_song);
  if (msg.type === 'now_playing') return toSong(msg.payload.song) ?? prev;
  return prev;
}

export const nowPlayingAlignments: readonly NowPlayingAlign[] = ['left', 'center', 'right'];
const defaultSizePx = 32;
const minSizePx = 12;
const maxSizePx = 120;
const maxHoldSecs = 600;

function parseWholeNumber(raw: string | null): number {
  return raw !== null && /^\d+$/.test(raw) ? Number(raw) : NaN;
}

/**
 * Reads `?align=`, `?size=` (px) and `?hold=` (seconds, 1..600). Anything invalid
 * falls back to left / 32 / 0, where hold 0 means always visible.
 */
export function parseNowPlayingOptions(search: string): NowPlayingOptions {
  const params = new URLSearchParams(search);
  const rawAlign = params.get('align');
  const align = nowPlayingAlignments.find(value => value === rawAlign) ?? 'left';
  const px = parseWholeNumber(params.get('size'));
  const size = px >= minSizePx && px <= maxSizePx ? px : defaultSizePx;
  const secs = parseWholeNumber(params.get('hold'));
  const hold = secs >= 1 && secs <= maxHoldSecs ? secs : 0;
  return { align, size, hold };
}

/** Rounds to a whole number inside min..max; a missing or non-numeric value gives `fallback`. */
function clampWhole(value: number | undefined, min: number, max: number, fallback: number): number {
  if (value === undefined || !Number.isFinite(value)) return fallback;
  return Math.min(max, Math.max(min, Math.round(value)));
}

/**
 * Builds the /now-playing path that parseNowPlayingOptions reads back. Values are
 * clamped to the accepted ranges and defaults are left out of the query.
 */
export function nowPlayingPath(opts: Partial<NowPlayingOptions>): string {
  const align = nowPlayingAlignments.find(value => value === opts.align) ?? 'left';
  const size = clampWhole(opts.size, minSizePx, maxSizePx, defaultSizePx);
  const hold = clampWhole(opts.hold, 0, maxHoldSecs, 0);
  const params = new URLSearchParams();
  if (align !== 'left') params.set('align', align);
  if (size !== defaultSizePx) params.set('size', String(size));
  if (hold !== 0) params.set('hold', String(hold));
  const query = params.toString();
  return query ? `/now-playing?${query}` : '/now-playing';
}
