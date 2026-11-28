// Track-time text for the player deck, progress bar and admin console.
// No solid-js import, so node --test can load it.

const pad2 = (n: number) => n.toString().padStart(2, '0');

/** Formats seconds as m:ss, or h:mm:ss from an hour on. Non-finite or negative input reads 0:00. */
export function formatTrackTime(secs: number): string {
  const total = Number.isFinite(secs) && secs > 0 ? Math.floor(secs) : 0;
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const seconds = total % 60;
  if (hours === 0) return `${minutes}:${pad2(seconds)}`;
  return `${hours}:${pad2(minutes)}:${pad2(seconds)}`;
}

/** Formats a song duration, or '--:--' when it is unknown (non-finite or not positive). */
export function formatDuration(secs: number): string {
  if (!Number.isFinite(secs) || secs <= 0) return '--:--';
  return formatTrackTime(secs);
}
