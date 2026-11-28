import type { AudioEngine } from './AudioEngine';
import { coverURL, getWebSocketURL, resolveStreamURL } from '../config/backend.ts';
import { HEARTBEAT_MS, armHandshakeTimeout, isStale, reconnectDelay } from '../config/reconnect.ts';

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
  | 'skip_now'
  | 'now_playing';

interface SocketMessage<T = unknown> {
  type: MessageType;
  payload?: T;
}

interface Song {
  id: string;
  title: string;
  artist?: string;
  filename: string;
  has_cover?: boolean;
}

interface PlaySongPayload {
  song: Song;
  stream_url: string;
  history_id: string;
}

// A start or a retag re-announce of the current song; only retags (no history_id) are used.
interface NowPlayingPayload {
  song: Song;
  history_id?: unknown;
}

// `code` is checked by parseErrorCode, so a newer or older backend still parses.
interface ErrorPayload {
  code?: unknown;
  message: string;
}

/** Why the server could not send a song. Unknown or missing codes read as 'internal'. */
export type ErrorCode = 'queue_empty' | 'library_empty' | 'internal';

interface ServerError {
  message: string;
  code: ErrorCode;
}

export interface SongInfo { title: string; artist?: string; coverUrl?: string }

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
  /** Set only while playback is 'error'. */
  errorCode?: ErrorCode;
}

interface BackendPlaybackOptions {
  audio: AudioEngine;
  onStatus?: (status: RadioStatus) => void;
  onSongChange?: (song: SongInfo | null) => void;
}

const streamRetryMs = 5000;
const maxStreamRetries = 3;
const awaitSongTimeoutMs = 15000;
const errorRetryMaxMs = 30000;
const maxOutboxMessages = 20;
// Slack past a tail's expected end before its deferred error is reported anyway.
const tailGraceMs = 2000;
// Tail assumed when the duration is unknown: AudioEngine's near-end threshold.
const unknownTailMs = 3000;
// Longest a resumed-after-pause tail can push the report past its first deadline.
const tailCeilingMs = 30000;
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
  'now_playing',
]);

export class BackendPlaybackClient {
  private audio: AudioEngine;
  private onStatus?: (status: RadioStatus) => void;
  private onSongChange?: (song: SongInfo | null) => void;
  private socket: WebSocket | null = null;
  private reconnectTimer = 0;
  private heartbeatTimer = 0;
  private cancelHandshake = () => {};
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
  private errorCode: ErrorCode | undefined;
  private playback: PlaybackStatus = 'idle';
  // Bumped by every play(); a play that settles after a newer one started is ignored.
  private playToken = 0;
  private playInFlight = false;
  // history_id of an in-flight play whose track already ended or neared its end;
  // its song_finished is sent once the play settles and becomes current.
  private finishedInFlight: string | null = null;
  // history_id of the song whose title is on screen; it can outlive `current`
  // through the near-end handoff, while the old track is still audible.
  private displayedHistoryId: string | null = null;
  // Song id of that title, matched by a retag re-announce.
  private displayedSongId: string | null = null;
  // Server error held back while a track that finished early is still audible.
  private deferredError: ServerError | null = null;
  // Reports the deferred error if the tail never fires onEnded (stalled or silent element).
  private tailFallbackTimer = 0;

  constructor(options: BackendPlaybackOptions) {
    this.audio = options.audio;
    this.onStatus = options.onStatus;
    this.onSongChange = options.onSongChange;
  }

