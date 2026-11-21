import type { AudioEngine } from './AudioEngine';
import { getWebSocketURL, resolveStreamURL } from '../config/backend.ts';

type MessageType =
  | 'need_song'
  | 'song_finished'
  | 'peek_next'
  | 'heartbeat'
  | 'play_song'
  | 'prebuffer_song'
  | 'queue_updated'
  | 'error'
  | 'heartbeat_ack'
  | 'state'
  | 'skip_now';

interface SocketMessage<T = unknown> {
  type: MessageType;
  payload?: T;
}

interface Song {
  id: string;
  title: string;
  artist?: string;
  filename: string;
}

interface PlaySongPayload {
  song: Song;
  stream_url: string;
  history_id: string;
}

interface ErrorPayload {
  message: string;
}

export interface SongInfo { title: string; artist?: string }

/** Health of the WebSocket link to the backend. */
export type ConnectionStatus = 'connected' | 'reconnecting' | 'disconnected';

/** What the audio element is currently doing. */
export type PlaybackStatus = 'idle' | 'playing' | 'blocked' | 'error';

/**
 * Connection health and playback health are independent — the socket can be
 * healthy while audio is blocked by autoplay policy, and vice versa. They are
 * reported as separate fields so callers switch on a value instead of
 * pattern-matching a human-readable string.
 */
export interface RadioStatus {
  connection: ConnectionStatus;
  playback: PlaybackStatus;
  /** Human-readable detail, for logs and tooltips only. */
  message: string;
}

interface BackendPlaybackOptions {
  audio: AudioEngine;
  onStatus?: (status: RadioStatus) => void;
  onSongChange?: (song: SongInfo | null) => void;
}

const reconnectBaseMs = 900;
const reconnectMaxMs = 8000;
const heartbeatMs = 25000;
const heartbeatTimeoutMs = heartbeatMs * 2.5;
const streamRetryMs = 5000;
const maxStreamRetries = 3;
const awaitSongTimeoutMs = 15000;
const errorRetryMaxMs = 30000;
const maxOutboxMessages = 20;
const messageTypes = new Set<MessageType>([
  'need_song',
  'song_finished',
  'peek_next',
  'heartbeat',
  'play_song',
  'prebuffer_song',
  'queue_updated',
  'error',
  'heartbeat_ack',
  'state',
  'skip_now',
]);

export class BackendPlaybackClient {
  private audio: AudioEngine;
  private onStatus?: (status: RadioStatus) => void;
  private onSongChange?: (song: SongInfo | null) => void;
  private socket: WebSocket | null = null;
  private reconnectTimer = 0;
  private heartbeatTimer = 0;
  private reconnectAttempts = 0;
  private shouldRun = false;
  private pendingPlay: PlaySongPayload | null = null;
  private current: PlaySongPayload | null = null;
  private outbox: SocketMessage[] = [];
  private awaitingSong = false;
  private lastMessageAt = 0;
  private connecting = false;
  private streamRetryTimer = 0;
  // Retries spent on one history_id; a song that keeps failing is skipped.
  private streamRetryHistoryId: string | null = null;
  private streamRetryAttempts = 0;
  private errorRetryTimer = 0;
  private errorRetryAttempts = 0;
  private awaitSongTimer = 0;
  private connection: ConnectionStatus = 'disconnected';
  private playback: PlaybackStatus = 'idle';
  // Bumped by every play(); a play that settles after a newer one started is ignored.
  private playToken = 0;
  private playInFlight = false;
  // history_id of the song whose title is on screen; it can outlive `current`
  // through the near-end handoff, while the old track is still audible.
  private displayedHistoryId: string | null = null;

  constructor(options: BackendPlaybackOptions) {
    this.audio = options.audio;
    this.onStatus = options.onStatus;
    this.onSongChange = options.onSongChange;
  }

