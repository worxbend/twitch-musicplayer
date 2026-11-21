/**
 * Reconnect and heartbeat timing shared by the socket clients.
 *
 * Kept free of window and Vite env so `node --test` can load it.
 */

/** Interval between client heartbeat frames; the server answers each with heartbeat_ack. */
export const HEARTBEAT_MS = 25000;

/** Silence longer than this means the socket is half-open: two missed acks plus slack. */
export const STALE_AFTER_MS = HEARTBEAT_MS * 2.5;

/** Exponential backoff: baseMs doubled per failed attempt, capped at maxMs. */
export function reconnectDelay(attempt: number, baseMs = 900, maxMs = 8000): number {
  return Math.min(baseMs * 2 ** attempt, maxMs);
}

/** True when nothing has arrived for longer than timeoutMs. */
export function isStale(lastMessageAt: number, now: number, timeoutMs = STALE_AFTER_MS): boolean {
  return now - lastMessageAt > timeoutMs;
}
