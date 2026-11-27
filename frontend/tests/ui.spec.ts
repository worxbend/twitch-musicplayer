import { test, expect, type Page, type WebSocketRoute } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { createHash } from 'node:crypto';

const songs = [
  { id: 'a', title: 'Midnight compiler', artist: 'Soft Circuit', album: 'After hours', filename: 'a.mp3', duration_secs: 180, size_bytes: 1024 },
  { id: 'b', title: 'Buffering dreams', artist: 'Terminal Club', album: 'Night shift', filename: 'b.mp3', duration_secs: 205, size_bytes: 1024 },
];
const setlistText = '2026-01-01 19:57 UTC  Soft Circuit - Midnight compiler\n2026-01-01 20:01 UTC  Terminal Club - Buffering dreams\n';

async function mockStation(page: Page, options: { failure?: boolean; empty?: boolean; slow?: boolean; requireGesture?: boolean; hasCover?: boolean; trackSecs?: number; answerOnce?: boolean } = {}) {
  if (options.requireGesture) {
    // Playwright's WebSocket interception injects messages with synthetic activation.
    // Model the browser policy explicitly, unlocking only on a real input event.
    await page.addInitScript(() => {
      let activated = false;
      const activate = (event: Event) => { if (event.isTrusted) activated = true; };
      window.addEventListener('pointerdown', activate, true);
      window.addEventListener('keydown', activate, true);
      Object.defineProperty(navigator, 'userActivation', { value: { get isActive() { return activated; }, get hasBeenActive() { return activated; } } });
    });
  }
  let queue = options.empty ? [] : [{ id: 'q1', song_id: 'b', song: songs[1], position: 1, source: 'auto' }];
  let sawNeedSong!: () => void;
  const needSong = new Promise<void>(resolve => { sawNeedSong = resolve; });
  let sawSongFinished!: (ws: WebSocketRoute) => void;
  const songFinished = new Promise<WebSocketRoute>(resolve => { sawSongFinished = resolve; });
  let answered = false;
  await page.routeWebSocket(/\/ws(\?.*)?$/, ws => {
    ws.onMessage(message => {
      const type = JSON.parse(String(message)).type;
      if (type === 'song_finished') sawSongFinished(ws);
      if (type !== 'need_song') return;
      sawNeedSong();
      if (!options.empty && !(options.answerOnce && answered)) {
        answered = true;
        const song = options.hasCover === undefined ? songs[0] : { ...songs[0], has_cover: options.hasCover };
        ws.send(JSON.stringify({ type: 'play_song', payload: { song, history_id: 'h1', stream_url: '/v1/songs/a/content' } }));
      }
    });
  });
  await page.route('**/v1/**', async route => {
    const request = route.request();
    const url = new URL(request.url());
    if (options.failure) { await route.fulfill({ status: 503, json: { error: 'unavailable' } }); return; }
    if (options.slow) await new Promise(resolve => setTimeout(resolve, 500));
    if (url.pathname.endsWith('/content')) {
      // A tiny silent WAV lets the browser exercise the real media/autoplay path.
      const samples = 22050 * (options.trackSecs ?? 60);
      const wav = Buffer.alloc(44 + samples * 2);
      wav.write('RIFF'); wav.writeUInt32LE(wav.length - 8, 4); wav.write('WAVEfmt ', 8);
      wav.writeUInt32LE(16, 16); wav.writeUInt16LE(1, 20); wav.writeUInt16LE(1, 22);
      wav.writeUInt32LE(22050, 24); wav.writeUInt32LE(44100, 28); wav.writeUInt16LE(2, 32); wav.writeUInt16LE(16, 34);
      wav.write('data', 36); wav.writeUInt32LE(samples * 2, 40);
      await route.fulfill({ contentType: 'audio/wav', body: wav }); return;
    }
    if (request.method() === 'POST') {
      if (url.pathname === '/v1/queue') {
        const songId = request.postDataJSON().song_id;
        queue.push({ id: 'q2', song_id: songId, song: songs.find(s => s.id === songId)!, position: 2, source: 'manual' });
      }
      if (url.pathname.endsWith(':clear')) queue = [];
      await route.fulfill({ json: {} }); return;
    }
    if (request.method() === 'DELETE') {
      queue = queue.filter(item => item.id !== url.pathname.split('/').pop());
      await route.fulfill({ status: 204 }); return;
    }
    if (url.pathname === '/v1/history' && url.searchParams.get('format') === 'text') {
      await route.fulfill({ body: setlistText, contentType: 'text/plain; charset=utf-8' }); return;
    }
    if (url.pathname === '/v1/history') {
      const items = options.empty ? [] : [{ song: songs[0], started_at: '2026-01-01T20:05:00Z' }, { song: songs[1], started_at: '2026-01-01T20:01:00Z' }, { song: songs[0], started_at: '2026-01-01T19:57:00Z' }];
      await route.fulfill({ json: { items, total: items.length } }); return;
    }
    const query = url.searchParams.get('q')?.toLowerCase() || '';
    const json = url.pathname === '/v1/songs' ? { songs: options.empty ? [] : songs.filter(song => `${song.title} ${song.artist} ${song.album}`.toLowerCase().includes(query)) }
      : url.pathname === '/v1/queue' ? { items: queue } : { current_song: options.empty ? null : songs[0] };
    await route.fulfill({ json });
  });
  return { needSong, songFinished };
}

async function noOverflow(page: Page) {
  const dimensions = await page.evaluate(() => ({ viewport: window.innerWidth, content: document.documentElement.scrollWidth }));
  expect(dimensions.content).toBeLessThanOrEqual(dimensions.viewport);
}

