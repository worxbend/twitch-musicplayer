import { test, expect } from '@playwright/test';

const song = { id: 'a', title: 'Midnight compiler', filename: 'a.mp3', artist: 'Soft Circuit', album: 'After hours', duration_secs: 180 };

test('keyboard skip link, shared navigation, and visible focus', async ({ page }) => {
  await page.routeWebSocket('**/ws', () => {});
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
  await page.routeWebSocket('**/ws', () => {});
  await page.route('**/v1/**', route => {
    if (fail) return route.fulfill({ status: 503, json: {} });
    const path = new URL(route.request().url()).pathname;
    return route.fulfill({ json: path === '/v1/songs' ? {songs: [song]} : path === '/v1/queue' ? {items: []} : {current_song: song} });
  });
  await page.goto('/admin');
  await expect(page.locator('.song-item')).toHaveCount(1);
  fail = true;
  await page.getByRole('button', {name: 'Add Midnight compiler to queue'}).click();
  await expect(page.getByRole('status')).toContainText('Action failed');
  await expect(page.locator('.notification')).toHaveClass(/notification--error/);
  await expect(page.getByRole('button', {name: 'Add Midnight compiler to queue'})).toBeEnabled();
  await expect(page.locator('.song-item')).toHaveCount(1);
  fail = false;
  await page.getByRole('button', {name: 'Add Midnight compiler to queue'}).click();
  await expect(page.getByRole('status')).toContainText('Queued: Midnight compiler');
});

test('a slow previous search never overwrites a newer result', async ({ page }) => {
  await page.routeWebSocket('**/ws', () => {});
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
