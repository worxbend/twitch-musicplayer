/* Admin REST helpers: a fetch that gives up after a deadline, so one hung
 * request (stalled proxy, half-open TCP) cannot lock the console's controls,
 * and HttpError, which keeps the backend's {"error": msg} reason.
 */
export const REQUEST_TIMEOUT_MS = 10_000;

// HttpError keeps the status and the backend's {"error": msg} text.
// A plain field, not a parameter property: node --test strips types only.
export class HttpError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

export async function errorText(res: Response): Promise<string> {
  try {
    const body: unknown = await res.json();
    const text = (body as { error?: unknown } | null)?.error;
    return typeof text === 'string' ? text.trim() : '';
  } catch { return ''; }
}

// fetchWithTimeout rejects with a DOMException named 'TimeoutError' after ms.
// A caller's init.signal still aborts the request with its own reason.
export async function fetchWithTimeout(fetchFn: typeof fetch, url: string, init: RequestInit | undefined, ms: number): Promise<Response> {
  const controller = new AbortController();
  const callerSignal = init?.signal;
  const onCallerAbort = () => controller.abort(callerSignal?.reason);
  if (callerSignal?.aborted) onCallerAbort();
  else callerSignal?.addEventListener('abort', onCallerAbort, { once: true });
  const timer = setTimeout(() => controller.abort(new DOMException('Request timed out', 'TimeoutError')), ms);
  try {
    return await fetchFn(url, { ...init, signal: controller.signal });
  } catch (err) {
    // Browsers reject with the abort reason; fall back to it if a fetch does not.
    throw controller.signal.aborted ? controller.signal.reason : err;
  } finally {
    clearTimeout(timer);
    callerSignal?.removeEventListener('abort', onCallerAbort);
  }
}