for (const viewport of [{width: 1440, height: 960}, {width: 768, height: 1024}, {width: 390, height: 844}, {width: 320, height: 740}]) {
  test(`responsive layouts and accessible navigation at ${viewport.width}px`, async ({ page }) => {
    await page.setViewportSize(viewport);
    await mockStation(page);
    for (const route of ['/scenes', '/admin', '/', '/bars']) {
      await page.goto(route);
      await expect(page.getByRole('navigation', { name: 'Main navigation' })).toBeVisible();
      await expect(page.locator('main h1')).toBeVisible();
      await noOverflow(page);
      expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()).violations).toEqual([]);
    }
  });
}

test('scene ramp selection, copy confirmation, and clipboard fallback', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  await page.goto('/scenes');
  await page.getByLabel('Colour ramp').selectOption('EMBER');
  const equaliser = page.getByRole('article').filter({ has: page.getByRole('heading', { name: 'Equaliser', exact: true }) });
  await expect(equaliser.getByRole('link', { name: 'Open scene' })).toHaveAttribute('href', '/bars?ramp=EMBER');
  await expect(equaliser.getByRole('link', { name: 'Overlay' })).toHaveAttribute('href', '/bars-overlay?ramp=EMBER');
  await page.getByRole('button', { name: 'Copy Equaliser overlay URL' }).click();
  await expect(page.getByRole('status')).toContainText('Browser-source URL copied');
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe('http://127.0.0.1:3100/bars-overlay?ramp=EMBER');
  // Simulate a browser that denies clipboard access, without touching production code.
  await page.addInitScript(() => { Object.defineProperty(navigator, 'clipboard', { value: undefined }); });
  await page.reload();
  await page.getByRole('button', { name: 'Copy Equaliser scene URL' }).click();
  await expect(page.getByLabel('Browser-source URL', { exact: true })).toHaveValue('http://127.0.0.1:3100/bars?ramp=SPECTRUM');
});

test('now playing overlay URL builder copies the configured URL', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  await page.goto('/scenes');
  await page.getByLabel('Align').selectOption('center');
  await page.getByLabel('Size (px)').fill('48');
  await page.getByLabel('Hold (s)').fill('8');
  await page.getByLabel('Album art').check();
  await page.getByLabel('Theme').selectOption('pill');
  await page.getByLabel('Font').selectOption('mono');
  await page.getByLabel('Up next').check();
  await page.getByRole('button', { name: 'Copy now playing overlay URL' }).click();
  await expect(page.getByRole('status')).toContainText('Browser-source URL copied');
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe('http://127.0.0.1:3100/now-playing?align=center&size=48&hold=8&art=1&theme=pill&font=mono&next=1');
});

test('library search, queue add/remove, and keyboard-safe clear confirmation', async ({ page }) => {
  await mockStation(page);
  await page.goto('/admin');
  const library = page.getByRole('region', { name: 'Music library' });
  await expect(library.locator('.song-item')).toHaveCount(2);
  await page.getByLabel('Search music library').fill('Midnight');
  await expect(library.locator('.song-item')).toHaveCount(1);
  await page.getByRole('button', { name: 'Add Midnight compiler to queue' }).click();
  await expect(page.getByRole('status')).toContainText('Queued: Midnight compiler');
  await expect(page.locator('.queue-item')).toHaveCount(2);
  await page.getByRole('button', { name: 'Remove Midnight compiler from queue' }).click();
  await expect(page.locator('.queue-item')).toHaveCount(1);
  await page.getByRole('button', { name: 'Clear queue', exact: true }).click();
  await expect(page.getByRole('dialog')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Keep queue' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog')).not.toBeVisible();
  await expect(page.getByRole('button', { name: 'Clear queue', exact: true })).toBeFocused();
  await page.getByRole('button', { name: 'Clear queue', exact: true }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Clear queue', exact: true }).click();
  await expect(page.locator('.queue-item')).toHaveCount(0);
  await expect(page.getByText('Make the next move')).toBeVisible();
  await page.getByRole('button', { name: 'Reset', exact: true }).click();
  await expect(library.locator('.song-item')).toHaveCount(2);
});

