import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

const song = { id: 'a', title: 'Midnight compiler', filename: 'a.mp3', artist: 'Soft Circuit', album: 'After hours', duration_secs: 180 };

test('keyboard skip link, shared navigation, and visible focus', async ({ page }) => {
  await page.routeWebSocket(/\/ws(\?.*)?$/, () => {});
  await page.route('**/v1/**', route => route.fulfill({ json: { songs: [], items: [], current_song: null } }));
  await page.goto('/scenes');
  await page.keyboard.press('Tab');
  await expect(page.getByRole('link', { name: 'Skip to content' })).toBeFocused();
  await expect(page.getByRole('link', { name: 'Skip to content' })).toBeVisible();
  await page.keyboard.press('Enter');
  expect(new URL(page.url()).hash).toBe('#main');
  await page.getByRole('link', { name: 'Console', exact: true }).click();
  await expect(page).toHaveURL(/\/admin$/);
  await expect(page.getByRole('heading', { name: 'Station control' })).toBeVisible();
  await page.getByRole('link', { name: 'Scenes', exact: true }).click();
  await expect(page).toHaveURL(/\/scenes$/);
  await expect(page.getByRole('link', { name: 'Scenes', exact: true })).toHaveAttribute('aria-current', 'page');
  await page.getByLabel('Colour ramp').focus();
  expect(await page.getByLabel('Colour ramp').evaluate(el => getComputedStyle(el).outlineStyle)).toBe('solid');
});

test('failed actions report an error, then recover without losing the library', async ({ page }) => {
  let fail = false;
  let served = 0;
  let songLoads = 0;
  await page.routeWebSocket(/\/ws(\?.*)?$/, () => {});
  await page.route('**/v1/**', route => {
    if (fail) return route.fulfill({ status: 503, json: {} });
    const path = new URL(route.request().url()).pathname;
    served++;
    if (path === '/v1/songs') songLoads++;
    return route.fulfill({ json: path === '/v1/songs' ? {songs: [song]} : path === '/v1/queue' ? {items: []} : {current_song: song} });
  });
  await page.goto('/admin');
  await expect(page.locator('.song-item')).toHaveCount(1);
  await expect(page.getByText('Backend connected')).toBeVisible();
  // Startup sends 10 requests: 4 from onMount, 1 from the search debounce, 5 from the socket open.
  // A late one answered with 503 would wipe the library, so let all of them land first.
  await expect.poll(() => songLoads).toBe(2);
  await expect.poll(() => served).toBe(10);
  fail = true;
  await page.getByRole('button', {name: 'Add Midnight compiler to queue'}).click();
  await expect(page.getByRole('status')).toContainText('Action failed. Check the backend connection');
  await expect(page.locator('.notification')).toHaveClass(/notification--error/);
  await expect(page.getByRole('button', {name: 'Add Midnight compiler to queue'})).toBeEnabled();
  await expect(page.locator('.song-item')).toHaveCount(1);
  fail = false;
  await page.getByRole('button', {name: 'Add Midnight compiler to queue'}).click();
  await expect(page.getByRole('status')).toContainText('Queued: Midnight compiler');
});

test('a rejected action shows the backend reason, not a connection failure', async ({ page }) => {
  await page.routeWebSocket(/\/ws(\?.*)?$/, () => {});
  await page.route('**/v1/**', route => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/v1/player:skip') return route.fulfill({ status: 409, json: { error: 'queue is empty' } });
    return route.fulfill({ json: path === '/v1/songs' ? {songs: [song]} : path === '/v1/queue' ? {items: []} : {current_song: song} });
  });
  await page.goto('/admin');
  await expect(page.locator('.now-playing-title')).toHaveText('Midnight compiler');
  await page.getByRole('button', {name: 'Skip track'}).click();
  await expect(page.getByRole('status')).toContainText('Action failed: queue is empty.');
  await expect(page.getByRole('status')).not.toContainText('Check the backend connection');
  await expect(page.locator('.notification')).toHaveClass(/notification--error/);
});

