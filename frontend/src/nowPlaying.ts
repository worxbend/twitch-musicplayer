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
export type NowPlayingOptions = { align: NowPlayingAlign; size: number };

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

const alignments: readonly NowPlayingAlign[] = ['left', 'center', 'right'];
const defaultSizePx = 32;
const minSizePx = 12;
const maxSizePx = 120;

/** Reads `?align=` and `?size=` (px); anything invalid falls back to left / 32. */
export function parseNowPlayingOptions(search: string): NowPlayingOptions {
  const params = new URLSearchParams(search);
  const rawAlign = params.get('align');
  const align = alignments.find(value => value === rawAlign) ?? 'left';
  const rawSize = params.get('size') ?? '';
  const px = /^\d+$/.test(rawSize) ? Number(rawSize) : NaN;
  const size = px >= minSizePx && px <= maxSizePx ? px : defaultSizePx;
  return { align, size };
}