  /** Records the latest status and notifies the listener. */
  private report(
    change: { connection?: ConnectionStatus; playback?: PlaybackStatus },
    message: string,
  ) {
    if (change.connection) this.connection = change.connection;
    if (change.playback) this.playback = change.playback;
    this.onStatus?.({ connection: this.connection, playback: this.playback, message });
  }

  start() {
    if (this.shouldRun) return;
    this.shouldRun = true;
    window.addEventListener('online', this.handleOnline);
    window.addEventListener('offline', this.handleOffline);
    this.connect();
  }

  stop() {
    this.shouldRun = false;
    // Void any in-flight play so it cannot report a title or become current after stop.
    this.playToken += 1;
    this.playInFlight = false;
    this.pendingPlay = null;
    this.current = null;
    this.awaitingSong = false;
    this.resetStreamRetries();
    window.clearTimeout(this.reconnectTimer);
    this.reconnectTimer = 0;
    window.clearInterval(this.heartbeatTimer);
    window.clearTimeout(this.streamRetryTimer);
    window.clearTimeout(this.errorRetryTimer);
    window.clearTimeout(this.awaitSongTimer);
    window.removeEventListener('online', this.handleOnline);
    window.removeEventListener('offline', this.handleOffline);
    if (this.socket) {
      detachSocket(this.socket);
      this.socket.close();
    }
    this.socket = null;
    this.connecting = false;
    this.report({ connection: 'disconnected', playback: 'idle' }, 'radio stopped');
    this.clearDisplayedSong();
  }

  /** Retries a play the browser blocked; called from a user gesture. */
  retryPendingPlay() {
    if (this.playback !== 'blocked' || this.playInFlight || !this.pendingPlay) return;
    return this.play(this.pendingPlay);
  }

  private connect() {
    if (!this.shouldRun) return;
    if (this.connecting) return;
    if (this.socket && this.socket.readyState <= WebSocket.OPEN) return;

    this.connecting = true;
    const socket = new WebSocket(getWebSocketURL());
    this.socket = socket;
    this.lastMessageAt = Date.now();

    socket.onopen = () => {
      this.connecting = false;
      this.reconnectAttempts = 0;
      this.report({ connection: 'connected' }, 'radio connected');
      const requestQueued = this.outbox.some(isSongRequest);
      this.flushOutbox();
      // Awaiting with nothing queued means the request went out on a socket
      // that is now gone, so its reply is lost too.
      const requestLost = this.awaitingSong && !requestQueued;
      if (!this.current && !this.pendingPlay && (!this.awaitingSong || requestLost)) this.requestSong();
      window.clearInterval(this.heartbeatTimer);
      this.heartbeatTimer = window.setInterval(() => {
        if (Date.now() - this.lastMessageAt > heartbeatTimeoutMs) {
          this.reconnectNow('radio heartbeat timeout');
          return;
        }
        this.send({ type: 'heartbeat' });
      }, heartbeatMs);
    };

    socket.onmessage = event => {
      this.lastMessageAt = Date.now();
      const msg = parseMessage(event.data);
      if (!msg) return;
      void this.handleMessage(msg);
    };

    socket.onclose = () => {
      if (this.socket !== socket) {
        detachSocket(socket);
        return;
      }
      this.connecting = false;
      window.clearInterval(this.heartbeatTimer);
      this.socket = null;
      if (this.shouldRun) {
        this.report({ connection: 'reconnecting' }, 'radio disconnected');
        this.scheduleReconnect();
      } else {
        this.report({ connection: 'disconnected', playback: 'idle' }, 'radio stopped');
      }
    };

    socket.onerror = () => {
      if (this.socket !== socket) {
        detachSocket(socket);
        return;
      }
      this.reconnectNow('radio connection error');
    };
  }

