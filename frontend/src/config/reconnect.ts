/**
 * Reconnect and heartbeat timing shared by the socket clients.
 *
 * Kept free of window and Vite env so `node --test` can load it.
 */

/** Interval between client heartbeat frames; the server answers each with heartbeat_ack. */
export const HEARTBEAT_MS = 25000;

/** Silence longer than this means the socket is half-open: two missed acks plus slack. */
export const STALE_AFTER_MS = HEARTBEAT_MS * 2.5;

/** A socket still CONNECTING after this long is treated as dead and retried. */
export const HANDSHAKE_TIMEOUT_MS = 10000;

/** Exponential backoff: baseMs doubled per failed attempt, capped at maxMs. */
export function reconnectDelay(attempt: number, baseMs = 900, maxMs = 8000): number {
  return Math.min(baseMs * 2 ** attempt, maxMs);
}

/** True when nothing has arrived for longer than timeoutMs. */
export function isStale(lastMessageAt: number, now: number, timeoutMs = STALE_AFTER_MS): boolean {
  return now - lastMessageAt > timeoutMs;
}

const CONNECTING = 0;

/**
 * Calls onTimeout if the socket is still CONNECTING after ms. A stalled
 * handshake fires neither onopen nor onclose, so without this a client waits
 * minutes for the browser's TCP timeout. Returns an idempotent cancel.
 */
export function armHandshakeTimeout(
  socket: { readonly readyState: number },
  onTimeout: () => void,
  ms = HANDSHAKE_TIMEOUT_MS,
): () => void {
  const timer = globalThis.setTimeout(() => {
    if (socket.readyState === CONNECTING) onTimeout();
  }, ms);
  return () => globalThis.clearTimeout(timer);
}
