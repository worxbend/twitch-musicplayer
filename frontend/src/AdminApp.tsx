import { createSignal, createEffect, onMount, onCleanup, splitProps, For, Show, type JSX } from 'solid-js';
import { getBackendURL, getWebSocketURL } from './config/backend';
import { reconnectDelay, HEARTBEAT_MS, isStale } from './config/reconnect';
import { createLatest } from './admin/latestRequest';
import { HttpError, errorText, fetchWithTimeout, REQUEST_TIMEOUT_MS } from './admin/request';
import { TerminalHeader } from './components/TerminalHeader';

interface Song {
  id: string; filename: string; title: string; artist: string; album: string;
  duration_secs: number; size_bytes: number;
}
interface QueueItem {
  id: string; song_id: string; song?: Song; position: number;
  source: 'auto' | 'manual'; added_at: string;
}
interface HistoryItem { song: Song; started_at: string; }
function formatDuration(secs: number): string {
  if (!secs || secs <= 0) return '--:--';
  return `${Math.floor(secs / 60)}:${Math.floor(secs % 60).toString().padStart(2, '0')}`;
}
function songLabel(song: Song) { return song.title || song.filename; }
function queueItemLabel(item: QueueItem) { return item.song ? songLabel(item.song) : item.song_id; }

// fetchQueue replaces every row, which drops keyboard focus. These keep the
// focused queue control (by item id and action) focused across the re-render.
// When its row is gone (removed), focus moves to the row now in that slot,
// or to the queue heading once the queue is empty.
type QueueControl = { id: string; action: string; index: number };
function focusedQueueControl(items: QueueItem[]): QueueControl | null {
  const el = document.activeElement;
  if (!(el instanceof HTMLElement) || !el.closest('.queue-list')) return null;
  const { queueId, queueAction } = el.dataset;
  if (!queueId || !queueAction) return null;
  return { id: queueId, action: queueAction, index: items.findIndex(item => item.id === queueId) };
}
function restoreQueueFocus(control: QueueControl | null, items: QueueItem[]) {
  if (!control) return;
  const same = document.querySelector<HTMLElement>(`.queue-list [data-queue-id="${CSS.escape(control.id)}"][data-queue-action="${CSS.escape(control.action)}"]`);
  if (same) { same.focus(); return; }
  if (!items.length) { document.getElementById('queue-heading')?.focus(); return; }
  const slot = items[Math.min(Math.max(control.index, 0), items.length - 1)];
  document.querySelector<HTMLElement>(`.queue-list [data-queue-id="${CSS.escape(slot.id)}"][data-queue-action="remove"]`)?.focus();
}
// aria-disabled, not disabled: a natively disabled button drops keyboard
// focus to <body> when it becomes disabled under the cursor.
type ActionButtonProps = Omit<JSX.ButtonHTMLAttributes<HTMLButtonElement>, 'type' | 'disabled' | 'onClick'> & {
  inactive: () => boolean;
  onClick: () => void;
};
function ActionButton(props: ActionButtonProps) {
  const [local, rest] = splitProps(props, ['inactive', 'onClick']);
  return <button {...rest} type="button" aria-disabled={local.inactive() ? 'true' : undefined} onClick={() => { if (!local.inactive()) local.onClick(); }} />;
}
function formatClock(iso: string) { return new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }); }