test('empty, loading, search-empty, and backend failure states', async ({ page }) => {
  await mockStation(page, { empty: true, slow: true });
  await page.goto('/admin');
  await expect(page.getByText('Reading your library')).toBeVisible();
  await expect(page.getByText('Your library is quiet')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Skip track' })).toHaveAttribute('aria-disabled', 'true');
  await page.getByLabel('Search music library').fill('missing');
  await expect(page.getByText('No matching tracks')).toBeVisible();
  await expect(page.getByText('No songs played yet.')).toBeVisible();
  await page.unroute('**/v1/**');
  await mockStation(page, { failure: true });
  await page.reload();
  await expect(page.getByText('Library unavailable', { exact: true })).toBeVisible();
  await expect(page.getByText('Queue unavailable', { exact: true })).toBeVisible();
  await expect(page.getByText('Player status unavailable', { exact: true })).toBeVisible();
  await expect(page.getByText('History unavailable', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Retry connection' })).toBeVisible();
});

test('admin recently played lists history newest first', async ({ page }) => {
  await mockStation(page);
  await page.goto('/admin');
  const history = page.getByRole('region', { name: 'Recently played' }).getByRole('listitem');
  // songs[0] is on air, so only its newest entry is hidden; its earlier play stays.
  await expect(history).toHaveCount(2);
  await expect(history.nth(0)).toContainText('Buffering dreams');
  await expect(history.nth(0)).toContainText('Terminal Club');
  await expect(history.nth(0).locator('time')).toHaveAttribute('datetime', '2026-01-01T20:01:00Z');
  await expect(history.nth(1)).toContainText('Midnight compiler');
  await expect(history.nth(1).locator('time')).toHaveAttribute('datetime', '2026-01-01T19:57:00Z');
});

test('admin copies the setlist by keyboard and announces it', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  await mockStation(page);
  await page.goto('/admin');
  const panel = page.getByRole('region', { name: 'Recently played' });
  await expect(panel.getByRole('listitem')).toHaveCount(2);
  await panel.getByRole('button', { name: 'Copy setlist' }).focus();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('status')).toContainText('Setlist copied: 2 tracks');
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(setlistText);
  await expect(panel.getByRole('button', { name: 'Copy setlist' })).toBeFocused();
  expect((await new AxeBuilder({ page }).include('.history-panel').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()).violations).toEqual([]);
});

test('admin setlist falls back to a selectable text box without a clipboard', async ({ page }) => {
  await page.addInitScript(() => { Object.defineProperty(navigator, 'clipboard', { value: undefined }); });
  await mockStation(page);
  await page.goto('/admin');
  await page.getByRole('button', { name: 'Copy setlist' }).click();
  await expect(page.getByRole('status')).toContainText('Clipboard unavailable');
  await expect(page.getByRole('textbox', { name: 'Setlist' })).toHaveValue(setlistText);
});

test('admin refetches the library when the socket reconnects', async ({ page }) => {
  await mockStation(page);
  const sockets: WebSocketRoute[] = [];
  await page.routeWebSocket(/\/ws(\?.*)?$/, ws => { sockets.push(ws); });
  let library = songs;
  let songRequests = 0;
  await page.route(url => url.pathname === '/v1/songs', route => { songRequests++; return route.fulfill({ json: { songs: library } }); });
  await page.goto('/admin');
  const tracks = page.getByRole('region', { name: 'Music library' }).locator('.song-item');
  await expect(tracks).toHaveCount(2);
  // One fetch from the search debounce, one from the first socket open.
  await expect.poll(() => songRequests).toBe(2);
  const rescanned = { id: 'c', title: 'Rescanned lullaby', artist: 'Soft Circuit', album: 'After hours', filename: 'c.mp3', duration_secs: 150, size_bytes: 1024 };
  library = [...songs, rescanned];
  await sockets[0].close();
  await expect.poll(() => sockets.length).toBe(2);
  await expect.poll(() => songRequests).toBe(3);
  await expect(tracks).toHaveCount(3);
  await expect(page.getByText('Rescanned lullaby')).toBeVisible();
});

test('admin drops a silent socket and reconnects', async ({ page }) => {
  await mockStation(page);
  await page.clock.install();
  const sockets: WebSocketRoute[] = [];
  await page.routeWebSocket(/\/ws(\?.*)?$/, ws => { sockets.push(ws); });
  await page.goto('/admin');
  await expect.poll(() => sockets.length).toBe(1);
  // The route handler runs before the page's onopen, which arms the heartbeat.
  await expect(page.getByText('Backend connected')).toBeVisible();
  // No heartbeat_ack: stale at the 75s heartbeat tick, reconnect 900ms later. Running
  // further would hit the new socket's 10s handshake timeout before it opens in real time.
  await page.clock.runFor(75_000 + 900 + 100);
  await expect.poll(() => sockets.length).toBe(2);
});

test('untitled queue song is labelled by its filename', async ({ page }) => {
  await mockStation(page);
  const untitled = { id: 'c', title: '', artist: '', album: '', filename: 'c.mp3', duration_secs: 0, size_bytes: 1024 };
  await page.route('**/v1/queue', route => route.fulfill({ json: { items: [{ id: 'q3', song_id: 'c', song: untitled, position: 1, source: 'manual' }] } }));
  await page.goto('/admin');
  await expect(page.locator('.queue-item .queue-title')).toHaveText('c.mp3');
  await expect(page.getByRole('button', { name: 'Remove c.mp3 from queue' })).toBeVisible();
});

test('queue reorder by keyboard keeps focus', async ({ page }) => {
  await mockStation(page);
  const third = { id: 'c', title: 'Cassette rain', artist: 'Soft Circuit', album: 'After hours', filename: 'c.mp3', duration_secs: 150, size_bytes: 1024 };
  const [qa, qb, qc] = [songs[0], songs[1], third].map((song, i) => ({ id: `q${song.id}`, song_id: song.id, song, position: i + 1, source: 'manual' }));
  let queue = [qa, qb, qc];
  const moves: { url: string; body: unknown }[] = [];
  await page.route('**/v1/queue', route => route.fulfill({ json: { items: queue } }));
  await page.route(url => url.pathname.endsWith(':move'), route => {
    moves.push({ url: route.request().url(), body: route.request().postDataJSON() });
    queue = [qb, qa, qc];
    return route.fulfill({ json: { id: 'qb', position: 1, moved: true } });
  });
  await page.goto('/admin');
  const rows = page.locator('.queue-item');
  await expect(rows).toHaveCount(3);

  // The first row cannot move up: the control stays focusable but sends nothing.
  const firstUp = page.getByRole('button', { name: 'Move Midnight compiler up' });
  await expect(firstUp).toHaveAttribute('aria-disabled', 'true');
  await firstUp.focus();
  await page.keyboard.press('Enter');
  await expect(firstUp).toBeFocused();

  await page.getByRole('button', { name: 'Move Buffering dreams up' }).focus();
  await page.keyboard.press('Enter');
  await expect(rows.first()).toContainText('Buffering dreams');
  expect(moves).toHaveLength(1);
  expect(moves[0].url).toContain('/v1/queue/qb:move');
  expect(moves[0].body).toEqual({ direction: 'up' });
  const movedUp = page.getByRole('button', { name: 'Move Buffering dreams up' });
  await expect(movedUp).toBeFocused();
  await expect(movedUp).toHaveAttribute('aria-disabled', 'true');
  await expect(page.getByRole('button', { name: 'Move Cassette rain down' })).toHaveAttribute('aria-disabled', 'true');
  expect((await new AxeBuilder({ page }).include('.queue-panel').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()).violations).toEqual([]);
});

test('queue remove by keyboard moves focus to the next row, then the heading', async ({ page }) => {
  await mockStation(page);
  const third = { id: 'c', title: 'Cassette rain', artist: 'Soft Circuit', album: 'After hours', filename: 'c.mp3', duration_secs: 150, size_bytes: 1024 };
  let queue = [songs[0], songs[1], third].map((song, i) => ({ id: `q${song.id}`, song_id: song.id, song, position: i + 1, source: 'manual' }));
  await page.route('**/v1/queue', route => route.fulfill({ json: { items: queue } }));
  await page.route(url => url.pathname.startsWith('/v1/queue/q'), route => {
    if (route.request().method() !== 'DELETE') return route.fallback();
    const id = new URL(route.request().url()).pathname.split('/').pop();
    queue = queue.filter(item => item.id !== id);
    return route.fulfill({ status: 204 });
  });
  await page.goto('/admin');
  const rows = page.locator('.queue-item');
  await expect(rows).toHaveCount(3);

  await page.getByRole('button', { name: 'Remove Buffering dreams from queue' }).focus();
  await page.keyboard.press('Enter');
  await expect(rows).toHaveCount(2);
  await expect(page.getByRole('button', { name: 'Remove Cassette rain from queue' })).toBeFocused();

  // Removing the last row moves focus up to the new last row.
  await page.keyboard.press('Enter');
  await expect(rows).toHaveCount(1);
  await expect(page.getByRole('button', { name: 'Remove Midnight compiler from queue' })).toBeFocused();

  await page.keyboard.press('Enter');
  await expect(rows).toHaveCount(0);
  await expect(page.locator('#queue-heading')).toBeFocused();
  expect((await new AxeBuilder({ page }).include('.queue-panel').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()).violations).toEqual([]);
});

test('play-now and skip send the existing backend commands', async ({ page }) => {
  await mockStation(page);
  await page.goto('/admin');
  const play = page.waitForRequest(r => r.url().endsWith('/v1/queue:play-next') && r.method() === 'POST');
  await page.getByRole('button', { name: 'Play Midnight compiler now' }).click();
  expect((await play).postDataJSON()).toEqual({song_id: 'a'});
  await expect(page.getByRole('status')).toContainText('Play requested');
  const skip = page.waitForRequest(r => r.url().endsWith('/v1/player:skip') && r.method() === 'POST');
  await page.getByRole('button', { name: 'Skip track' }).click();
  await skip;
  await expect(page.getByRole('status')).toContainText('Skip requested');
});

// mockBlocklist serves GET /v1/blocked from a list that :block and :unblock edit.
async function mockBlocklist(page: Page, initial: { id: string; title?: string; missing?: true }[] = []) {
  let blocked = initial;
  await page.route(url => url.pathname === '/v1/blocked', route => route.fulfill({ json: { songs: blocked } }));
  await page.route(url => /^\/v1\/songs\/[^/]+:(un)?block$/.test(url.pathname), route => {
    const [, id, verb] = new URL(route.request().url()).pathname.match(/^\/v1\/songs\/([^/]+):(block|unblock)$/)!;
    blocked = verb === 'block' ? [...blocked, songs.find(song => song.id === id)!] : blocked.filter(entry => entry.id !== id);
    return route.fulfill({ json: { message: `song ${verb}ed` } });
  });
}

test('admin blocks a song from the library and allows it again', async ({ page }) => {
  await mockStation(page);
  await mockBlocklist(page);
  await page.goto('/admin');
  const panel = page.getByRole('region', { name: 'Blocked', exact: true });
  await expect(panel.getByText('Every track is in rotation.')).toBeVisible();
  const block = page.waitForRequest(r => r.url().endsWith('/v1/songs/a:block') && r.method() === 'POST');
  await page.getByRole('button', { name: "Don't play Midnight compiler" }).click();
  await block;
  await expect(page.getByRole('status')).toContainText('Out of rotation: Midnight compiler');
  await expect(panel.getByRole('listitem')).toHaveText([/Midnight compiler/]);
  const unblock = page.waitForRequest(r => r.url().endsWith('/v1/songs/a:unblock') && r.method() === 'POST');
  await panel.getByRole('button', { name: 'Allow Midnight compiler again' }).click();
  await unblock;
  await expect(panel.getByRole('listitem')).toHaveCount(0);
  await expect(page.locator('#blocked-heading')).toBeFocused();
  expect((await new AxeBuilder({ page }).include('[aria-labelledby="blocked-heading"]').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()).violations).toEqual([]);
});

test('admin labels a missing blocked song by id and reports a blocked add', async ({ page }) => {
  await mockStation(page);
  await mockBlocklist(page, [{ id: 'gone', missing: true }]);
  await page.route('**/v1/queue', route => route.request().method() === 'POST'
    ? route.fulfill({ status: 409, json: { error: 'song is blocked' } })
    : route.fallback());
  await page.goto('/admin');
  const panel = page.getByRole('region', { name: 'Blocked', exact: true });
  await expect(panel.getByRole('listitem')).toHaveText([/gone.*File no longer in the library/]);
  await page.getByRole('button', { name: 'Add Midnight compiler to queue' }).click();
  await expect(page.getByRole('status')).toContainText('Action failed: song is blocked.');
});

test('audio unlock works by keyboard and reports actual playback', async ({ page }) => {
  await mockStation(page, { requireGesture: true });
  await page.goto('/');
  const enable = page.getByRole('button', { name: 'Enable audio' });
  await expect(enable).toBeVisible();
  await expect(page.getByRole('progressbar')).toHaveAttribute('aria-valuetext', 'No track loaded');
  await enable.focus();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('button', { name: 'Starting audio' })).toHaveAttribute('aria-disabled', 'true');
  await expect(page.locator('.status')).toHaveText('On air', {timeout: 10000});
  await expect(page.locator('#np-title')).toHaveText('Midnight compiler');
  await expect(page.getByRole('progressbar')).toHaveAttribute('aria-valuenow', /[1-9]/);
  await expect(page.getByRole('progressbar')).toHaveAttribute('aria-valuetext', /^\d+:\d{2} of \d+:\d{2}$/);
});

