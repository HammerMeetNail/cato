// api.js — the transport: JSON over fetch with the CSRF header, timeouts,
// conditional GETs and typed errors. No DOM.

export class ApiError extends Error {
  constructor(status, code, message, fields = null) {
    super(message || code || `HTTP ${status}`);
    this.status = status;
    this.code = code;
    this.fields = fields;
  }

  // retryable: worth sending again later (offline, timeouts, overload).
  get retryable() {
    return this.status === 0 || this.status === 408 || this.status === 429 || this.status >= 500;
  }
}

let csrfToken = null;
export function setCSRF(token) { csrfToken = token; }
export function getCSRF() { return csrfToken; }

// request sends one API call. Options: body, signal, etag (If-None-Match),
// timeout (ms). Resolves {status, data, etag}; a 304 has no data.
export async function request(method, path, { body, signal, etag, timeout = 15000, fetchImpl = globalThis.fetch } = {}) {
  const headers = { Accept: 'application/json' };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  if (method !== 'GET' && csrfToken) headers['X-CSRF-Token'] = csrfToken;
  if (etag) headers['If-None-Match'] = etag;
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(new DOMException('timeout', 'TimeoutError')), timeout);
  const onAbort = () => ctl.abort(signal.reason);
  signal?.addEventListener('abort', onAbort, { once: true });
  let res;
  try {
    res = await fetchImpl(path, {
      method,
      headers,
      credentials: 'same-origin',
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: ctl.signal,
    });
  } catch (err) {
    if (signal?.aborted) throw err;
    throw new ApiError(0, 'network', navigator?.onLine === false ? "You're offline" : "Can't reach Cato");
  } finally {
    clearTimeout(timer);
    signal?.removeEventListener('abort', onAbort);
  }
  if (res.status === 304) return { status: 304, data: null, etag: res.headers.get('ETag') };
  let data = null;
  const text = await res.text();
  if (text) {
    try { data = JSON.parse(text); } catch { data = null; }
  }
  if (!res.ok) {
    if (res.status === 401 && typeof dispatchEvent === 'function') {
      dispatchEvent(new CustomEvent('cato:unauthorized'));
    }
    throw new ApiError(res.status, data?.error ?? 'http_error', data?.message ?? `HTTP ${res.status}`, data?.fields ?? null);
  }
  return { status: res.status, data, etag: res.headers.get('ETag') };
}

const call = method => async (path, body, opts) => (await request(method, path, { ...opts, body })).data;

export const api = {
  get: async (path, opts) => (await request('GET', path, opts)).data,
  put: call('PUT'),
  patch: call('PATCH'),
  post: call('POST'),
  del: call('DELETE'),
  request,
};
