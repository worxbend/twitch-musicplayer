/**
 * Pure reducer for the /now-playing text overlay.
 *
 * Kept free of config, Vite env and Pixi imports so `node --test` can load it.
 * It reads the two server frames that carry the current song: `state`, sent
 * once on connect, and `now_playing`, broadcast whenever a song starts. The
 * "Up next" line reads `next_song` from `state` and `queue_updated`. Frames
 * are `{type, payload}` envelopes (backend/internal/websocket/protocol.go).
 */

/**
 * `startId` is now_playing's history_id: set on a real song start, absent on a retag or `state` frame.
 * `coverId` is the song id when the song has embedded cover art; the overlay turns it into a URL.
 * `durationSecs` is the song length (> 0); `elapsedSecs` is the server's play time when the frame was built.
 */
export type NowPlayingSong = {
  title: string;
  artist: string;
  startId?: string;
  coverId?: string;
  durationSecs?: number;
  elapsedSecs?: number;
};
export type NowPlayingAlign = 'left' | 'center' | 'right';
/** `shadow` is the soft default; `outline` and `pill` stay readable over bright footage. */
export type NowPlayingTheme = 'shadow' | 'outline' | 'pill';
/** `sans` is the page font; `mono` is the bundled JetBrains Mono. */
export type NowPlayingFont = 'sans' | 'mono';
/**
 * `hold` is seconds the title stays visible after each change; 0 keeps it visible. `art` shows the cover.
 * `next` shows an "Up next" line with the next queued title. `progress` shows a track progress bar.
 */
export type NowPlayingOptions = {
  align: NowPlayingAlign;
  size: number;
  hold: number;
  art: boolean;
  theme: NowPlayingTheme;
  font: NowPlayingFont;
  next: boolean;
  progress: boolean;
};

type JsonObject = Record<string, unknown>;

function isObject(value: unknown): value is JsonObject {
  return typeof value === 'object' && value !== null;
}

function asText(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}

/** A finite number >= 0, else undefined. */
function asSeconds(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : undefined;
}

function stripExtension(filename: string): string {
  const dot = filename.lastIndexOf('.');
  return dot > 0 ? filename.slice(0, dot) : filename;
}

/** A song needs a title or a filename to be shown; otherwise it is unusable. */
function toSong(raw: unknown): NowPlayingSong | null {
  if (!isObject(raw)) return null;
  const title = asText(raw.title) || stripExtension(asText(raw.filename));
  if (!title) return null;
  const song: NowPlayingSong = { title, artist: asText(raw.artist) };
  const coverId = raw.has_cover === true ? asText(raw.id) : '';
  if (coverId) song.coverId = coverId;
  const durationSecs = asSeconds(raw.duration_secs);
  if (durationSecs) song.durationSecs = durationSecs;
  return song;
}

/** Copies the payload's `elapsed_secs` onto `song` when it is a usable number. */
function withElapsed(song: NowPlayingSong | null, payload: JsonObject): NowPlayingSong | null {
  const elapsedSecs = asSeconds(payload.elapsed_secs);
  return song && elapsedSecs !== undefined ? { ...song, elapsedSecs } : song;
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

  const payload = msg.payload;
  if (msg.type === 'state') return withElapsed(toSong(payload.current_song), payload);
  if (msg.type === 'now_playing') return withElapsed(withStartId(toSong(payload.song), asText(payload.history_id)), payload) ?? prev;
  return prev;
}

/**
 * Applies one raw socket frame to the "Up next" song. `state` and `queue_updated` carry the
 * next queued song as `next_song`; a missing key means nothing is queued. Other frames keep `prev`.
 */
export function reduceUpNext(prev: NowPlayingSong | null, raw: string): NowPlayingSong | null {
  const msg = parse(raw);
  if (!msg || !isObject(msg.payload)) return prev;
  if (msg.type === 'state' || msg.type === 'queue_updated') return toSong(msg.payload.next_song);
  return prev;
}

function withStartId(song: NowPlayingSong | null, startId: string): NowPlayingSong | null {
  return song && startId ? { ...song, startId } : song;
}

/**
 * Where the progress bar is `nowMs` after the frame carrying `song` arrived at `receivedAtMs`:
 * the server's elapsed time plus the local time since, clamped to 0..duration. Null when the
 * song lacks a duration or an elapsed time, so the bar is hidden.
 */
