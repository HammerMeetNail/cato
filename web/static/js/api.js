const BASE = '';

async function request(method, path, body = null, opts = {}) {
  const { data } = await requestFull(method, path, body, opts);
  return data;
}

// requestFull returns the parsed body alongside the Response so callers that
// need headers (e.g. pagination metadata) don't have to duplicate the
// CSRF/error plumbing.
async function requestFull(method, path, body = null, opts = {}) {
  const fetchOpts = {
    method,
    credentials: 'include',
    headers: {},
  };

  if (opts.signal) fetchOpts.signal = opts.signal;

  if (body) {
    fetchOpts.headers['Content-Type'] = 'application/json';
    fetchOpts.body = JSON.stringify(body);
  }

  const csrf = getCSRF();
  if (csrf && method !== 'GET') {
    fetchOpts.headers['X-CSRF-Token'] = csrf;
  }

  const res = await fetch(BASE + path, fetchOpts);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const err = new Error(data.message || `HTTP ${res.status}`);
    err.code = data.error;
    err.status = res.status;
    throw err;
  }
  return { data, res };
}

// The CSRF token is kept in memory only. It used to also be mirrored into
// localStorage, which turned any XSS into a full session takeover (the token
// authorizes all unsafe requests). In-memory costs nothing: checkAuth()
// refreshes it on every page load.
let csrfToken = null;

function getCSRF() {
  return csrfToken;
}

function setCSRF(token) {
  csrfToken = token;
}

export const api = {
  get(path, opts) { return request('GET', path, null, opts); },
  post(path, body) { return request('POST', path, body); },
  patch(path, body) { return request('PATCH', path, body); },
  del(path) { return request('DELETE', path); },
  getFull(path, opts) { return requestFull('GET', path, null, opts); },
  setCSRF,
  getCSRF,
};

export async function checkAuth() {
  try {
    const data = await api.get('/api/me');
    if (data.authenticated) {
      api.setCSRF(data.csrf_token);
    }
    return data;
  } catch {
    return { authenticated: false };
  }
}

export async function login(email, password) {
  const data = await api.post('/api/auth/login', { email, password });
  api.setCSRF(data.csrf_token);
  return data;
}

export async function signup(email, password) {
  const data = await api.post('/api/auth/signup', { email, password });
  api.setCSRF(data.csrf_token);
  return data;
}

export async function logout() {
  await api.post('/api/auth/logout');
  api.setCSRF(null);
}

// updateMe patches the caller's profile (currently display_name only).
export function updateMe(data) {
  return api.patch('/api/me', data);
}

export async function getGame(id) {
  return api.get(`/api/games/${id}`);
}

export async function searchGames(query, signal) {
  if (!query || query.length < 2) return [];
  return api.get(`/api/games/search?q=${encodeURIComponent(query)}`, { signal });
}

// searchGamesFull fetches a page of full search results plus the total match
// count (X-Total-Count header). opts: limit, offset, sort ('relevance' |
// 'release_new' | 'release_old' | 'rating' | 'popularity' | 'name'),
// yearFrom/yearTo (numbers), minRating (number).
export async function searchGamesFull(query, {
  limit = 24, offset = 0,
  sort = '', yearFrom = null, yearTo = null, minRating = null,
  signal,
} = {}) {
  if (!query || query.length < 2) return { results: [], total: 0 };
  const params = new URLSearchParams();
  params.append('q', query);
  params.append('full', '1');
  params.append('limit', limit);
  params.append('offset', offset);
  if (sort && sort !== 'relevance') params.append('sort', sort);
  if (yearFrom) params.append('year_from', yearFrom);
  if (yearTo) params.append('year_to', yearTo);
  if (minRating) params.append('min_rating', minRating);
  const { data, res } = await api.getFull(`/api/games/search?${params.toString()}`, { signal });
  return {
    results: data,
    total: Number(res.headers.get('X-Total-Count')) || data.length,
  };
}