const onePixelPng = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==', 'base64');

async function startPlayback(page: Page) {
  await page.goto('/');
  await page.getByRole('button', { name: 'Enable audio' }).click();
  await expect(page.locator('#np-title')).toHaveText('Midnight compiler', { timeout: 10000 });
}

test('the track deck shows embedded cover art for a tagged track', async ({ page }) => {
  await mockStation(page, { requireGesture: true, hasCover: true });
  await page.route('**/v1/songs/*/cover', route => route.fulfill({ contentType: 'image/png', body: onePixelPng }));
  await startPlayback(page);
  const cover = page.locator('.player-deck img.np-cover');
  await expect(cover).toBeVisible();
  await expect(cover).toHaveAttribute('src', 'http://127.0.0.1:18089/v1/songs/a/cover');
  await expect(page.locator('.player-deck .deck-art')).toHaveCount(0);
});

test('the track deck keeps the record art for a track without a cover', async ({ page }) => {
  await mockStation(page, { requireGesture: true, hasCover: false });
  await startPlayback(page);
  await expect(page.locator('.player-deck img')).toHaveCount(0);
  await expect(page.locator('.player-deck .deck-art')).toBeVisible();
});

test('the track deck falls back to the record art when the cover fails to load', async ({ page }) => {
  await mockStation(page, { requireGesture: true, hasCover: true });
  await page.route('**/v1/songs/*/cover', route => route.fulfill({ status: 404, json: { error: 'no cover' } }));
  await startPlayback(page);
  await expect(page.locator('.player-deck img')).toHaveCount(0);
  await expect(page.locator('.player-deck .deck-art')).toBeVisible();
});