  private scheduleReconnect() {
    if (!this.shouldRun) return;
    const delay = Math.min(reconnectBaseMs * 2 ** this.reconnectAttempts, reconnectMaxMs);
    this.reconnectAttempts += 1;
    window.clearTimeout(this.reconnectTimer);
    this.reconnectTimer = window.setTimeout(() => {
      this.reconnectTimer = 0;
      this.connect();
    }, delay);
  }

  private async handleMessage(msg: SocketMessage) {
    switch (msg.type) {
      case 'play_song':
        if (!isPlaySongPayload(msg.payload)) return;
        this.awaitingSong = false;
        window.clearTimeout(this.awaitSongTimer);
        await this.play(msg.payload);
        break;
      case 'prebuffer_song':
        // Response to peek_next: buffer the next track on the inactive channel. No dequeue,
        // no state change — when playStream is called for this URL it skips re-loading.
        if (!isPlaySongPayload(msg.payload)) return;
        this.audio.preloadNext(resolveStreamURL(msg.payload.stream_url));
        break;
      case 'queue_updated':
        if (!this.current && !this.pendingPlay && !this.awaitingSong) this.requestSong();
        break;
      case 'skip_now':
        this.current = null;
        // Drop the skipped song entirely: a leftover pendingPlay blocks every
        // need_song recovery path, and the token bump voids its in-flight play.
        this.pendingPlay = null;
        this.playToken += 1;
        this.playInFlight = false;
        this.resetStreamRetries();
        window.clearTimeout(this.streamRetryTimer);
        window.clearTimeout(this.errorRetryTimer);
        window.clearTimeout(this.awaitSongTimer);
        this.awaitingSong = false;
        this.requestSong();
        break;
      case 'error':
        if (msg.payload !== undefined && !isErrorPayload(msg.payload)) return;
        this.awaitingSong = false;
        window.clearTimeout(this.awaitSongTimer);
        this.report({ playback: 'error' }, msg.payload?.message ?? 'radio error');
        this.scheduleErrorRetry();
        break;
    }
  }

  // Backs off so an empty library does not draw a need_song every second from every player.
  private scheduleErrorRetry() {
    const delay = Math.min(reconnectBaseMs * 2 ** this.errorRetryAttempts, errorRetryMaxMs);
    this.errorRetryAttempts += 1;
    window.clearTimeout(this.errorRetryTimer);
    this.errorRetryTimer = window.setTimeout(() => {
      if (this.shouldRun && !this.current && !this.pendingPlay && !this.awaitingSong) this.requestSong();
    }, delay);
  }

  private async play(payload: PlaySongPayload) {
    if (!payload?.song || !payload.stream_url || !payload.history_id) return;

    const token = ++this.playToken;
    this.playInFlight = true;
    this.pendingPlay = payload;
    const songName = payload.song.title || payload.song.filename;

    try {
      await this.audio.playStream(resolveStreamURL(payload.stream_url), songName, {
        onEnded: () => this.handleEnded(payload),
        onError: () => this.retryStream(payload),
        onPrefetch: () => this.handlePrefetch(payload),
        onNearEnd: () => this.handleNearEnd(payload),
        // Show the title as soon as the track is audible, not after the crossfade.
        onStarted: () => {
          if (token !== this.playToken) return;
          this.displayedHistoryId = payload.history_id;
          this.onSongChange?.({ title: songName, artist: payload.song.artist });
          this.report({ playback: 'playing' }, 'playing');
        },
      });
    } catch (error) {
      if (token !== this.playToken) return;
      this.playInFlight = false;
      this.handlePlayFailure(payload, error);
      return;
    }
    if (token !== this.playToken) return;
    this.playInFlight = false;
    this.current = payload;
    this.pendingPlay = null;
    window.clearTimeout(this.streamRetryTimer);
    this.errorRetryAttempts = 0;
    this.resetStreamRetries();
  }

