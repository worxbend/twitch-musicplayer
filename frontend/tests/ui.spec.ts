import { test, expect, type Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { createHash } from 'node:crypto';

const songs = [
  { id: 'a', title: 'Midnight compiler', artist: 'Soft Circuit', album: 'After hours', filename: 'a.mp3', duration_secs: 180, size_bytes: 1024 },
  { id: 'b', title: 'Buffering dreams', artist: 'Terminal Club', album: 'Night shift', filename: 'b.mp3', duration_secs: 205, size_bytes: 1024 },
];

async function mockStation(page: Page, options: { failure?: boolean; empty?: boolean; slow?: boolean; requireGesture?: boolean } = {}) {
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
  await page.routeWebSocket('**/ws', ws => {
    ws.onMessage(message => {
      if (JSON.parse(String(message)).type === 'need_song' && !options.empty) {
        ws.send(JSON.stringify({ type: 'play_song', payload: { song: songs[0], history_id: 'h1', stream_url: '/v1/songs/a/content' } }));
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
      const samples = 22050 * 60;
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
    const query = url.searchParams.get('q')?.toLowerCase() || '';
    const json = url.pathname === '/v1/songs' ? { songs: options.empty ? [] : songs.filter(song => `${song.title} ${song.artist} ${song.album}`.toLowerCase().includes(query)) }
      : url.pathname === '/v1/queue' ? { items: queue } : { current_song: options.empty ? null : songs[0] };
    await route.fulfill({ json });
  });
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
  await expect(page.getByRole('button', { name: 'Skip track' })).toBeDisabled();
  await page.getByLabel('Search music library').fill('missing');
  await expect(page.getByText('No matching tracks')).toBeVisible();
  await page.unroute('**/v1/**');
  await mockStation(page, { failure: true });
  await page.reload();
  await expect(page.getByText('Library unavailable', { exact: true })).toBeVisible();
  await expect(page.getByText('Queue unavailable', { exact: true })).toBeVisible();
  await expect(page.getByText('Player status unavailable', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Retry connection' })).toBeVisible();
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

test('audio unlock works by keyboard and reports actual playback', async ({ page }) => {
  await mockStation(page, { requireGesture: true });
  await page.goto('/');
  const enable = page.getByRole('button', { name: 'Enable audio' });
  await expect(enable).toBeVisible();
  await enable.focus();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('button', { name: 'Starting audio' })).toBeDisabled();
  await expect(page.locator('.status')).toHaveText('On air', {timeout: 10000});
  await expect(page.locator('#np-title')).toHaveText('Midnight compiler');
  await expect(page.getByRole('progressbar')).toHaveAttribute('aria-valuenow', /[1-9]/);
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
  for (const route of ['/overlay', '/logo-overlay', '/bars?transparent=1']) {
    await page.goto(route);
    await expect(page.locator('#stage canvas')).toBeVisible();
    await expect(page.getByRole('navigation')).toHaveCount(0);
    if (route !== '/overlay') {
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