test('player status stays in one live region from blocked to on air', async ({ page }) => {
  await mockStation(page, { requireGesture: true });
  await page.goto('/');
  const status = page.getByRole('status');
  await expect(status).toHaveCount(1);
  await expect(status).toHaveAttribute('aria-live', 'polite');
  await expect(status).toContainText('click');
  const deckToggle = page.getByRole('button', { name: 'Pause', exact: true });
  await expect(deckToggle).toHaveAttribute('aria-pressed', 'true');
  await deckToggle.click();
  await expect(status).toContainText('On air', {timeout: 10000});
  await expect(status).toHaveCount(1);
  await expect(deckToggle).toHaveAttribute('aria-pressed', 'false');
});

test('deck stays on air when an error frame arrives during the audible tail', async ({ page }) => {
  const { songFinished } = await mockStation(page, { requireGesture: true, trackSecs: 6, answerOnce: true });
  await startPlayback(page);
  const ws = await songFinished;
  ws.send(JSON.stringify({ type: 'error', payload: { code: 'queue_empty', message: 'queue is empty' } }));
  // Give the frame time to land; the old track still has ~3s to play.
  await page.waitForTimeout(500);
  const deckToggle = page.getByRole('button', { name: 'Pause', exact: true });
  await expect(page.locator('.status')).toHaveText('On air');
  await expect(deckToggle).not.toHaveAttribute('aria-disabled', 'true');
  await expect(page.locator('.status')).toHaveText('Standby', { timeout: 10000 });
  await expect(page.locator('.player-message')).toContainText('The queue is empty. Add a track in the console.');
});

test('K and M ignore key auto-repeat', async ({ page }) => {
  await mockStation(page, { requireGesture: true });
  await startPlayback(page);
  const deckToggle = page.getByRole('button', { name: 'Pause', exact: true });
  const mute = page.getByRole('button', { name: 'Mute' });
  const repeat = (key: string) => page.evaluate(k => document.activeElement!.dispatchEvent(new KeyboardEvent('keydown', { key: k, repeat: true, bubbles: true })), key);
  await deckToggle.focus();
  await page.keyboard.press('k');
  await expect(deckToggle).toHaveAttribute('aria-pressed', 'true');
  await repeat('k');
  await expect(deckToggle).toHaveAttribute('aria-pressed', 'true');
  await mute.focus();
  await page.keyboard.press('m');
  await expect(mute).toHaveAttribute('aria-pressed', 'true');
  await repeat('m');
  await expect(mute).toHaveAttribute('aria-pressed', 'true');
});

type MediaHandlerWindow = Window & { __mediaHandlers: Record<string, () => void> };

