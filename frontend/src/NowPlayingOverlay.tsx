import { Show, createEffect, createSignal, onCleanup, onMount } from 'solid-js';
import { coverURL, getWebSocketURL } from './config/backend';
import { HEARTBEAT_MS, armHandshakeTimeout, isStale, reconnectDelay } from './config/reconnect';
import { nextShown, reduceNowPlaying, reduceUpNext, type NowPlayingOptions, type NowPlayingSong, type ShownKey } from './nowPlaying';

type Props = NowPlayingOptions;

const heartbeatFrame = JSON.stringify({ type: 'heartbeat' });

/**
 * Transparent text overlay for OBS. It only listens: the server dequeues on
 * need_song or song_finished, and this socket sends nothing but heartbeat, so
 * it can never advance the queue. Silence past STALE_AFTER_MS (no heartbeat_ack)
 * means a half-open socket, so it reconnects. With `hold` > 0 the text fades out
 * that many seconds after each new song start or title change. With `next` it
 * also shows the next queued title from the frames it already receives.
 */
export function NowPlayingOverlay(props: Props) {
  const [song, setSong] = createSignal<NowPlayingSong | null>(null);
  const [upNext, setUpNext] = createSignal<NowPlayingSong | null>(null);
  const [visible, setVisible] = createSignal(true);
  // A cover that fails to load is dropped, so OBS never shows a broken image.
  const [failedCover, setFailedCover] = createSignal('');
  const cover = () => {
    const id = props.art ? song()?.coverId : undefined;
    const url = id ? coverURL(id) : '';
    return url !== failedCover() ? url : '';
  };
  let shown: ShownKey = { key: '', title: '' };
  let hideTimer = 0;
  let lastMessageAt = 0;
  let socket: WebSocket | null = null;
  let reconnectTimer = 0;
  let heartbeatTimer = 0;
  let reconnectAttempts = 0;
  let running = true;
  let cancelHandshake = () => {};

  const closeSocket = () => {
    window.clearInterval(heartbeatTimer);
    cancelHandshake();
    if (!socket) return;
    socket.onopen = socket.onmessage = socket.onclose = socket.onerror = null;
    socket.close();
    socket = null;
  };

  const scheduleReconnect = () => {
    const delay = reconnectDelay(reconnectAttempts);
    reconnectAttempts += 1;
    window.clearTimeout(reconnectTimer);
    reconnectTimer = window.setTimeout(connect, delay);
  };

  function connect() {
    if (!running) return;
    let ws: WebSocket;
    try {
      ws = new WebSocket(getWebSocketURL());
    } catch {
      // A bad backend URL or a CSP block throws instead of firing onclose.
      socket = null;
      scheduleReconnect();
      return;
    }
    socket = ws;
    cancelHandshake = armHandshakeTimeout(ws, () => {
      closeSocket();
      scheduleReconnect();
    });
    ws.onopen = () => {
      cancelHandshake();
      reconnectAttempts = 0;
      lastMessageAt = Date.now();
      window.clearInterval(heartbeatTimer);
      heartbeatTimer = window.setInterval(() => {
        if (isStale(lastMessageAt, Date.now())) {
          // closeSocket drops onclose, so the reconnect is scheduled here.
          closeSocket();
          scheduleReconnect();
          return;
        }
        ws.send(heartbeatFrame);
      }, HEARTBEAT_MS);
    };
    ws.onmessage = event => {
      lastMessageAt = Date.now();
      if (typeof event.data !== 'string') return;
      const raw = event.data;
      setSong(prev => reduceNowPlaying(prev, raw));
      setUpNext(prev => reduceUpNext(prev, raw));
    };
    ws.onclose = () => {
      closeSocket();
      if (running) scheduleReconnect();
    };
  }

  // Shows on a new song start (history_id), even with a repeated title, or on a changed
  // title. A reconnect `state` frame or same-title retag keeps a hidden title hidden.
  createEffect(() => {
    const next = nextShown(shown, song());
    shown = next.shown;
    if (!next.show) return;
    setVisible(true);
    window.clearTimeout(hideTimer);
    if (props.hold > 0) hideTimer = window.setTimeout(() => setVisible(false), props.hold * 1000);
  });

  onMount(() => {
    document.documentElement.classList.add('logo-overlay-page');
    connect();
  });

  onCleanup(() => {
    running = false;
    window.clearTimeout(reconnectTimer);
    window.clearTimeout(hideTimer);
    closeSocket();
    document.documentElement.classList.remove('logo-overlay-page');
  });

  return (
    <div class="now-playing-overlay" classList={{
        'np-hidden': !visible(),
        'np-theme-outline': props.theme === 'outline',
        'np-theme-pill': props.theme === 'pill',
        'np-font-mono': props.font === 'mono',
      }} style={{ 'text-align': props.align, '--np-size': `${props.size}px` }}>
      <Show when={song()?.title} keyed>
        {title => (
          <div class="np-track" classList={{ 'np-track--art': cover() !== '' }}>
            <Show when={cover()} keyed>
              {url => <img class="np-art" src={url} alt="" decoding="async" onError={() => setFailedCover(url)} />}
            </Show>
            <div class="np-text">
              <p class="np-title">{title}</p>
              <Show when={song()?.artist}>
                <p class="np-artist">{song()?.artist}</p>
              </Show>
              <Show when={props.next && upNext()?.title}>
                <p class="np-next">Up next: {upNext()?.title}</p>
              </Show>
            </div>
          </div>
        )}
      </Show>
    </div>
  );
}
