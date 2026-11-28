/* Library ordering and the "New" badge for the admin console. */

export type LibrarySort = 'title' | 'recent';

export const NEW_WINDOW_MS = 24 * 60 * 60 * 1000;
// A backend clock slightly ahead of the browser must not hide a fresh song.
const CLOCK_SKEW_MS = 5 * 60 * 1000;

// An unparsable date sorts last.
function addedMs(addedAt: string): number {
  const t = Date.parse(addedAt);
  return Number.isNaN(t) ? -Infinity : t;
}

/** Returns a new array. 'title' keeps the server's order; 'recent' is newest first, ties in server order. */
export function sortSongs<T extends { added_at: string }>(songs: readonly T[], sort: LibrarySort): T[] {
  if (sort === 'title') return [...songs];
  // Array.prototype.sort is stable, so ties keep server order.
  return [...songs].sort((a, b) => {
    const ta = addedMs(a.added_at);
    const tb = addedMs(b.added_at);
    return ta === tb ? 0 : tb > ta ? 1 : -1;
  });
}

/** True when the song joined the library in the last 24 hours. */
export function isNew(addedAt: string, nowMs: number): boolean {
  const t = Date.parse(addedAt);
  if (Number.isNaN(t)) return false;
  const age = nowMs - t;
  return age >= -CLOCK_SKEW_MS && age < NEW_WINDOW_MS;
}