test('media-key pause shows the paused state, Resume, the deck toggle and K restart playback', async ({ page }) => {
  // The audio elements are never in the DOM, so the media key's pause handler is the real trigger.
  await page.addInitScript(() => {
    if (!('mediaSession' in navigator)) return;
    const handlers: Record<string, () => void> = {};
    (window as unknown as MediaHandlerWindow).__mediaHandlers = handlers;
    const original = navigator.mediaSession.setActionHandler.bind(navigator.mediaSession);
    navigator.mediaSession.setActionHandler = (action, handler) => {
      if (handler) handlers[action] = () => handler({ action });
      original(action, handler);
    };
  });
  await mockStation(page, { requireGesture: true });
  await page.goto('/');
  test.skip(!(await page.evaluate(() => 'mediaSession' in navigator)), 'browser has no Media Session API');
  await page.getByRole('button', { name: 'Enable audio' }).focus();
  await page.keyboard.press('Enter');
  await expect(page.locator('.status')).toHaveText('On air', {timeout: 10000});
  const playbackState = () => page.evaluate(() => navigator.mediaSession.playbackState);
  const deckToggle = page.getByRole('button', { name: 'Pause', exact: true });
  await expect(deckToggle).toHaveAttribute('aria-pressed', 'false');

  await page.evaluate(() => (window as unknown as MediaHandlerWindow).__mediaHandlers.pause());
  await expect(page.getByRole('status')).toContainText('Paused');
  const resume = page.getByRole('button', { name: 'Resume ▶' });
  await expect(resume).toBeVisible();
  await expect.poll(playbackState).toBe('paused');
  await expect(deckToggle).toHaveAttribute('aria-pressed', 'true');
  expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()).violations).toEqual([]);

  await resume.click();
  await expect(resume).toHaveCount(0);
  await expect(page.locator('.status')).toHaveText('On air');
  await expect.poll(playbackState).toBe('playing');
  await expect(deckToggle).toHaveAttribute('aria-pressed', 'false');

  await deckToggle.focus();
  await page.keyboard.press('k');
  await expect.poll(playbackState).toBe('paused');
  await expect(deckToggle).toHaveAttribute('aria-pressed', 'true');
  await deckToggle.click();
  await expect(page.locator('.status')).toHaveText('On air');
  await expect.poll(playbackState).toBe('playing');
  await expect(deckToggle).toHaveAttribute('aria-pressed', 'false');
});

test('volume shortcuts change the slider, mute toggles, and the level survives reload', async ({ page }) => {
  await mockStation(page, { empty: true });
  await page.goto('/');
  const slider = page.getByRole('slider', { name: 'Volume' });
  const mute = page.getByRole('button', { name: 'Mute' });
  await expect(slider).toHaveValue('100');
  await mute.focus();
  await page.keyboard.press('-');
  await page.keyboard.press('-');
  await expect(slider).toHaveValue('90');
  await page.keyboard.press('m');
  await expect(mute).toHaveAttribute('aria-pressed', 'true');
  await page.keyboard.press('=');
  await expect(mute).toHaveAttribute('aria-pressed', 'false');
  await expect(slider).toHaveValue('95');
  await page.reload();
  await expect(page.getByRole('slider', { name: 'Volume' })).toHaveValue('95');
});

test('volume shortcuts work while the slider has focus and its arrow keys still step it', async ({ page }) => {
  await mockStation(page, { empty: true });
  await page.goto('/');
  const slider = page.getByRole('slider', { name: 'Volume' });
  const mute = page.getByRole('button', { name: 'Mute' });
  await expect(slider).toHaveValue('100');
  await slider.focus();
  await page.keyboard.press('-');
  await page.keyboard.press('-');
  await expect(slider).toHaveValue('90');
  await page.keyboard.press('m');
  await expect(mute).toHaveAttribute('aria-pressed', 'true');
  await page.keyboard.press('m');
  await expect(mute).toHaveAttribute('aria-pressed', 'false');
  await page.keyboard.press('ArrowUp');
  await expect(slider).toHaveValue('91');
});

// WCAG 2.1.4: single-key shortcuts work only while focus is inside the deck.
test('volume shortcuts fire only while focus is inside the player deck', async ({ page }) => {
  await mockStation(page, { empty: true });
  await page.setViewportSize({ width: 1440, height: 960 });
  await page.goto('/');
  const slider = page.getByRole('slider', { name: 'Volume' });
  const mute = page.getByRole('button', { name: 'Mute' });
  for (const outside of [page.locator('main'), page.getByRole('group', { name: 'Scene selection' }).getByRole('link').first()]) {
    await outside.focus();
    await page.keyboard.press('m');
    await page.keyboard.press('-');
    await expect(mute).toHaveAttribute('aria-pressed', 'false');
    await expect(slider).toHaveValue('100');
  }
  await page.getByRole('link', { name: 'Change scene' }).focus();
  await page.keyboard.press('m');
  await expect(mute).toHaveAttribute('aria-pressed', 'true');
  await mute.focus();
  await page.keyboard.press('m');
  await expect(mute).toHaveAttribute('aria-pressed', 'false');
});

test('scene routes share the persisted volume; overlays render no slider', async ({ page }) => {
  await mockStation(page, { empty: true });
  await page.goto('/');
  await page.getByRole('button', { name: 'Mute' }).focus();
  await page.keyboard.press('-');
  await page.keyboard.press('-');
  await expect(page.getByRole('slider', { name: 'Volume' })).toHaveValue('90');
  await page.goto('/spectrum');
  await expect(page.getByRole('slider', { name: 'Volume' })).toHaveValue('90');
  await page.getByRole('button', { name: 'Mute' }).focus();
  await page.keyboard.press('m');
  await expect(page.getByRole('button', { name: 'Mute' })).toHaveAttribute('aria-pressed', 'true');
  await page.goto('/spectrum-overlay');
  await expect(page.locator('#stage canvas')).toBeVisible();
  await expect(page.getByRole('slider', { name: 'Volume' })).toHaveCount(0);
});

for (const slug of ['spectrum', 'bars', 'mirror', 'dots', 'ribbon', 'line', 'lens']) {
  test(`${slug} renders; its OBS overlay stays chrome-free and transparent`, async ({ page }) => {
    await mockStation(page, { empty: true });
    const errors: string[] = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(`/${slug}?ramp=NEON`);
    await expect(page.locator('#stage canvas')).toBeVisible();
    await expect(page.getByRole('navigation')).toBeVisible();
    await page.goto(`/${slug}-overlay?ramp=NEON`);
    await expect(page.locator('#stage canvas')).toBeVisible();
    await expect(page.getByRole('navigation')).toHaveCount(0);
    expect(await page.locator('body').evaluate(el => getComputedStyle(el).backgroundColor)).toBe('rgba(0, 0, 0, 0)');
    expect(errors).toEqual([]);
  });
}