  // Only an autoplay refusal waits for a click; anything else (404, undecodable
  // stream, disposed engine) is a stream error that retries on its own timer.
  private handlePlayFailure(payload: PlaySongPayload, error: unknown) {
    if (!isAutoplayBlocked(error)) {
      this.retryStream(payload);
      return;
    }
    this.report(
      { playback: 'blocked' },
      this.audio.allowAutoplay ? 'audio autoplay blocked' : 'click anywhere to start audio',
    );
  }

  // Called ~15s before end: request the next song URL for prebuffering (no dequeue).
  private handlePrefetch(payload: PlaySongPayload) {
    if (this.current?.history_id !== payload.history_id && this.pendingPlay?.history_id !== payload.history_id) return;
    this.queueOrSend({ type: 'peek_next' });
  }

  // Called ~3s before end: trigger the crossfade by sending song_finished early.
  // The server responds with play_song for the next track; that handler calls play()
  // which detects the pre-buffered URL on the inactive channel and starts near-instantly.
  private handleNearEnd(payload: PlaySongPayload) {
    if (this.current?.history_id !== payload.history_id && this.pendingPlay?.history_id !== payload.history_id) return;
    // awaitingSong is latched by finishCurrent, and only once it has actually
    // sent song_finished. Setting it here instead would strand the client
    // whenever finishCurrent returned early at its own guard: every recovery
    // path checks !awaitingSong, so nothing would ever request a song again.
    this.finishCurrent(payload, { awaitReplacement: true });
  }

  // After the near-end handoff `current` is already null and finishCurrent does
  // nothing; the title still has to go if no newer song has taken the screen.
  private handleEnded(payload: PlaySongPayload) {
    this.finishCurrent(payload);
    if (this.displayedHistoryId === payload.history_id) this.clearDisplayedSong();
  }

  // The title stays up: the track is still audible until it ends or the next one starts.
  private finishCurrent(payload: PlaySongPayload, options: { awaitReplacement?: boolean } = {}) {
    if (this.current?.history_id !== payload.history_id) return;
    // Set before sending so a synchronous reply cannot clear it first.
    if (options.awaitReplacement) this.startAwaitingSong();
    this.current = null;
    this.queueOrSend({
      type: 'song_finished',
      payload: {
        song_id: payload.song.id,
        history_id: payload.history_id,
      },
    });
  }

  private requestSong() {
    this.startAwaitingSong();
    this.queueOrSend({ type: 'need_song' });
  }

  // The backend may drop a reply when this client's send buffer is full; ask again.
  // While disconnected, only re-arm: onopen already re-requests a lost song, and
  // queueing here would flush one extra need_song per timeout on reconnect.
  private startAwaitingSong() {
    this.awaitingSong = true;
    window.clearTimeout(this.awaitSongTimer);
    this.awaitSongTimer = window.setTimeout(() => {
      if (!this.shouldRun || !this.awaitingSong || this.current || this.pendingPlay) return;
      if (this.socket?.readyState === WebSocket.OPEN) this.requestSong();
      else this.startAwaitingSong();
    }, awaitSongTimeoutMs);
  }

  private retryStream(payload: PlaySongPayload) {
    if (this.current?.history_id !== payload.history_id && this.pendingPlay?.history_id !== payload.history_id) return;
    window.clearTimeout(this.streamRetryTimer);
    this.current = null;
    this.clearDisplayedSong();
    if (payload.history_id === this.streamRetryHistoryId && this.streamRetryAttempts >= maxStreamRetries) {
      this.skipUnplayableStream();
      return;
    }
    this.pendingPlay = payload;
    this.report({ playback: 'error' }, 'stream error, retrying');
    // onError and a play() rejection from one attempt both land here, so the
    // attempt is counted when the timer fires, not per call.
    this.streamRetryTimer = window.setTimeout(() => {
      if (!this.shouldRun) return;
      if (payload.history_id !== this.streamRetryHistoryId) {
        this.streamRetryHistoryId = payload.history_id;
        this.streamRetryAttempts = 0;
      }
      this.streamRetryAttempts += 1;
      void this.play(payload);
    }, streamRetryMs);
  }

