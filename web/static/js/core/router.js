// router.js — hash routes, including filter state (#library?shelf=backlog
// &q=zel). parse() and href() are pure; start() wires hashchange.

const LEGACY = {
  '': 'home',
  now: 'home',
  'now-playing': 'home',
  'playing-now': 'home',
  playing: 'home',
  stats: 'you/stats',
  settings: 'you/settings',
};
const STATUS_HASHES = ['wishlist', 'backlog', 'completed', 'abandoned', 'paused'];

// parse turns a location hash into {name, id, query, redirect}. redirect is
// set when the hash is a legacy alias that should be replaced.
export function parse(hash) {
  let raw = String(hash || '').replace(/^#/, '');
  let qs = '';
  const qi = raw.indexOf('?');
  if (qi >= 0) {
    qs = raw.slice(qi + 1);
    raw = raw.slice(0, qi);
  }
  const query = Object.fromEntries(new URLSearchParams(qs));
  let path = raw.replace(/\/+$/, '');
  let redirect = null;

  if (path in LEGACY) {
    const target = LEGACY[path];
    if (path !== '') redirect = '#' + target;
    path = target;
  }
  if (STATUS_HASHES.includes(path) || path === 'library/playing') {
    const shelf = path === 'library/playing' ? 'playing' : path;
    return { name: 'library', query: { shelf }, redirect: href('library', { shelf }) };
  }
  if (path === 'search') {
    return { name: 'library', query: {}, focusSearch: true, redirect: '#library' };
  }
  if (path.startsWith('search/')) {
    let q = path.slice('search/'.length);
    try { q = decodeURIComponent(q); } catch { /* keep the raw text */ }
    return { name: 'library', query: { q }, redirect: href('library', { q }) };
  }
  const game = /^game\/(-?\d+)$/.exec(path);
  if (game) return { name: 'game', id: Number(game[1]), query, redirect };
  const user = /^u\/([A-Za-z0-9_.-]{1,32})$/.exec(path);
  if (user) return { name: 'profile', id: user[1], query, redirect };
  const list = /^list\/(\d+)$/.exec(path);
  if (list) return { name: 'list', id: Number(list[1]), query, redirect };
  const year = /^you\/year(?:\/(\d{4}))?$/.exec(path);
  if (year) return { name: 'you/year', id: year[1] ? Number(year[1]) : null, query, redirect };

  const known = ['home', 'library', 'log', 'discover', 'discover/search', 'you', 'you/stats', 'you/diary',
    'you/settings', 'you/help', 'you/lists', 'you/goals', 'you/import', 'you/people', 'you/inbox', 'admin', 'dev/ui'];
  if (known.includes(path)) return { name: path, query, redirect };
  return { name: 'home', query: {}, redirect: '#home' };
}

// href builds a hash for a route name and query (empty values dropped).
export function href(name, query = {}, id = null) {
  let path = name;
  if (name === 'game' || name === 'profile' || name === 'list') path = `${name === 'profile' ? 'u' : name}/${id}`;
  if (name === 'you/year' && id) path = `you/year/${id}`;
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) {
    if (v !== undefined && v !== null && v !== '' && !(Array.isArray(v) && !v.length)) params.set(k, String(v));
  }
  const qs = params.toString();
  return '#' + path + (qs ? '?' + qs : '');
}

// tab is the bottom-navigation section a route belongs to.
export function tab(name) {
  if (name === 'home') return 'home';
  if (name === 'library' || name === 'game') return 'library';
  if (name.startsWith('discover')) return 'discover';
  if (name.startsWith('you') || name === 'profile' || name === 'list' || name === 'admin') return 'you';
  return '';
}

export function start(onRoute) {
  const handle = () => {
    const route = parse(location.hash);
    if (route.redirect && route.redirect !== location.hash) {
      history.replaceState(history.state, '', location.pathname + location.search + route.redirect);
    }
    onRoute(route);
  };
  addEventListener('hashchange', handle);
  handle();
  return handle;
}

export function go(hash, { replace = false } = {}) {
  if (replace) {
    history.replaceState(history.state, '', location.pathname + location.search + hash);
    dispatchEvent(new HashChangeEvent('hashchange'));
  } else if (location.hash !== hash) {
    location.hash = hash;
  } else {
    dispatchEvent(new HashChangeEvent('hashchange'));
  }
}
