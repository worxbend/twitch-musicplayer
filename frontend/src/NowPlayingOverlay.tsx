import { Show, createSignal, onCleanup, onMount } from 'solid-js';
import { getWebSocketURL } from './config/backend';
import { reduceNowPlaying, type NowPlayingOptions, type NowPlayingSong } from './nowPlaying';

type Props = NowPlayingOptions;

// Same reconnect curve as BackendPlaybackClient.
const reconnectBaseMs = 900;
const reconnectMaxMs = 8000;
const heartbeatMs = 25000;
const heartbeatFrame = JSON.stringify({ type: 'heartbeat' });

/**
 * Transparent text overlay for OBS. It only listens: the server dequeues on
 * need_song or song_finished, and this socket sends nothing but heartbeat, so
 * it can never advance the queue.
 */
export function NowPlayingOverlay(props: Props) {
  const [song, setSong] = createSignal<NowPlayingSong | null>(null);
  let socket: WebSocket | null = null;
  let reconnectTimer = 0;
  let heartbeatTimer = 0;
  let reconnectAttempts = 0;
  let running = true;

  const closeSocket = () => {
    window.clearInterval(heartbeatTimer);
    if (!socket) return;
    socket.onopen = socket.onmessage = socket.onclose = socket.onerror = null;
    socket.close();
    socket = null;
  };

  const scheduleReconnect = () => {
    const delay = Math.min(reconnectBaseMs * 2 ** reconnectAttempts, reconnectMaxMs);
    reconnectAttempts += 1;
    window.clearTimeout(reconnectTimer);
    reconnectTimer = window.setTimeout(connect, delay);
  };

  function connect() {
    if (!running) return;
    const ws = new WebSocket(getWebSocketURL());
    socket = ws;
    ws.onopen = () => {
      reconnectAttempts = 0;
      window.clearInterval(heartbeatTimer);
      heartbeatTimer = window.setInterval(() => ws.send(heartbeatFrame), heartbeatMs);
    };
    ws.onmessage = event => {
      if (typeof event.data === 'string') setSong(prev => reduceNowPlaying(prev, event.data));
    };
    ws.onclose = () => {
      closeSocket();
      if (running) scheduleReconnect();
    };
  }

  onMount(() => {
    document.documentElement.classList.add('logo-overlay-page');
    connect();
  });

  onCleanup(() => {
    running = false;
    window.clearTimeout(reconnectTimer);
    closeSocket();
    document.documentElement.classList.remove('logo-overlay-page');
  });

  return (
    <div class="now-playing-overlay" style={{ 'text-align': props.align, '--np-size': `${props.size}px` }}>
      <Show when={song()?.title} keyed>
        {title => (
          <div class="np-track">
            <p class="np-title">{title}</p>
            <Show when={song()?.artist}>
              <p class="np-artist">{song()?.artist}</p>
            </Show>
          </div>
        )}
      </Show>
    </div>
  );
}
