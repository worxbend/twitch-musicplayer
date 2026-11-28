/**
 * Library view of the blocklist: which song ids are out of rotation, and the
 * reason a disabled queue or play control gives for it.
 *
 * Kept free of solid-js and window so `node --test` can load it.
 */

/** Ids from GET /v1/blocked, including entries whose file left the library. */
export function blockedIdSet(entries: readonly { id: string }[]): Set<string> {
  return new Set(entries.map(entry => entry.id));
}

export function blockedReason(label: string): string {
  return `Blocked: ${label} is out of rotation`;
}