  // A deleted, missing or undecodable file never plays; ask the server for the next one.
  private skipUnplayableStream() {
    this.pendingPlay = null;
    this.resetStreamRetries();
    this.report({ playback: 'error' }, 'stream unplayable, skipping');
    this.requestSong();
  }

  private clearDisplayedSong() {
    this.displayedHistoryId = null;
    this.onSongChange?.(null);
  }

  private resetStreamRetries() {
    this.streamRetryHistoryId = null;
    this.streamRetryAttempts = 0;
  }

  private queueOrSend(message: SocketMessage) {
    if (this.socket?.readyState === WebSocket.OPEN) {
      this.send(message);
      return;
    }

    this.outbox.push(message);
    if (this.outbox.length > maxOutboxMessages) {
      this.outbox.splice(0, this.outbox.length - maxOutboxMessages);
    }
    // A live socket flushes the outbox on open and a pending timer reconnects;
    // forcing a reconnect here would abort the handshake or lengthen the backoff.
    if (!this.socket && !this.reconnectTimer) this.connect();
  }

  private send(message: SocketMessage) {
    if (this.socket?.readyState !== WebSocket.OPEN) return;
    this.socket.send(JSON.stringify(message));
  }

  private flushOutbox() {
    const pending = this.outbox.splice(0);
    for (const msg of pending) this.send(msg);
  }

  private reconnectNow(reason = 'radio reconnecting') {
    if (!this.shouldRun) return;
    this.report({ connection: 'reconnecting' }, reason);
    window.clearInterval(this.heartbeatTimer);
    window.clearTimeout(this.reconnectTimer);
    this.reconnectTimer = 0;
    if (this.socket) {
      // Detach first so this socket's own close/error events cannot schedule a second reconnect.
      detachSocket(this.socket);
      if (this.socket.readyState !== WebSocket.CLOSED) this.socket.close();
    }
    this.socket = null;
    this.connecting = false;
    this.scheduleReconnect();
  }

  private handleOnline = () => {
    this.reconnectNow('network online');
  };

  private handleOffline = () => {
    this.report({ connection: 'disconnected' }, 'network offline');
    window.clearInterval(this.heartbeatTimer);
  };

  dispose() {
    this.stop();
    this.outbox = [];
  }
}

function isSongRequest(message: SocketMessage): boolean {
  return message.type === 'need_song' || message.type === 'song_finished';
}

function detachSocket(socket: WebSocket) {
  socket.onopen = null;
  socket.onmessage = null;
  socket.onclose = null;
  socket.onerror = null;
}

/** Browsers reject play() with NotAllowedError under autoplay policy; AudioEngine mirrors that name. */
function isAutoplayBlocked(error: unknown): boolean {
  return error instanceof DOMException && error.name === 'NotAllowedError';
}

function parseMessage(data: unknown): SocketMessage | null {
  if (typeof data !== 'string') return null;
  try {
    const parsed = JSON.parse(data);
    if (!isSocketMessage(parsed)) return null;
    return parsed;
  } catch {
    return null;
  }
}

function isSocketMessage(value: unknown): value is SocketMessage {
  if (!isRecord(value)) return false;
  return typeof value.type === 'string' && messageTypes.has(value.type as MessageType);
}

function isPlaySongPayload(value: unknown): value is PlaySongPayload {
  if (!isRecord(value) || !isRecord(value.song)) return false;
  return (
    typeof value.song.id === 'string' &&
    typeof value.song.title === 'string' &&
    typeof value.song.filename === 'string' &&
    (value.song.artist === undefined || typeof value.song.artist === 'string') &&
    typeof value.stream_url === 'string' &&
    typeof value.history_id === 'string'
  );
}

function isErrorPayload(value: unknown): value is ErrorPayload {
  return isRecord(value) && typeof value.message === 'string';
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null;
}
