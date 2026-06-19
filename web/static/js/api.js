const BASE = '';

async function request(method, path, body = null) {
  const opts = {
    method,
    credentials: 'include',
    headers: {},
  };

  if (body) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }

  const csrf = getCSRF();
  if (csrf && method !== 'GET') {
    opts.headers['X-CSRF-Token'] = csrf;
  }

  const res = await fetch(BASE + path, opts);
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
  get(path) { return request('GET', path); },
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

export async function searchGames(query) {
  if (!query || query.length < 2) return [];
  return api.get(`/api/games/search?q=${encodeURIComponent(query)}`);
}

export function getCoverURL(game) {
  if (game.local_cover_path) return game.local_cover_path;
  if (game.cover_url) return game.cover_url;
  return '/covers/placeholder.jpg';
}

export const library = {
  list(status) {
    const qs = status ? `?status=${encodeURIComponent(status)}` : '';
    return api.get(`/api/library${qs}`);
  },

  add(gameID, data) {
    return api.post(`/api/library/${gameID}`, data);
  },

  remove(gameID) {
    return api.del(`/api/library/${gameID}`);
  },
};
