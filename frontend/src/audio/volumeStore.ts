/**
 * Master volume persistence and keyboard rules for the player.
 *
 * Pure functions so `node --test` can load them without a DOM. Storage can
 * throw (private mode, OBS browser sources), so every access falls back silently.
 * Mute is UI-only on purpose: a reload always comes back audible at the saved level.
 */
import { clamp } from '../viz/util.ts';

export const VOLUME_KEY = 'lofi.volume';
export const VOLUME_STEP = 0.05;

const TYPING_TAGS = new Set(['INPUT', 'TEXTAREA', 'SELECT']);

export function parseVolume(raw: string | null): number {
  if (raw === null || raw.trim() === '') return 1;
  const parsed = Number(raw);
  return Number.isNaN(parsed) ? 1 : clamp(parsed, 0, 1);
}

export function stepVolume(volume: number, delta: number): number {
  return Math.round(clamp(volume + delta, 0, 1) * 100) / 100;
}

export function loadVolume(storage: Pick<Storage, 'getItem'> | undefined): number {
  try {
    return parseVolume(storage?.getItem(VOLUME_KEY) ?? null);
  } catch {
    return 1;
  }
}

export function saveVolume(storage: Pick<Storage, 'setItem'> | undefined, volume: number): void {
  try {
    storage?.setItem(VOLUME_KEY, String(volume));
  } catch {
    // Not persisting is acceptable; the in-memory volume still applies.
  }
}

interface KeyEventLike {
  ctrlKey: boolean;
  metaKey: boolean;
  altKey: boolean;
  target: unknown;
}

// Shortcuts must not fire while the user types or uses a browser/OS chord.
export function shouldIgnoreVolumeKey(event: KeyEventLike): boolean {
  if (event.ctrlKey || event.metaKey || event.altKey) return true;
  if (typeof event.target !== 'object' || event.target === null) return false;
  const target = event.target as { tagName?: unknown; isContentEditable?: unknown };
  return target.isContentEditable === true
    || (typeof target.tagName === 'string' && TYPING_TAGS.has(target.tagName.toUpperCase()));
}