test('original overlays and forced transparency preserve browser-source mode', async ({ page }) => {
  await mockStation(page, { empty: true });
  for (const route of ['/overlay', '/overlay/', '/logo-overlay', '/logo-overlay/', '/bars?transparent=1']) {
    await page.goto(route);
    await expect(page.locator('#stage canvas')).toBeVisible();
    await expect(page.getByRole('navigation')).toHaveCount(0);
    if (!route.startsWith('/overlay')) {
      expect(await page.locator('body').evaluate(el => getComputedStyle(el).backgroundColor)).toBe('rgba(0, 0, 0, 0)');
    }
  }
});

test('reduced motion freezes the composition without preventing playback', async ({ page }) => {
  test.setTimeout(60000); // Software-rendered WebGL screenshots can be slow in CI.
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await mockStation(page, { requireGesture: true });
  await page.goto('/');
  await page.getByRole('button', { name: 'Enable audio' }).click();
  await expect(page.locator('.status')).toHaveText('On air', { timeout: 10000 });
  // Inspect the scene, excluding the live clock and track-progress chrome.
  const clip = { x: 500, y: 360, width: 400, height: 250 };
  const hash = (image: Buffer) => createHash('sha256').update(image).digest('hex');
  const before = hash(await page.screenshot({ clip }));
  await page.waitForTimeout(300);
  expect(hash(await page.screenshot({ clip }))).toEqual(before);
  const progress = Number(await page.getByRole('progressbar').getAttribute('aria-valuenow'));
  await expect.poll(async () => Number(await page.getByRole('progressbar').getAttribute('aria-valuenow'))).toBeGreaterThan(progress);
});

test('the player canvas follows its frame when the page scrolls', async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 256 });
  await mockStation(page, { requireGesture: true });
  await page.goto('/');
  await page.locator('#stage canvas').waitFor({ state: 'attached' });
  await page.evaluate(() => document.body.scrollTo(0, 200));
  expect(await page.evaluate(() => document.body.scrollTop)).toBeGreaterThan(0);
  const offset = () => page.evaluate(() => Math.abs(
    document.querySelector('#stage')!.getBoundingClientRect().top
    - document.querySelector('.performance-space')!.getBoundingClientRect().top));
  await expect.poll(offset).toBeLessThanOrEqual(1);
});

test('audio starts when PixiJS has no renderer at all', async ({ page }) => {
  await page.addInitScript(() => {
    // Pixi falls back to Canvas2D without WebGL/WebGPU, so deny that too to make init reject.
    const getContext = HTMLCanvasElement.prototype.getContext;
    const denied = new Set(['webgl', 'webgl2', 'experimental-webgl', '2d']);
    HTMLCanvasElement.prototype.getContext = function (this: HTMLCanvasElement, type: string, ...rest: unknown[]) {
      return denied.has(type) ? null : (getContext as (...args: unknown[]) => RenderingContext | null).call(this, type, ...rest);
    } as typeof HTMLCanvasElement.prototype.getContext;
    Object.defineProperty(navigator, 'gpu', { value: undefined, configurable: true });
  });
  const { needSong } = await mockStation(page);
  const content = page.waitForRequest(r => r.url().endsWith('/v1/songs/a/content'));
  await page.goto('/?autoplay=1');
  await needSong;
  await content;
});

test('now-playing hold fades the title out; no hold keeps it visible', async ({ page }) => {
  await page.routeWebSocket(/\/ws(\?.*)?$/, ws => {
    ws.send(JSON.stringify({ type: 'now_playing', payload: { song: songs[0], queue_depth: 1 } }));
  });
  const overlay = page.locator('.now-playing-overlay');
  await page.goto('/now-playing?hold=1');
  await expect(page.getByText('Midnight compiler')).toBeAttached();
  await expect(overlay).toHaveClass(/np-hidden/, { timeout: 3000 });
  await page.goto('/now-playing');
  await expect(page.getByText('Midnight compiler')).toBeVisible();
  await page.waitForTimeout(1500);
  await expect(overlay).not.toHaveClass(/np-hidden/);
});

test('now-playing applies ?theme= and ?font= classes; no params adds none', async ({ page }) => {
  await page.routeWebSocket(/\/ws(\?.*)?$/, ws => {
    ws.send(JSON.stringify({ type: 'now_playing', payload: { song: songs[0], queue_depth: 1 } }));
  });
  const overlay = page.locator('.now-playing-overlay');
  await page.goto('/now-playing?theme=pill&font=mono');
  await expect(page.getByText('Midnight compiler')).toBeVisible();
  await expect(overlay).toHaveClass(/\bnp-theme-pill\b/);
  await expect(overlay).toHaveClass(/\bnp-font-mono\b/);
  await page.goto('/now-playing');
  await expect(page.getByText('Midnight compiler')).toBeVisible();
  await expect(overlay).toHaveAttribute('class', 'now-playing-overlay');
});

test('now-playing shows cover art only with ?art=1', async ({ page }) => {
  await page.routeWebSocket(/\/ws(\?.*)?$/, ws => {
    ws.send(JSON.stringify({ type: 'now_playing', payload: { song: { ...songs[0], has_cover: true }, history_id: 'h1', queue_depth: 1 } }));
  });
  await page.route('**/v1/songs/*/cover', route => route.fulfill({ contentType: 'image/png', body: onePixelPng }));
  await page.goto('/now-playing?art=1');
  await expect(page.locator('.np-art')).toBeVisible();
  await expect(page.locator('.np-art')).toHaveAttribute('src', 'http://127.0.0.1:18089/v1/songs/a/cover');
  await page.goto('/now-playing');
  await expect(page.getByText('Midnight compiler')).toBeVisible();
  await expect(page.locator('.np-art')).toHaveCount(0);
});

