/**
 * Admin backend connection states, their visible labels and the
 * screen-reader announcement for each change.
 *
 * Kept free of solid-js and window so `node --test` can load it.
 */

export type AdminConnection = 'connecting' | 'connected' | 'reconnecting' | 'offline';

const LABELS: Record<AdminConnection, string> = {
  connecting: 'Connecting to backend',
  connected: 'Backend connected',
  reconnecting: 'Reconnecting to backend',
  offline: 'Backend offline',
};

const ANNOUNCEMENTS: Record<AdminConnection, string> = {
  connecting: 'Connecting to backend.',
  connected: 'Backend connected. Live sync resumed.',
  reconnecting: 'Backend connection lost. Reconnecting.',
  offline: 'Backend offline. Waiting for the network.',
};

export function connectionLabel(state: AdminConnection): string {
  return LABELS[state];
}

/** The sentence to announce for a change, or null when nothing should be said. */
export function connectionAnnouncement(prev: AdminConnection, next: AdminConnection): string | null {
  if (prev === next) return null;
  // The first connect on page load is expected; announcing it is noise.
  if (prev === 'connecting' && next === 'connected') return null;
  return ANNOUNCEMENTS[next];
}