// parseTagQuery parses a raw tag filter string into { tags, op }.
// Tags containing spaces or pipes must be double-quoted ("Switch 2").
// Outside quotes: space = AND, pipe = OR (any pipe => op 'or').
export function parseTagQuery(raw) {
  const tags = [];
  let op = 'and';
  let cur = '';
  let quoted = false;
  const push = () => {
    const t = cur.trim();
    if (t) tags.push(t);
    cur = '';
  };
  for (const ch of raw) {
    if (ch === '"') {
      quoted = !quoted;
      continue;
    }
    if (!quoted && ch === '|') {
      op = 'or';
      push();
      continue;
    }
    if (!quoted && /\s/.test(ch)) {
      if (op !== 'or') push();
      else cur += ch;
      continue;
    }
    cur += ch;
  }
  push();
  return { tags, op };
}

// formatTagForQuery quotes a tag when needed so it survives round-tripping
// through a tag query string (spaces/pipes would otherwise split it).
export function formatTagForQuery(tag) {
  return /[\s|"]/.test(tag) ? `"${tag.replace(/"/g, '')}"` : tag;
}

export async function autocompleteTags(prefix) {
  if (!prefix || prefix.length < 1) return [];
  return api.get(`/api/library/tags?q=${encodeURIComponent(prefix)}`);
}

export function getCoverURL(game) {
  if (game.local_cover_path) return game.local_cover_path;
  if (game.cover_url) return game.cover_url;
  return '/covers/placeholder.jpg';
}

// getCoverThumbnailURL returns a smaller image URL suitable for compact
// display contexts like the search dropdown (48×64 px rendered size).
// For remote IGDB URLs it substitutes the t_cover_big size (264×374 px)
// with t_thumb (96×128 px), cutting transfer size ~8×. Locally cached
// covers and placeholders are returned as-is.
export function getCoverThumbnailURL(game) {
  const url = getCoverURL(game);
  if (url.startsWith('https://images.igdb.com/') && url.includes('/t_cover_big/')) {
    return url.replace('/t_cover_big/', '/t_thumb/');
  }
  return url;
}

export const library = {
  // list returns { items, total, hasMore }. total/hasMore come from the
  // X-Total-Count / X-Has-More response headers; hasMore is exact even when
  // the item count is a multiple of the page size.
  async list(status, limit = 60, offset = 0, tag = '') {
    const params = new URLSearchParams();
    if (status) params.append('status', status);
    if (tag) {
      const { tags: tagList, op } = parseTagQuery(tag);
      if (op === 'or') params.append('tag_op', 'or');
      for (const t of tagList) {
        params.append('tag', t);
      }
    }
    params.append('limit', limit);
    params.append('offset', offset);
    const qs = params.toString() ? `?${params.toString()}` : '';
    const { data, res } = await api.getFull(`/api/library${qs}`);
    return {
      items: data,
      total: Number(res.headers.get('X-Total-Count')) || data.length,
      hasMore: res.headers.get('X-Has-More') === 'true',
    };
  },

  // get fetches a single library item by game ID, or throws (404) when the
  // game is not in the library.
  get(gameID) {
    return api.get(`/api/library/${gameID}`);
  },

  // check returns the subset of the given game IDs that are in the library.
  async check(ids) {
    if (!ids || ids.length === 0) return [];
    try {
      return await api.get(`/api/library/check?ids=${ids.map(Number).join(',')}`);
    } catch {
      return [];
    }
  },

  // counts returns per-status item counts, e.g. { all: 43, backlog: 20, ... }.
  async counts() {
    try {
      return await api.get('/api/library/counts');
    } catch {
      return null;
    }
  },

  // suggestions returns popular catalog games not yet in the library,
  // covers only — used to seed a fresh collection.
  async suggestions(limit = 8) {
    try {
      return await api.get(`/api/library/suggestions?limit=${limit}`);
    } catch {
      return [];
    }
  },

  add(gameID, data) {
    return api.post(`/api/library/${gameID}`, data);
  },

  // patch partially updates a library item — absent fields keep their stored
  // values, so quick status changes can't wipe rating/tags/notes. Pass
  // playtime_delta_minutes to add time without read-modify-write races.
  // Resolves with the updated item JSON.
  patch(gameID, data) {
    return api.patch(`/api/library/${gameID}`, data);
  },

  remove(gameID) {
    return api.del(`/api/library/${gameID}`);
  },
};
