import type { ConnectionStatus, ErrorCode, PlaybackStatus } from '../audio/BackendPlaybackClient';

export interface PlayerStatus {
  live: boolean;
  label: string;
  detail: string;
}

const STANDBY_DETAIL: Record<Exclude<PlaybackStatus, 'playing'>, string> = {
  blocked: 'Your browser needs a click before it can play audio.',
  error: 'Waiting for playable audio. Check your library; the player will retry automatically.',
  idle: 'Connected. Waiting for the next track.',
};

// A dry queue or an empty library has a known fix; anything else keeps the generic hint.
const ERROR_DETAIL: Partial<Record<ErrorCode, string>> = {
  queue_empty: 'The queue is empty. Add a track in the console.',
  library_empty: 'Your library is empty. Add MP3 files to the music folder.',
};

// `paused` is the audio element's own state (media keys pause it without
// telling the backend client), so it only matters while playback is 'playing'.
export function playerStatus(
  ws: ConnectionStatus,
  playback: PlaybackStatus,
  paused: boolean,
  errorCode?: ErrorCode,
): PlayerStatus {
  if (ws !== 'connected') {
    return {
      live: false,
      label: ws === 'reconnecting' ? 'Reconnecting' : 'Offline',
      detail: 'Waiting for the backend. Reconnecting automatically.',
    };
  }
  if (playback === 'error') {
    return { live: false, label: 'Standby', detail: ERROR_DETAIL[errorCode ?? 'internal'] ?? STANDBY_DETAIL.error };
  }
  if (playback !== 'playing') return { live: false, label: 'Standby', detail: STANDBY_DETAIL[playback] };
  if (paused) return { live: false, label: 'Paused', detail: 'Paused. Press play or a media key to resume.' };
  return { live: true, label: 'On air', detail: 'Your library, on a continuous frequency.' };
}

export interface PlayToggle {
  action: 'enable' | 'toggle' | 'none';
  pressed: boolean;
}

// The deck's play/pause toggle. Pressed mirrors the audio element's paused state,
// so a media key and the button move it the same way. Audio the browser blocks
// is not playing, so the toggle reads as paused and shows the play glyph.
export function playToggle(playback: PlaybackStatus, paused: boolean): PlayToggle {
  if (playback === 'blocked') return { action: 'enable', pressed: true };
  if (playback === 'playing') return { action: 'toggle', pressed: paused };
  return { action: 'none', pressed: false };
}