  /** Records the latest status and notifies the listener. */
  // The error code lives as long as the 'error' playback it explains; any other
  // playback clears it, and connection-only reports keep it.
  private report(
    change: { connection?: ConnectionStatus; playback?: PlaybackStatus },
    message: string,
    errorCode: ErrorCode = 'internal',
  ) {
    if (change.connection) this.connection = change.connection;
    if (change.playback) {
      this.playback = change.playback;
      this.errorCode = change.playback === 'error' ? errorCode : undefined;
    }
    const status: RadioStatus = { connection: this.connection, playback: this.playback, message };
    if (this.errorCode) status.errorCode = this.errorCode;
    this.onStatus?.(status);
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
    this.finishedInFlight = null;
    this.pendingPlay = null;
    this.current = null;
    this.awaitingSong = false;
    this.deferredError = null;
    this.cancelTailFallback();
    this.resetStreamRetries();
    window.clearTimeout(this.reconnectTimer);
    this.reconnectTimer = 0;
    window.clearInterval(this.heartbeatTimer);
    this.cancelHandshake();
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

    let socket: WebSocket;
    try {
      socket = new WebSocket(getWebSocketURL('player'));
    } catch (error) {
      // A bad backend URL or a CSP block throws here and fires no onclose, so
      // the retry is scheduled here or the client would never connect again.
      this.socket = null;
      this.report({ connection: 'reconnecting' }, `radio connection failed: ${errorName(error)}`);
      this.scheduleReconnect();
      return;
    }
    this.connecting = true;
    this.socket = socket;
    this.lastMessageAt = Date.now();
    this.cancelHandshake = armHandshakeTimeout(socket, () => {
      if (this.socket === socket) this.reconnectNow('radio connection timed out');
    });

    socket.onopen = () => {
      this.cancelHandshake();
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
        if (isStale(this.lastMessageAt, Date.now())) {
          this.reconnectNow('radio heartbeat timeout');
          return;
        }
        this.send({ type: 'heartbeat' });
      }, HEARTBEAT_MS);
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
      this.cancelHandshake();
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
    const delay = reconnectDelay(this.reconnectAttempts);
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
        this.finishedInFlight = null;
        this.deferredError = null;
        this.cancelTailFallback();
        this.resetStreamRetries();
        window.clearTimeout(this.streamRetryTimer);
        window.clearTimeout(this.errorRetryTimer);
        window.clearTimeout(this.awaitSongTimer);
        this.awaitingSong = false;
        this.requestSong();
        break;
      case 'now_playing':
        if (isNowPlayingPayload(msg.payload) && isRetag(msg.payload)) this.applyRetag(msg.payload.song);
        break;
      case 'error': {
        if (msg.payload !== undefined && !isErrorPayload(msg.payload)) return;
        this.awaitingSong = false;
        window.clearTimeout(this.awaitSongTimer);
        const error: ServerError = { message: msg.payload?.message ?? 'radio error', code: parseErrorCode(msg.payload?.code) };
        // After an early song_finished the old track's tail is still audible; report once it ends.
        if (this.displayedHistoryId !== null && !this.current && !this.pendingPlay) this.deferError(this.displayedHistoryId, error);
        else this.report({ playback: 'error' }, error.message, error.code);
        this.scheduleErrorRetry();
        break;
      }
    }
  }

  // Backs off so an empty library does not draw a need_song every second from every player.
  private scheduleErrorRetry() {
    const delay = reconnectDelay(this.errorRetryAttempts, 900, errorRetryMaxMs);
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
    this.finishedInFlight = null;
    this.pendingPlay = payload;
    const songName = displayTitle(payload.song);

    try {
      await this.audio.playStream(resolveStreamURL(payload.stream_url), songName, {
        onEnded: () => this.handleEnded(payload),
        onError: () => this.retryStream(payload),
        onStall: () => this.retryStream(payload),
        onPrefetch: () => this.handlePrefetch(payload),
        onNearEnd: () => this.handleNearEnd(payload),
        // Show the title as soon as the track is audible, not after the crossfade.
        onStarted: () => {
          if (token !== this.playToken) return;
          this.displayedHistoryId = payload.history_id;
          this.displayedSongId = payload.song.id;
          this.deferredError = null;
          this.cancelTailFallback();
          this.onSongChange?.(songInfo(payload.song));
          this.report({ playback: 'playing' }, 'playing');
        },
      });
    } catch (error) {
      if (token !== this.playToken) return;
      this.playInFlight = false;
      this.finishedInFlight = null;
      this.handlePlayFailure(payload, error);
      return;
    }
    if (token !== this.playToken) return;
    this.playInFlight = false;
    this.current = payload;
    this.pendingPlay = null;
    window.clearTimeout(this.streamRetryTimer);
    this.errorRetryAttempts = 0;
    // A restart of the same song keeps its count, so a deterministic mid-file
    // error is still skipped instead of replaying the same seconds forever.
    if (this.streamRetryHistoryId !== payload.history_id) this.resetStreamRetries();
    if (this.finishedInFlight === payload.history_id) {
      this.finishedInFlight = null;
      this.finishCurrent(payload);
    }
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
    this.finishCurrent(payload);
  }

  // A track can end with no near-end handoff (unknown duration, missed
  // timeupdate); finishCurrent then sends song_finished and awaits the reply.
  // After the handoff `current` is already null and finishCurrent does nothing;
  // the title still has to go if no newer song has taken the screen.
  private handleEnded(payload: PlaySongPayload) {
    this.finishCurrent(payload);
    if (this.displayedHistoryId !== payload.history_id) return;
    this.finishTail();
  }

  // The audible tail is over: drop its title and report an error held back for it.
  private finishTail() {
    this.cancelTailFallback();
    this.clearDisplayedSong();
    const deferred = this.deferredError;
    this.deferredError = null;
    if (deferred !== null && !this.current && !this.pendingPlay) this.report({ playback: 'error' }, deferred.message, deferred.code);
  }

  // A stalled or broken tail never fires onEnded, so the error is also reported
  // shortly after the tail's expected end. An armed timer already belongs to this
  // tail (onStarted cancels it), so it is kept and its ceiling still holds.
  private deferError(historyId: string, error: ServerError) {
    this.deferredError = error;
    if (this.tailFallbackTimer) return;
    const delay = this.tailRemainingMs() + tailGraceMs;
    this.scheduleTailCheck(historyId, Date.now() + delay + tailCeilingMs, delay);
  }

  private scheduleTailCheck(historyId: string, ceiling: number, delay: number) {
    window.clearTimeout(this.tailFallbackTimer);
    this.tailFallbackTimer = window.setTimeout(() => this.checkTail(historyId, ceiling), delay);
  }

  // Still audible with time left means the user paused and resumed: wait for the
  // new end, but never past the ceiling.
  private checkTail(historyId: string, ceiling: number) {
    this.tailFallbackTimer = 0;
    const remainingMs = this.tailRemainingMs();
    const untilCeiling = ceiling - Date.now();
    if (this.audio.audible && remainingMs > 250 && untilCeiling > 0) {
      this.scheduleTailCheck(historyId, ceiling, Math.min(remainingMs + tailGraceMs, untilCeiling));
      return;
    }
    if (this.displayedHistoryId === historyId) this.finishTail();
  }

  private tailRemainingMs(): number {
    const progress = this.audio.getPlaybackProgress();
    if (!progress || !Number.isFinite(progress.duration) || progress.duration <= 0) return unknownTailMs;
    return Math.max(0, progress.duration - progress.currentTime) * 1000;
  }

  private cancelTailFallback() {
    window.clearTimeout(this.tailFallbackTimer);
    this.tailFallbackTimer = 0;
  }

  // The server answers song_finished with play_song, like need_song, so the
  // reply is awaited and re-requested on timeout. The title stays up: the track
  // is still audible until it ends or the next one starts.
  private finishCurrent(payload: PlaySongPayload) {
    // A track shorter than the crossfade ends before play() settles and sets
    // `current`; play() sends the finish when it settles.
    if (
      this.current?.history_id !== payload.history_id &&
      this.playInFlight &&
      this.pendingPlay?.history_id === payload.history_id
    ) {
      this.finishedInFlight = payload.history_id;
      return;
    }
    if (this.current?.history_id !== payload.history_id) return;
    // Set before sending so a synchronous reply cannot clear it first.
    this.startAwaitingSong();
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
    if (this.current?.history_id !== payload.history_id && this.pendingPlay?.history_id !== payload.history_id) {
      // The tail of a song already handed off stalled or broke: it is over, not retried.
      if (payload.history_id === this.displayedHistoryId && !this.current && !this.pendingPlay) this.finishTail();
      return;
    }
    window.clearTimeout(this.streamRetryTimer);
    // An error during the fade-in arrives before play() settles; void that play
    // so its late settle cannot cancel this retry and make the dead track current.
    if (this.playInFlight && this.pendingPlay?.history_id === payload.history_id) {
      this.playToken += 1;
      this.playInFlight = false;
      this.finishedInFlight = null;
    }
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

  // A retag of the song on screen shows its new tags at once, and a later
  // retry or restart of it keeps them. Playback state is left alone.
  private applyRetag(song: Song) {
    if (this.displayedSongId === null || song.id !== this.displayedSongId) return;
    this.onSongChange?.(songInfo(song));
    for (const play of [this.current, this.pendingPlay]) {
      if (play?.song.id === song.id) play.song = song;
    }
  }

  private clearDisplayedSong() {
    this.displayedHistoryId = null;
    this.displayedSongId = null;
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
    this.cancelHandshake();
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

  // An open socket can outlive a network flap (e.g. a localhost backend), so
  // online/offline only act when the socket is not open. The heartbeat
  // timeout still catches an open socket that really died.
  private handleOnline = () => {
    if (this.socket?.readyState === WebSocket.OPEN) return;
    this.reconnectNow('network online');
  };

  private handleOffline = () => {
    if (this.socket?.readyState === WebSocket.OPEN) return;
    this.report({ connection: 'disconnected' }, 'network offline');
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

function errorName(error: unknown): string {
  return error instanceof Error ? error.name : String(error);
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

function displayTitle(song: Song): string {
  return song.title || song.filename;
}

function songInfo(song: Song): SongInfo {
  return {
    title: displayTitle(song),
    artist: song.artist,
    coverUrl: song.has_cover === true ? coverURL(song.id) : undefined,
  };
}

function isSong(value: unknown): value is Song {
  return (
    isRecord(value) &&
    typeof value.id === 'string' &&
    typeof value.title === 'string' &&
    typeof value.filename === 'string' &&
    (value.artist === undefined || typeof value.artist === 'string')
  );
}

function isPlaySongPayload(value: unknown): value is PlaySongPayload {
  return isRecord(value) && isSong(value.song) && typeof value.stream_url === 'string' && typeof value.history_id === 'string';
}

function isNowPlayingPayload(value: unknown): value is NowPlayingPayload {
  return isRecord(value) && isSong(value.song);
}

/** A now_playing without a history_id re-announces a retag; real starts arrive via play_song. */
function isRetag(payload: NowPlayingPayload): boolean {
  return typeof payload.history_id !== 'string' || payload.history_id === '';
}

function isErrorPayload(value: unknown): value is ErrorPayload {
  return isRecord(value) && typeof value.message === 'string';
}

function parseErrorCode(value: unknown): ErrorCode {
  return value === 'queue_empty' || value === 'library_empty' ? value : 'internal';
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null;
}
