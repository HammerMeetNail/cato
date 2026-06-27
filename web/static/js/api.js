const BASE = '';

async function request(method, path, body = null, opts = {}) {
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
  return data;
}

let csrfToken = null;

function getCSRF() {
  return csrfToken || localStorage.getItem('cato_csrf');
}

function setCSRF(token) {
  csrfToken = token;
  if (token) {
    localStorage.setItem('cato_csrf', token);
  } else {
    localStorage.removeItem('cato_csrf');
  }
}

export const api = {
  get(path, opts) { return request('GET', path, null, opts); },
  post(path, body) { return request('POST', path, body); },
  del(path) { return request('DELETE', path); },
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

export async function getGame(id) {
  return api.get(`/api/games/${id}`);
}

export async function searchGames(query, signal) {
  if (!query || query.length < 2) return [];
  return api.get(`/api/games/search?q=${encodeURIComponent(query)}`, { signal });
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
  list(status, limit = 60, offset = 0) {
    const params = new URLSearchParams();
    if (status) params.append('status', status);
    params.append('limit', limit);
    params.append('offset', offset);
    const qs = params.toString() ? `?${params.toString()}` : '';
    return api.get(`/api/library${qs}`);
  },

  add(gameID, data) {
    return api.post(`/api/library/${gameID}`, data);
  },

  remove(gameID) {
    return api.del(`/api/library/${gameID}`);
  },
};