test('a slow previous search never overwrites a newer result', async ({ page }) => {
  await page.routeWebSocket(/\/ws(\?.*)?$/, () => {});
  await page.route('**/v1/**', async route => {
    const url = new URL(route.request().url());
    const query = url.searchParams.get('q');
    if (query === 'old') await new Promise(resolve => setTimeout(resolve, 1000));
    await route.fulfill({ json: url.pathname === '/v1/songs' ? {songs: [{...song, title: query || song.title}]} : {items: []} });
  });
  await page.goto('/admin');
  await expect(page.locator('.song-title')).toHaveText('Midnight compiler');
  await page.getByLabel('Search music library').fill('old');
  await page.waitForRequest(r => r.url().endsWith('/v1/songs?q=old'));
  await page.getByLabel('Search music library').fill('new');
  await expect(page.locator('.song-title')).toHaveText('new');
  await page.waitForResponse(r => r.url().endsWith('/v1/songs?q=old'));
  await expect(page.locator('.song-title')).toHaveText('new');
});

test('a busy action button keeps keyboard focus and ignores repeat presses', async ({ page }) => {
  let release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  let posts = 0;
  await page.routeWebSocket(/\/ws(\?.*)?$/, () => {});
  await page.route('**/v1/**', async route => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/v1/queue' && route.request().method() === 'POST') { posts++; await gate; }
    return route.fulfill({ json: path === '/v1/songs' ? {songs: [song]} : path === '/v1/queue' ? {items: []} : {current_song: song} });
  });
  await page.goto('/admin');
  const add = page.getByRole('button', {name: 'Add Midnight compiler to queue'});
  await add.focus();
  await page.keyboard.press('Enter');
  await expect(add).toHaveAttribute('aria-disabled', 'true');
  await expect(add).toBeFocused();
  expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()).violations).toEqual([]);
  // The axe scan itself moves focus to <body>; put it back before the repeat press.
  await add.focus();
  await page.keyboard.press('Enter');
  await expect(add).toBeFocused();
  expect(posts).toBe(1);
  release();
  await expect(page.getByRole('status')).toContainText('Queued: Midnight compiler');
  await expect(add).not.toHaveAttribute('aria-disabled');
  await expect(add).toBeFocused();
  expect(posts).toBe(1);
});

test('a repeated status message is inserted again so screen readers re-announce it', async ({ page }) => {
  const second = { ...song, id: 'b', title: 'Buffering dreams', filename: 'b.mp3' };
  const items = [song, second].map((s, i) => ({ id: `q${s.id}`, song_id: s.id, song: s, position: i + 1, source: 'manual' }));
  await page.routeWebSocket(/\/ws(\?.*)?$/, () => {});
  await page.route('**/v1/**', route => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith(':move')) return route.fulfill({ json: { moved: true } });
    return route.fulfill({ json: path === '/v1/songs' ? {songs: [song]} : path === '/v1/queue' ? {items} : {current_song: song} });
  });
  await page.goto('/admin');
  await expect(page.locator('.queue-item')).toHaveCount(2);
  await page.evaluate(() => {
    const status = document.querySelector<HTMLElement>('[role="status"]')!;
    status.dataset.notices = '0';
    new MutationObserver(records => {
      const added = records.flatMap(r => Array.from(r.addedNodes)).filter(n => n instanceof Element && n.matches('.notification')).length;
      status.dataset.notices = String(Number(status.dataset.notices) + added);
    }).observe(status, { childList: true, subtree: true });
  });
  const moveUp = page.getByRole('button', {name: 'Move Buffering dreams up'});
  for (let press = 0; press < 2; press++) {
    await moveUp.click();
    await expect(page.getByRole('status')).toContainText('Queue order updated.');
    await expect(moveUp).not.toHaveAttribute('aria-disabled');
  }
  await expect.poll(() => page.getByRole('status').evaluate(el => Number(el.dataset.notices))).toBeGreaterThanOrEqual(2);
});

test('Reset clears the search and keeps focus in the search box', async ({ page }) => {
  await page.routeWebSocket(/\/ws(\?.*)?$/, () => {});
  await page.route('**/v1/**', route => route.fulfill({ json: { songs: [song], items: [], current_song: null } }));
  await page.goto('/admin');
  const search = page.getByLabel('Search music library');
  await search.fill('Midnight');
  await page.getByRole('button', {name: 'Reset', exact: true}).click();
  await expect(search).toHaveValue('');
  await expect(search).toBeFocused();
});