test('now-playing ?next=1 shows the next queued title; an empty queue hides it', async ({ page }) => {
  let route!: WebSocketRoute;
  await page.routeWebSocket(/\/ws(\?.*)?$/, ws => { route = ws; });
  const send = (msg: object) => route.send(JSON.stringify(msg));
  const upNext = page.locator('.np-next');
  await page.goto('/now-playing?next=1');
  await expect.poll(() => route).toBeTruthy();

  send({ type: 'state', payload: { current_song: songs[0], queue_length: 1, total_songs: 2, next_song: songs[1] } });
  await expect(upNext).toHaveText('Up next: Buffering dreams');
  send({ type: 'queue_updated', payload: { queue_depth: 0, reason: 'song_started' } });
  await expect(upNext).toHaveCount(0);
  await expect(page.getByText('Midnight compiler')).toBeVisible();
});

test('now-playing without ?next=1 shows no up-next line', async ({ page }) => {
  await page.routeWebSocket(/\/ws(\?.*)?$/, ws => {
    ws.send(JSON.stringify({ type: 'state', payload: { current_song: songs[0], queue_length: 1, total_songs: 2, next_song: songs[1] } }));
  });
  await page.goto('/now-playing');
  await expect(page.getByText('Midnight compiler')).toBeVisible();
  await expect(page.locator('.np-next')).toHaveCount(0);
});

test('now-playing hold re-shows a repeated title on a new history_id; a dry-queue state clears it', async ({ page }) => {
  let route!: WebSocketRoute;
  await page.routeWebSocket(/\/ws(\?.*)?$/, ws => { route = ws; });
  const send = (msg: object) => route.send(JSON.stringify(msg));
  const overlay = page.locator('.now-playing-overlay');
  const title = page.getByText('Midnight compiler');
  await page.goto('/now-playing?hold=2');
  await expect.poll(() => route).toBeTruthy();

  send({ type: 'now_playing', payload: { song: songs[0], history_id: 'h1', queue_depth: 1 } });
  await expect(title).toBeAttached();
  await expect(overlay).toHaveClass(/np-hidden/, { timeout: 5000 });

  // Same title, new start: shows again, then hides after the hold.
  send({ type: 'now_playing', payload: { song: songs[0], history_id: 'h2', queue_depth: 1 } });
  await expect(overlay).not.toHaveClass(/np-hidden/);
  await expect(overlay).toHaveClass(/np-hidden/, { timeout: 5000 });

  // A reconnect state frame carries no history_id and must not re-show.
  // One-shot reads across more than hold=2: a retrying expect would wait out
  // a wrong re-show until the hold timer hid the title again.
  send({ type: 'state', payload: { current_song: songs[0] } });
  for (let i = 0; i < 10; i++) {
    await page.waitForTimeout(250);
    expect(await overlay.getAttribute('class')).toMatch(/np-hidden/);
  }

  // Dry queue: state without current_song clears the title.
  send({ type: 'state', payload: {} });
  await expect(title).not.toBeAttached();

  // The next start after the idle clear shows again.
  send({ type: 'now_playing', payload: { song: songs[0], history_id: 'h3', queue_depth: 1 } });
  await expect(title).toBeAttached();
  await expect(overlay).not.toHaveClass(/np-hidden/);
});

// A half-open socket delivers nothing. Without heartbeat_ack the overlay must reconnect,
// and with acks it must keep the healthy socket (6df6ecb).
test('now-playing overlay reconnects a silent socket', async ({ page }) => {
  await page.clock.install();
  const sockets: WebSocketRoute[] = [];
  await page.routeWebSocket(/\/ws(\?.*)?$/, ws => {
    sockets.push(ws);
    ws.send(JSON.stringify({ type: 'now_playing', payload: { song: songs[0], history_id: 'h1', queue_depth: 1 } }));
  });
  await page.goto('/now-playing');
  await expect.poll(() => sockets.length).toBe(1);
  // onmessage follows onopen, so a shown title proves the heartbeat is armed.
  await expect(page.getByText('Midnight compiler')).toBeVisible();
  // Stale at the 75s heartbeat tick, reconnect 900ms later; stop before the new socket's
  // 10s handshake timeout, which fake time would reach before the real handshake ends.
  await page.clock.runFor(75_000 + 900 + 100);
  await expect.poll(() => sockets.length).toBe(2);
});

test('now-playing overlay keeps a socket that answers heartbeats', async ({ page }) => {
  await page.clock.install();
  const sockets: WebSocketRoute[] = [];
  let acks = 0;
  await page.routeWebSocket(/\/ws(\?.*)?$/, ws => {
    sockets.push(ws);
    ws.send(JSON.stringify({ type: 'now_playing', payload: { song: songs[0], history_id: 'h1', queue_depth: 1 } }));
    ws.onMessage(message => {
      if (JSON.parse(String(message)).type !== 'heartbeat') return;
      acks++;
      ws.send(JSON.stringify({ type: 'heartbeat_ack' }));
    });
  });
  await page.goto('/now-playing');
  await expect.poll(() => sockets.length).toBe(1);
  await expect(page.getByText('Midnight compiler')).toBeVisible();
  // Advance one heartbeat at a time so each ack lands before the next stale check.
  for (let i = 0; i < 7; i++) {
    await page.clock.runFor(25_000);
    await expect.poll(() => acks).toBe(i + 1);
  }
  expect(sockets.length).toBe(1);
});