export function AdminApp() {
  const backendURL = getBackendURL();
  const [songs, setSongs] = createSignal<Song[]>([]);
  const [queue, setQueue] = createSignal<QueueItem[]>([]);
  const [history, setHistory] = createSignal<HistoryItem[]>([]);
  const [currentSong, setCurrentSong] = createSignal<Song | null>(null);
  const [connected, setConnected] = createSignal(false);
  // A fresh id per call makes Solid insert a new node, so a repeated message is re-announced.
  const [notice, setNotice] = createSignal<{ id: number; text: string; error: boolean } | null>(null);
  const [searchQuery, setSearchQuery] = createSignal('');
  const [loading, setLoading] = createSignal(true);
  const [libraryError, setLibraryError] = createSignal(false);
  const [queueError, setQueueError] = createSignal(false);
  const [historyError, setHistoryError] = createSignal(false);
  const [playerError, setPlayerError] = createSignal(false);
  const [busy, setBusy] = createSignal('');
  let notifTimer = 0;
  let noticeSeq = 0;
  let searchTimer = 0;
  let searchRequest = 0;
  const queueGate = createLatest();
  const historyGate = createLatest();
  const playerGate = createLatest();
  let disposed = false;
  let clearDialog!: HTMLDialogElement;
  let searchInput!: HTMLInputElement;

  // The deadline covers the body too: read and the error text run inside it.
  function request<T>(path: string, read: (res: Response) => Promise<T>, init?: RequestInit) {
    // The wrapper keeps fetch bound to window (a bare reference throws Illegal invocation).
    return fetchWithTimeout((...args) => fetch(...args), `${backendURL}${path}`, init, REQUEST_TIMEOUT_MS, async res => {
      if (!res.ok) throw new HttpError(res.status, await errorText(res));
      return read(res);
    });
  }
  const readJSON = (res: Response) => res.json();
  async function fetchSongs(q = searchQuery()) {
    const id = ++searchRequest;
    setLoading(true);
    try {
      const data = await request(`/v1/songs${q ? `?q=${encodeURIComponent(q)}` : ''}`, readJSON);
      if (id === searchRequest && !disposed) { setSongs(data.songs ?? []); setLibraryError(false); }
    } catch { if (id === searchRequest && !disposed) setLibraryError(true); }
    finally { if (id === searchRequest && !disposed) setLoading(false); }
  }
  async function fetchQueue() {
    const id = queueGate.begin();
    try {
      const data = await request('/v1/queue', readJSON);
      if (queueGate.isLatest(id) && !disposed) {
        const focused = focusedQueueControl(queue());
        setQueue(data.items ?? []);
        setQueueError(false);
        restoreQueueFocus(focused, queue());
      }
    } catch { if (queueGate.isLatest(id) && !disposed) setQueueError(true); }
  }
  async function fetchHistory() {
    const id = historyGate.begin();
    try {
      const data = await request('/v1/history?limit=10', readJSON);
      if (historyGate.isLatest(id) && !disposed) { setHistory(data.items ?? []); setHistoryError(false); }
    } catch { if (historyGate.isLatest(id) && !disposed) setHistoryError(true); }
  }
  // The newest entry is the track on air; the now-playing bar already shows it.
  function pastHistory() {
    const items = history();
    return items.length && items[0].song.id === currentSong()?.id ? items.slice(1) : items;
  }
  async function fetchPlayerState() {
    const id = playerGate.begin();
    try {
      const data = await request('/v1/player', readJSON);
      if (playerGate.isLatest(id) && !disposed) { setCurrentSong(data.current_song ?? null); setPlayerError(false); }
    } catch { if (playerGate.isLatest(id) && !disposed) setPlayerError(true); }
  }
  function showNotification(message: string, error = false) {
    window.clearTimeout(notifTimer);
    setNotice({ id: ++noticeSeq, text: message, error });
    notifTimer = window.setTimeout(() => setNotice(null), 4500);
  }
  async function action(path: string, message: string, method = 'POST', body?: Record<string, unknown>) {
    if (busy()) return;
    setBusy(path);
    try {
      // Drain the body so the connection is released.
      await request(path, res => res.text(), {
        method,
        ...(body ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {}),
      });
      await Promise.all([fetchQueue(), fetchPlayerState()]);
      showNotification(`✓ ${message}`);
    } catch (err) {
      // A 4xx carries the backend's reason (e.g. 409 "queue is empty"); network errors and 5xx do not.
      const clientError = err instanceof HttpError && err.status >= 400 && err.status < 500 && err.message;
      const timedOut = err instanceof DOMException && err.name === 'TimeoutError';
      showNotification(
        clientError ? `Action failed: ${err.message}.`
          : timedOut ? 'Action timed out. The backend did not respond.'
          : 'Action failed. Check the backend connection and try again.',
        true,
      );
    }
    finally { setBusy(''); }
  }

  // A retry swaps its button out on success or on the next loading state; the
  // stable panel heading keeps keyboard focus off <body> either way.
  function retry(headingId: string, refetch: () => Promise<void>) {
    document.getElementById(headingId)?.focus();
    void refetch();
  }

  function MoveButton(props: { item: QueueItem; direction: 'up' | 'down'; blocked: boolean }) {
    return <ActionButton class="btn btn-ghost btn-icon" aria-label={`Move ${queueItemLabel(props.item)} ${props.direction}`} title={`Move ${props.direction}`} data-queue-id={props.item.id} data-queue-action={props.direction} inactive={() => props.blocked || !!busy()} onClick={() => void action(`/v1/queue/${props.item.id}:move`, 'Queue order updated.', 'POST', { direction: props.direction })}><span aria-hidden="true">{props.direction === 'up' ? '↑' : '↓'}</span></ActionButton>;
  }

  let socket: WebSocket | null = null;
  let heartbeatInterval = 0;
  let reconnectTimeout = 0;
  let reconnectAttempts = 0;
  let lastMessageAt = 0;
  function dropSocket() {
    window.clearInterval(heartbeatInterval);
    if (socket) {
      socket.onopen = socket.onmessage = socket.onclose = socket.onerror = null;
      socket.close();
      socket = null;
    }
    setConnected(false);
  }
  function scheduleReconnect() {
    window.clearTimeout(reconnectTimeout);
    reconnectTimeout = window.setTimeout(connectWS, reconnectDelay(reconnectAttempts++));
  }
  function connectWS() {
    if (disposed || (socket && socket.readyState <= WebSocket.OPEN)) return;
    let ws: WebSocket;
    try {
      ws = new WebSocket(getWebSocketURL());
    } catch {
      // A bad backend URL or a CSP block throws instead of firing onclose.
      dropSocket();
      scheduleReconnect();
      return;
    }
    socket = ws;
    ws.onopen = () => {
      reconnectAttempts = 0;
      lastMessageAt = Date.now();
      setConnected(true);
      void fetchQueue(); void fetchPlayerState(); void fetchHistory(); void fetchSongs();
      window.clearInterval(heartbeatInterval);
      heartbeatInterval = window.setInterval(() => {
        // Half-open socket: no heartbeat_ack or update for too long.
        if (isStale(lastMessageAt, Date.now())) { dropSocket(); scheduleReconnect(); return; }
        if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'heartbeat' }));
      }, HEARTBEAT_MS);
    };
    ws.onmessage = e => {
      lastMessageAt = Date.now();
      try {
        const msg = JSON.parse(e.data) as { type: string; payload?: { song?: Song; current_song?: Song } };
        if (msg.type === 'queue_updated') void fetchQueue();
        // A pushed song is newer than any /v1/player reply still in flight.
        if (msg.type === 'now_playing') { playerGate.begin(); setCurrentSong(msg.payload?.song ?? null); void fetchQueue(); void fetchHistory(); }
        if (msg.type === 'state') { playerGate.begin(); setCurrentSong(msg.payload?.current_song ?? null); void fetchQueue(); void fetchHistory(); }
      } catch { /* Ignore malformed protocol messages. */ }
    };
    ws.onclose = () => {
      dropSocket();
      if (!disposed) scheduleReconnect();
    };
    ws.onerror = () => ws.close();
  }
  createEffect(() => {
    const q = searchQuery();
    window.clearTimeout(searchTimer);
    // Invalidate an in-flight result as soon as the query changes.
    searchRequest++;
    searchTimer = window.setTimeout(() => void fetchSongs(q), 280);
  });
  onMount(() => {
    void fetchQueue(); void fetchPlayerState(); void fetchHistory(); connectWS();
  });
  onCleanup(() => {
    disposed = true;
    window.clearInterval(heartbeatInterval);
    window.clearTimeout(reconnectTimeout);
    window.clearTimeout(notifTimer);
    window.clearTimeout(searchTimer);
    dropSocket();
  });

  return (
    <div class="admin-shell">
      <TerminalHeader active="console" />
      <main id="main" class="admin-content" tabIndex={-1}>
        <header class="console-intro">
          <div><p class="console-caption">The broadcast studio</p><h1>Station control</h1><p>Keep the music moving. You’re at the controls.</p></div>
          <div class="connection-box"><span class="status" classList={{'status--live': connected()}}><span class="status-dot" />{connected() ? 'Backend connected' : 'Reconnecting to backend'}</span><span>Live library &amp; queue sync</span></div>
        </header>
        <section class="now-playing-bar" aria-labelledby="now-playing-heading">
          <div class="broadcast-mark" aria-hidden="true"><span /></div>
          <div class="now-playing-info">
            <h2 id="now-playing-heading" class="now-playing-label">{currentSong() ? 'Now playing / current track' : 'Now playing / standby'}</h2>
            <Show when={currentSong()} fallback={<><div class="now-playing-title">{playerError() ? 'Player status unavailable' : 'Waiting for a listener'}</div><div class="now-playing-artist">{playerError() ? 'Check the backend connection.' : 'Open the player to start the station.'}</div></>}>
              {song => <><div class="now-playing-title">{songLabel(song())}</div><div class="now-playing-artist">{song().artist || 'Unknown artist'}<span class="now-duration">{formatDuration(song().duration_secs)}</span></div></>}
            </Show>
          </div>
          <a class="btn" href="/">Open player ↗</a>
          <ActionButton class="btn btn-warning" inactive={() => !!busy() || !currentSong()} onClick={() => void action('/v1/player:skip', 'Skip requested. The player will advance.')}><span aria-hidden="true">≫</span>{busy() === '/v1/player:skip' ? 'Skipping…' : 'Skip track'}</ActionButton>
        </section>
        <div class="admin-main">
          <section class="song-panel" aria-labelledby="library-heading">
            <div class="panel-header"><h2 id="library-heading" class="panel-title" tabIndex={-1}>Music library</h2><span class="panel-meta">{songs().length} {searchQuery() ? 'matches' : 'tracks'}</span></div>
            <div class="search-row"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" aria-hidden="true"><circle cx="10" cy="10" r="6" stroke="currentColor" stroke-width="1.5" /><path d="m15 15 5 5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg><input ref={searchInput} class="search-box" aria-label="Search music library" type="search" placeholder="Search title, artist, album…" value={searchQuery()} onInput={e => setSearchQuery(e.currentTarget.value)} /><Show when={searchQuery()}><button class="btn" onClick={() => { setSearchQuery(''); searchInput.focus(); }}>Reset</button></Show></div>
            <div class="list-column-labels" aria-hidden="true"><span>Track / artist / album</span><span>Time</span><span>Actions</span></div>
            <div class="song-list" aria-busy={loading()}>
              <Show when={!loading()} fallback={<div class="empty-state"><span class="empty-symbol empty-symbol--loading" aria-hidden="true">◌</span><h3>Reading your library</h3><p>Fetching tracks from the backend.</p></div>}>
                <Show when={!libraryError()} fallback={<div class="empty-state"><span class="empty-symbol empty-symbol--error" aria-hidden="true">!</span><h3>Library unavailable</h3><p>Check the backend connection, then try again.</p><button class="btn" onClick={() => retry('library-heading', () => fetchSongs())}>Retry connection</button></div>}>
                  <For each={songs()} fallback={<div class="empty-state"><span class="empty-symbol" aria-hidden="true">♫</span><h3>{searchQuery() ? 'No matching tracks' : 'Your library is quiet'}</h3><p>{searchQuery() ? 'Try a different title, artist, or album.' : 'Add MP3 files to your configured music folder.'}</p></div>}>
                    {(song, idx) => <div class="song-item" classList={{'song-item--playing': currentSong()?.id === song.id}}>
                      <span class="song-index" aria-hidden="true">{currentSong()?.id === song.id ? '♫' : (idx() + 1).toString().padStart(2, '0')}</span>
                      <div class="song-info"><div class="song-title" title={songLabel(song)}>{songLabel(song)}</div><div class="song-meta">{[song.artist, song.album].filter(Boolean).join(' / ') || 'Unknown artist'}</div></div>
                      <span class="song-duration">{formatDuration(song.duration_secs)}</span>
                      <div class="song-actions"><ActionButton class="btn btn-ghost btn-icon" title="Add to queue" aria-label={`Add ${songLabel(song)} to queue`} inactive={() => !!busy()} onClick={() => void action('/v1/queue', `Queued: ${songLabel(song)}`, 'POST', { song_id: song.id })}>+</ActionButton><ActionButton class="btn btn-primary btn-icon" title="Play now" aria-label={`Play ${songLabel(song)} now`} inactive={() => !!busy()} onClick={() => void action('/v1/queue:play-next', `Play requested: ${songLabel(song)}`, 'POST', { song_id: song.id })}>▶</ActionButton></div>
                    </div>}
                  </For>
                </Show>
              </Show>
            </div>
            <div class="panel-footnote"><span class="key-hint">+</span> queue it <span class="key-hint">▶</span> play it now</div>
          </section>
          <section class="queue-panel" aria-labelledby="queue-heading">
            <div class="panel-header"><h2 id="queue-heading" class="panel-title" tabIndex={-1}>Up next <span class="queue-count">{queue().length}</span></h2><ActionButton class="btn btn-danger" inactive={() => !!busy() || !queue().length} onClick={() => clearDialog.showModal()}>Clear queue</ActionButton></div>
            <div class="queue-legend"><span><i class="queue-source manual" />manual</span><span><i class="queue-source auto" />auto rotation</span></div>
            <div class="queue-list">
              <Show when={!queueError()} fallback={<div class="empty-state"><h3>Queue unavailable</h3><p>Live updates will resume when the backend reconnects.</p><button class="btn" onClick={() => retry('queue-heading', fetchQueue)}>Retry</button></div>}>
                <For each={queue()} fallback={<div class="empty-state"><span class="empty-symbol" aria-hidden="true">+</span><h3>Make the next move</h3><p>Add a track from the library. Automatic rotation keeps things moving when the player is listening.</p></div>}>
                  {(item, idx) => <div class="queue-item"><span class="queue-pos">{(idx() + 1).toString().padStart(2, '0')}</span><span class={`queue-source ${item.source}`} role="img" aria-label={`${item.source} selection`} /><div class="queue-info"><div class="queue-title">{queueItemLabel(item)}</div><div class="queue-artist">{item.song?.artist || 'Unknown artist'}</div></div><MoveButton item={item} direction="up" blocked={idx() === 0} /><MoveButton item={item} direction="down" blocked={idx() === queue().length - 1} /><ActionButton class="btn btn-danger btn-icon" aria-label={`Remove ${queueItemLabel(item)} from queue`} title="Remove from queue" inactive={() => !!busy()} data-queue-id={item.id} data-queue-action="remove" onClick={() => void action(`/v1/queue/${item.id}`, 'Track removed from queue.', 'DELETE')}>×</ActionButton></div>}
                </For>
              </Show>
            </div>
            <div class="panel-footnote">Queue changes sync with every listener.</div>
          </section>
        </div>
        <section class="history-panel" aria-labelledby="history-heading">
          <div class="panel-header"><h2 id="history-heading" class="panel-title" tabIndex={-1}>Recently played</h2></div>
          <Show when={!historyError()} fallback={<div class="empty-state"><h3>History unavailable</h3><p>Live updates will resume when the backend reconnects.</p><button class="btn" onClick={() => retry('history-heading', fetchHistory)}>Retry</button></div>}>
            <Show when={pastHistory().length} fallback={<div class="empty-state"><p>No songs played yet.</p></div>}>
              <ol class="history-list">
                <For each={pastHistory()}>
                  {entry => <li class="history-item"><div class="queue-info"><div class="queue-title">{songLabel(entry.song)}</div><div class="queue-artist">{entry.song.artist || 'Unknown artist'}</div></div><time class="history-time" datetime={entry.started_at}>{formatClock(entry.started_at)}</time></li>}
                </For>
              </ol>
            </Show>
          </Show>
        </section>
        <footer class="terminal-footer"><span>Your station. Your selection.</span><span>{busy() ? 'Executing request…' : 'All controls in sync with your station.'}</span></footer>
      </main>
      <dialog ref={clearDialog!} class="terminal-dialog" aria-labelledby="clear-title" aria-describedby="clear-description">
        <form method="dialog"><h2 id="clear-title">Clear the queue?</h2><p id="clear-description">Remove all upcoming tracks. The current track keeps playing. Automatic rotation may refill the queue.</p><div class="dialog-actions"><button class="btn" autofocus>Keep queue</button><button class="btn btn-danger" onClick={() => void action('/v1/queue:clear', 'Queue cleared.')}>Clear queue</button></div></form>
      </dialog>
      <div role="status" aria-live="polite"><Show when={notice()} keyed>{n => <div class="notification" classList={{'notification--error': n.error}}>{n.text}</div>}</Show></div>
    </div>
  );
}