test('each Retry moves focus to its panel heading, not to the page body', async ({ page }) => {
  let fail = true;
  let served = 0;
  await page.routeWebSocket(/\/ws(\?.*)?$/, () => {});
  await page.route('**/v1/**', route => {
    served++;
    const path = new URL(route.request().url()).pathname;
    if (fail && ['/v1/songs', '/v1/queue', '/v1/history'].includes(path)) return route.fulfill({ status: 503, json: {} });
    return route.fulfill({ json: path === '/v1/songs' ? {songs: [song]} : path === '/v1/player' ? {current_song: song} : {items: []} });
  });
  await page.goto('/admin');
  await expect(page.getByText('Library unavailable', { exact: true })).toBeVisible();
  await expect(page.getByText('Queue unavailable', { exact: true })).toBeVisible();
  await expect(page.getByText('History unavailable', { exact: true })).toBeVisible();
  await expect(page.getByText('Backend connected')).toBeVisible();
  // Same 10 startup requests as above; a late one served after the flip would hide a Retry button.
  await expect.poll(() => served).toBe(10);
  fail = false;
  await page.getByRole('button', {name: 'Retry connection'}).click();
  await expect(page.locator('#library-heading')).toBeFocused();
  await expect(page.locator('.song-item')).toHaveCount(1);
  await page.getByRole('region', {name: 'Up next'}).getByRole('button', {name: 'Retry'}).click();
  await expect(page.locator('#queue-heading')).toBeFocused();
  await expect(page.getByText('Make the next move')).toBeVisible();
  await page.getByRole('region', {name: 'Recently played'}).getByRole('button', {name: 'Retry'}).click();
  await expect(page.locator('#history-heading')).toBeFocused();
  await expect(page.getByText('No songs played yet.')).toBeVisible();
  expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()).violations).toEqual([]);
});

test('connection changes are announced once each, and the first connect is silent', async ({ page, context }) => {
  const sockets: { close: () => Promise<void> }[] = [];
  let refused = 0;
  let refuse = false;
  await page.routeWebSocket(/\/ws(\?.*)?$/, ws => {
    // No backend listens on the real URL, so passing through fails the socket.
    if (refuse) { refused++; ws.connectToServer(); return; }
    sockets.push(ws);
  });
  await page.route('**/v1/**', route => route.fulfill({ json: { songs: [song], items: [], current_song: song } }));
  await page.goto('/admin');
  const label = page.locator('.connection-box .status');
  const status = page.getByRole('status');
  await expect(label).toHaveText('Backend connected');
  await page.evaluate(() => {
    const region = document.querySelector<HTMLElement>('[role="status"]')!;
    region.dataset.notices = '0';
    new MutationObserver(records => {
      const added = records.flatMap(r => Array.from(r.addedNodes)).filter(n => n instanceof Element && n.matches('.notification')).length;
      region.dataset.notices = String(Number(region.dataset.notices) + added);
    }).observe(region, { childList: true, subtree: true });
  });
  const notices = () => status.evaluate(el => Number(el.dataset.notices));
  await expect(status).not.toContainText('Backend connected');

  await sockets.at(-1)!.close();
  await expect(status).toContainText('Backend connection lost. Reconnecting.');
  await expect(label).toHaveText('Reconnecting to backend');
  // The first retry waits reconnectDelay(0), under a second.
  await expect(status).toContainText('Backend connected. Live sync resumed.', { timeout: 10000 });
  await expect(label).toHaveText('Backend connected');
  expect(await notices()).toBe(2);

  await context.setOffline(true);
  refuse = true;
  await sockets.at(-1)!.close();
  await expect(status).toContainText('Backend offline. Waiting for the network.');
  await expect(label).toHaveText('Backend offline');
  // Three failed retries (0.9 + 1.8 + 3.6 s) push the next backoff past the
  // expect timeout, so only the online handler can reconnect in time.
  await expect.poll(() => refused, { timeout: 20000 }).toBe(3);
  await expect(label).toHaveText('Backend offline');
  refuse = false;
  await context.setOffline(false);
  await expect(status).toContainText('Backend connected. Live sync resumed.');
  await expect(label).toHaveText('Backend connected');
  expect(await notices()).toBe(4);
});
