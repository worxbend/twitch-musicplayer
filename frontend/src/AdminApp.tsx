import { createSignal, createEffect, onMount, onCleanup, For, Show } from 'solid-js';
import { getBackendURL, getWebSocketURL } from './config/backend';
import { TerminalHeader } from './components/TerminalHeader';

interface Song {
  id: string; filename: string; title: string; artist: string; album: string;
  duration_secs: number; size_bytes: number;
}
interface QueueItem {
  id: string; song_id: string; song?: Song; position: number;
  source: 'auto' | 'manual'; added_at: string;
}
function formatDuration(secs: number): string {
  if (!secs || secs <= 0) return '--:--';
  return `${Math.floor(secs / 60)}:${Math.floor(secs % 60).toString().padStart(2, '0')}`;
}
function songLabel(song: Song) { return song.title || song.filename; }

export function AdminApp() {
  const backendURL = getBackendURL();
  const [songs, setSongs] = createSignal<Song[]>([]);
  const [queue, setQueue] = createSignal<QueueItem[]>([]);
  const [currentSong, setCurrentSong] = createSignal<Song | null>(null);
  const [connected, setConnected] = createSignal(false);
  const [notification, setNotification] = createSignal('');
  const [notificationError, setNotificationError] = createSignal(false);
  const [searchQuery, setSearchQuery] = createSignal('');
  const [loading, setLoading] = createSignal(true);
  const [libraryError, setLibraryError] = createSignal(false);
  const [queueError, setQueueError] = createSignal(false);
  const [playerError, setPlayerError] = createSignal(false);
  const [busy, setBusy] = createSignal('');
  let notifTimer = 0;
  let searchTimer = 0;
  let searchRequest = 0;
  let disposed = false;
  let clearDialog!: HTMLDialogElement;

  async function request(path: string, init?: RequestInit) {
    const res = await fetch(`${backendURL}${path}`, init);
    if (!res.ok) throw new Error(`Request failed (${res.status})`);
    return res;
  }
  async function fetchSongs(q = searchQuery()) {
    const id = ++searchRequest;
    setLoading(true);
    try {
      const data = await (await request(`/v1/songs${q ? `?q=${encodeURIComponent(q)}` : ''}`)).json();
      if (id === searchRequest && !disposed) { setSongs(data.songs ?? []); setLibraryError(false); }
    } catch { if (id === searchRequest && !disposed) setLibraryError(true); }
    finally { if (id === searchRequest && !disposed) setLoading(false); }
  }
  async function fetchQueue() {
    try {
      const data = await (await request('/v1/queue')).json();
      if (!disposed) { setQueue(data.items ?? []); setQueueError(false); }
    } catch { if (!disposed) setQueueError(true); }
  }
  async function fetchPlayerState() {
    try {
      const data = await (await request('/v1/player')).json();
      if (!disposed) { setCurrentSong(data.current_song ?? null); setPlayerError(false); }
    } catch { if (!disposed) setPlayerError(true); }
  }
  function showNotification(message: string, error = false) {
    window.clearTimeout(notifTimer);
    setNotificationError(error);
    setNotification(message);
    notifTimer = window.setTimeout(() => setNotification(''), 4500);
  }
  async function action(path: string, message: string, method = 'POST', songId?: string) {
    if (busy()) return;
    setBusy(path);
    try {
      await request(path, {
        method,
        ...(songId ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ song_id: songId }) } : {}),
      });
      await Promise.all([fetchQueue(), fetchPlayerState()]);
      showNotification(`✓ ${message}`);
    } catch { showNotification('Action failed. Check the backend connection and try again.', true); }
    finally { setBusy(''); }
  }

  let socket: WebSocket | null = null;
  let heartbeatInterval = 0;
  let reconnectTimeout = 0;
  function connectWS() {
    if (disposed || (socket && socket.readyState <= WebSocket.OPEN)) return;
    socket = new WebSocket(getWebSocketURL());
    socket.onopen = () => {
      setConnected(true);
      void fetchQueue(); void fetchPlayerState();
      window.clearInterval(heartbeatInterval);
      heartbeatInterval = window.setInterval(() => {
        if (socket?.readyState === WebSocket.OPEN) socket.send(JSON.stringify({ type: 'heartbeat' }));
      }, 25000);
    };
    socket.onmessage = e => {
      try {
        const msg = JSON.parse(e.data) as { type: string; payload?: { song?: Song; current_song?: Song } };
        if (msg.type === 'queue_updated') void fetchQueue();
        if (msg.type === 'now_playing') { setCurrentSong(msg.payload?.song ?? null); void fetchQueue(); }
        if (msg.type === 'state') { setCurrentSong(msg.payload?.current_song ?? null); void fetchQueue(); }
      } catch { /* Ignore malformed protocol messages. */ }
    };
    socket.onclose = () => {
      setConnected(false);
      window.clearInterval(heartbeatInterval);
      if (!disposed) reconnectTimeout = window.setTimeout(connectWS, 3000);
    };
    socket.onerror = () => socket?.close();
  }
  createEffect(() => {
    const q = searchQuery();
    window.clearTimeout(searchTimer);
    // Invalidate an in-flight result as soon as the query changes.
    searchRequest++;
    searchTimer = window.setTimeout(() => void fetchSongs(q), 280);
  });
  onMount(() => {
    void fetchQueue(); void fetchPlayerState(); connectWS();
  });
  onCleanup(() => {
    disposed = true;
    window.clearInterval(heartbeatInterval);
    window.clearTimeout(reconnectTimeout);
    window.clearTimeout(notifTimer);
    window.clearTimeout(searchTimer);
    if (socket) { socket.onclose = null; socket.close(); }
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
          <button class="btn btn-warning" disabled={!!busy() || !currentSong()} onClick={() => void action('/v1/player:skip', 'Skip requested. The player will advance.')}><span aria-hidden="true">≫</span>{busy() === '/v1/player:skip' ? 'Skipping…' : 'Skip track'}</button>
        </section>
        <div class="admin-main">
          <section class="song-panel" aria-labelledby="library-heading">
            <div class="panel-header"><h2 id="library-heading" class="panel-title">Music library</h2><span class="panel-meta">{songs().length} {searchQuery() ? 'matches' : 'tracks'}</span></div>
            <div class="search-row"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" aria-hidden="true"><circle cx="10" cy="10" r="6" stroke="currentColor" stroke-width="1.5" /><path d="m15 15 5 5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg><input class="search-box" aria-label="Search music library" type="search" placeholder="Search title, artist, album…" value={searchQuery()} onInput={e => setSearchQuery(e.currentTarget.value)} /><Show when={searchQuery()}><button class="btn" onClick={() => setSearchQuery('')}>Reset</button></Show></div>
            <div class="list-column-labels" aria-hidden="true"><span>Track / artist / album</span><span>Time</span><span>Actions</span></div>
            <div class="song-list" aria-busy={loading()}>
              <Show when={!loading()} fallback={<div class="empty-state"><span class="empty-symbol empty-symbol--loading" aria-hidden="true">◌</span><h3>Reading your library</h3><p>Fetching tracks from the backend.</p></div>}>
                <Show when={!libraryError()} fallback={<div class="empty-state"><span class="empty-symbol empty-symbol--error" aria-hidden="true">!</span><h3>Library unavailable</h3><p>Check the backend connection, then try again.</p><button class="btn" onClick={() => void fetchSongs()}>Retry connection</button></div>}>
                  <For each={songs()} fallback={<div class="empty-state"><span class="empty-symbol" aria-hidden="true">♫</span><h3>{searchQuery() ? 'No matching tracks' : 'Your library is quiet'}</h3><p>{searchQuery() ? 'Try a different title, artist, or album.' : 'Add MP3 files to your configured music folder.'}</p></div>}>
                    {(song, idx) => <div class="song-item" classList={{'song-item--playing': currentSong()?.id === song.id}}>
                      <span class="song-index" aria-hidden="true">{currentSong()?.id === song.id ? '♫' : (idx() + 1).toString().padStart(2, '0')}</span>
                      <div class="song-info"><div class="song-title" title={songLabel(song)}>{songLabel(song)}</div><div class="song-meta">{[song.artist, song.album].filter(Boolean).join(' / ') || 'Unknown artist'}</div></div>
                      <span class="song-duration">{formatDuration(song.duration_secs)}</span>
                      <div class="song-actions"><button class="btn btn-ghost btn-icon" title="Add to queue" aria-label={`Add ${songLabel(song)} to queue`} disabled={!!busy()} onClick={() => void action('/v1/queue', `Queued: ${songLabel(song)}`, 'POST', song.id)}>+</button><button class="btn btn-primary btn-icon" title="Play now" aria-label={`Play ${songLabel(song)} now`} disabled={!!busy()} onClick={() => void action('/v1/queue:play-next', `Play requested: ${songLabel(song)}`, 'POST', song.id)}>▶</button></div>
                    </div>}
                  </For>
                </Show>
              </Show>
            </div>
            <div class="panel-footnote"><span class="key-hint">+</span> queue it <span class="key-hint">▶</span> play it now</div>
          </section>
          <section class="queue-panel" aria-labelledby="queue-heading">
            <div class="panel-header"><h2 id="queue-heading" class="panel-title">Up next <span class="queue-count">{queue().length}</span></h2><button class="btn btn-danger" disabled={!!busy() || !queue().length} onClick={() => clearDialog.showModal()}>Clear queue</button></div>
            <div class="queue-legend"><span><i class="queue-source manual" />manual</span><span><i class="queue-source auto" />auto rotation</span></div>
            <div class="queue-list">
              <Show when={!queueError()} fallback={<div class="empty-state"><h3>Queue unavailable</h3><p>Live updates will resume when the backend reconnects.</p><button class="btn" onClick={() => void fetchQueue()}>Retry</button></div>}>
                <For each={queue()} fallback={<div class="empty-state"><span class="empty-symbol" aria-hidden="true">+</span><h3>Make the next move</h3><p>Add a track from the library. Automatic rotation keeps things moving when the player is listening.</p></div>}>
                  {(item, idx) => <div class="queue-item"><span class="queue-pos">{(idx() + 1).toString().padStart(2, '0')}</span><span class={`queue-source ${item.source}`} role="img" aria-label={`${item.source} selection`} /><div class="queue-info"><div class="queue-title">{item.song?.title || item.song?.filename || item.song_id}</div><div class="queue-artist">{item.song?.artist || 'Unknown artist'}</div></div><button class="btn btn-danger btn-icon" aria-label={`Remove ${item.song?.title || 'track'} from queue`} title="Remove from queue" disabled={!!busy()} onClick={() => void action(`/v1/queue/${item.id}`, 'Track removed from queue.', 'DELETE')}>×</button></div>}
                </For>
              </Show>
            </div>
            <div class="panel-footnote">Queue changes sync with every listener.</div>
          </section>
        </div>
        <footer class="terminal-footer"><span>Your station. Your selection.</span><span>{busy() ? 'Executing request…' : 'All controls in sync with your station.'}</span></footer>
      </main>
      <dialog ref={clearDialog!} class="terminal-dialog" aria-labelledby="clear-title" aria-describedby="clear-description">
        <form method="dialog"><h2 id="clear-title">Clear the queue?</h2><p id="clear-description">Remove all upcoming tracks. The current track keeps playing. Automatic rotation may refill the queue.</p><div class="dialog-actions"><button class="btn" autofocus>Keep queue</button><button class="btn btn-danger" onClick={() => void action('/v1/queue:clear', 'Queue cleared.')}>Clear queue</button></div></form>
      </dialog>
      <div role="status" aria-live="polite"><Show when={notification()}><div class="notification" classList={{'notification--error': notificationError()}}>{notification()}</div></Show></div>
    </div>
  );
}
