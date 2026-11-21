import type { ConnectionStatus, PlaybackStatus } from '../audio/BackendPlaybackClient';

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

// `paused` is the audio element's own state (media keys pause it without
// telling the backend client), so it only matters while playback is 'playing'.
export function playerStatus(ws: ConnectionStatus, playback: PlaybackStatus, paused: boolean): PlayerStatus {
  if (ws !== 'connected') {
    return {
      live: false,
      label: ws === 'reconnecting' ? 'Reconnecting' : 'Offline',
      detail: 'Waiting for the backend. Reconnecting automatically.',
    };
  }
  if (playback !== 'playing') return { live: false, label: 'Standby', detail: STANDBY_DETAIL[playback] };
  if (paused) return { live: false, label: 'Paused', detail: 'Paused. Press play or a media key to resume.' };
  return { live: true, label: 'On air', detail: 'Your library, on a continuous frequency.' };
}