export function progressTiming(
  song: NowPlayingSong | null,
  receivedAtMs: number,
  nowMs: number,
): { durationSecs: number; elapsedSecs: number } | null {
  const durationSecs = song?.durationSecs;
  const startSecs = song?.elapsedSecs;
  if (durationSecs === undefined || startSecs === undefined) return null;
  const elapsedSecs = Math.min(durationSecs, Math.max(0, startSecs + (nowMs - receivedAtMs) / 1000));
  return { durationSecs, elapsedSecs };
}

/** WebSocket close code the backend hub sends on graceful shutdown. */
export const GOING_AWAY = 1001;

/** True when a socket close means the server stopped, so the overlay clears its title; network drops keep it. */
export function clearsOnClose(code: number): boolean {
  return code === GOING_AWAY;
}

/** What the overlay last showed: `key` is the start id, or the title when the frame had none. */
export interface ShownKey { key: string; title: string }

const noneShown: ShownKey = { key: '', title: '' };

/**
 * Decides whether the overlay re-shows the title for `song`. A new start id always
 * shows, so a repeated title re-appears under ?hold. Without one (a retag re-announce
 * or a reconnect `state` frame) only a changed title shows, so a hidden title stays
 * hidden. No song resets `shown`, which re-arms the next start after an idle clear.
 */
export function nextShown(shown: ShownKey, song: NowPlayingSong | null): { show: boolean; shown: ShownKey } {
  const title = song?.title ?? '';
  if (!title) return { show: false, shown: noneShown };
  const startId = song?.startId;
  if (startId) {
    return { show: startId !== shown.key, shown: { key: startId, title } };
  }
  if (title !== shown.title) return { show: true, shown: { key: title, title } };
  return { show: false, shown };
}

// The first entry of each list is the default that parsing falls back to.
export const nowPlayingAlignments: readonly NowPlayingAlign[] = ['left', 'center', 'right'];
export const nowPlayingThemes: readonly NowPlayingTheme[] = ['shadow', 'outline', 'pill'];
export const nowPlayingFonts: readonly NowPlayingFont[] = ['sans', 'mono'];
const defaultSizePx = 32;
const minSizePx = 12;
const maxSizePx = 120;
const maxHoldSecs = 600;

function parseWholeNumber(raw: string | null): number {
  return raw !== null && /^\d+$/.test(raw) ? Number(raw) : NaN;
}

/** The entry of `allowed` that equals `raw` exactly, else the first entry (the default). */
function pick<T extends string>(allowed: readonly T[], raw: unknown): T {
  return allowed.find(value => value === raw) ?? allowed[0];
}

/**
 * Reads `?align=`, `?size=` (px), `?hold=` (seconds, 1..600), `?art=1`, `?theme=`,
 * `?font=`, `?next=1` and `?progress=1`. Anything invalid falls back to left / 32 / 0 / no art /
 * shadow / sans / no up-next line / no progress bar, where hold 0 means always visible.
 */
export function parseNowPlayingOptions(search: string): NowPlayingOptions {
  const params = new URLSearchParams(search);
  const align = pick(nowPlayingAlignments, params.get('align'));
  const px = parseWholeNumber(params.get('size'));
  const size = px >= minSizePx && px <= maxSizePx ? px : defaultSizePx;
  const secs = parseWholeNumber(params.get('hold'));
  const hold = secs >= 1 && secs <= maxHoldSecs ? secs : 0;
  const theme = pick(nowPlayingThemes, params.get('theme'));
  const font = pick(nowPlayingFonts, params.get('font'));
  return {
    align,
    size,
    hold,
    art: params.get('art') === '1',
    theme,
    font,
    next: params.get('next') === '1',
    progress: params.get('progress') === '1',
  };
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
  const align = pick(nowPlayingAlignments, opts.align);
  const theme = pick(nowPlayingThemes, opts.theme);
  const font = pick(nowPlayingFonts, opts.font);
  const size = clampWhole(opts.size, minSizePx, maxSizePx, defaultSizePx);
  const hold = clampWhole(opts.hold, 0, maxHoldSecs, 0);
  const params = new URLSearchParams();
  if (align !== 'left') params.set('align', align);
  if (size !== defaultSizePx) params.set('size', String(size));
  if (hold !== 0) params.set('hold', String(hold));
  if (opts.art === true) params.set('art', '1');
  if (theme !== 'shadow') params.set('theme', theme);
  if (font !== 'sans') params.set('font', font);
  if (opts.next === true) params.set('next', '1');
  if (opts.progress === true) params.set('progress', '1');
  const query = params.toString();
  return query ? `/now-playing?${query}` : '/now-playing';
}
