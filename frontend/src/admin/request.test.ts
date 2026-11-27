/* Tests for the admin REST helpers.
 *
 * Run with:  npm test          (node --test, no extra dependencies)
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fetchWithTimeout, errorText, HttpError, REQUEST_TIMEOUT_MS } from './request.ts';

// hangingFetch never settles on its own; like fetch, it rejects with the
// signal's reason once the signal aborts (or at once if it already has).
const hangingFetch: typeof fetch = (_input, init) => new Promise((_resolve, reject) => {
  const signal = init?.signal;
  if (signal?.aborted) { reject(signal.reason); return; }
  signal?.addEventListener('abort', () => reject(signal.reason), { once: true });
});

test('a hung request rejects with TimeoutError after REQUEST_TIMEOUT_MS', async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  let settled = false;
  const pending = fetchWithTimeout(hangingFetch, '/v1/queue', undefined, REQUEST_TIMEOUT_MS);
  pending.catch(() => {}).finally(() => { settled = true; });
  t.mock.timers.tick(REQUEST_TIMEOUT_MS - 1);
  await Promise.resolve();
  assert.equal(settled, false);
  t.mock.timers.tick(1);
  await assert.rejects(pending, (err: unknown) => err instanceof DOMException && err.name === 'TimeoutError');
});

test('a fast response is returned and leaves no timer behind', async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  let signal: AbortSignal | undefined;
  const fastFetch: typeof fetch = async (_input, init) => {
    signal = init?.signal ?? undefined;
    return new Response('{}', { status: 200 });
  };
  const res = await fetchWithTimeout(fastFetch, '/v1/player', undefined, REQUEST_TIMEOUT_MS);
  assert.equal(res.status, 200);
  t.mock.timers.tick(REQUEST_TIMEOUT_MS * 2);
  assert.equal(signal?.aborted, false);
});

test('a caller abort rejects with the caller AbortError, not TimeoutError', async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const caller = new AbortController();
  const pending = fetchWithTimeout(hangingFetch, '/v1/songs', { signal: caller.signal }, REQUEST_TIMEOUT_MS);
  caller.abort();
  await assert.rejects(pending, (err: unknown) => err instanceof DOMException && err.name === 'AbortError');
});

test('an already aborted caller signal rejects at once with its reason', async () => {
  const caller = new AbortController();
  caller.abort();
  await assert.rejects(
    fetchWithTimeout(hangingFetch, '/v1/songs', { signal: caller.signal }, REQUEST_TIMEOUT_MS),
    (err: unknown) => err instanceof DOMException && err.name === 'AbortError',
  );
});

test('errorText keeps the backend reason of a 4xx response', async () => {
  const res = new Response(JSON.stringify({ error: ' queue is empty ' }), { status: 409 });
  const err = new HttpError(res.status, await errorText(res));
  assert.equal(err.status, 409);
  assert.equal(err.message, 'queue is empty');
});

test('errorText is empty when the body is not the error shape', async () => {
  assert.equal(await errorText(new Response('not json', { status: 400 })), '');
  assert.equal(await errorText(new Response('{"error":5}', { status: 400 })), '');
});
