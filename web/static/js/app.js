// app.js — boots Cato: who's signed in, the on-device library, the router
// and the shell (header with the one search box, bottom navigation on
// phones / sidebar on desktop). Home and the Log sheet load eagerly; other
// screens load on first visit.
import { html, render, nothing } from './ui/lit.js';
import { icon } from './ui/icons.js';
import { skeletonTiles, empty } from './ui/parts.js';
import { toast, prepareToasts } from './ui/toast.js';
import { anySheetOpen, closeAllSheets } from './ui/sheet.js';
import { closeMenu } from './ui/menu.js';
import { api, request, setCSRF, getCSRF, ApiError } from './core/api.js';
import { openKV } from './core/idb.js';
import { Store } from './core/store.js';
import { start as startRouter, tab, go } from './core/router.js';
import { initials, plural } from './core/format.js';
import { app, applyTheme } from './features/app-state.js';
import { homeView, enterHome, beginHomeVisit } from './features/home.js';
import { openLogSheet } from './features/log.js';
import { timerPill, refreshTimer } from './features/timer.js';
import { searchBox, focusSearch } from './features/search-box.js';

// Trusted Types (CSP require-trusted-types-for 'script'): registering the
// service worker needs a TrustedScriptURL, from a policy that allows only it.
const swURL = window.trustedTypes
  ? window.trustedTypes.createPolicy('cato-sw', {
    createScriptURL: (u) => { if (u !== '/service-worker.js') throw new TypeError('blocked script URL'); return u; },
  }).createScriptURL('/service-worker.js')
  : '/service-worker.js';

// Screens other than Home load on first visit. Each module exports a
// view(route) and optionally enter(route) (data loading) and init(app).
const SCREENS = {
  library: () => import('./features/library.js').then(m => ({ view: m.libraryView })),
  game: () => import('./features/game.js').then(m => ({ view: m.gameView, enter: m.enterGame, init: () => m.watchGameWrites(app.store) })),
  discover: () => import('./features/discover.js').then(m => ({ view: m.discoverView, enter: m.enterDiscover })),
  'discover/search': () => import('./features/discover.js').then(m => ({ view: m.searchView, enter: r => m.enterSearch(r) })),
  you: () => import('./features/you.js').then(m => ({ view: m.youView })),
  'you/stats': () => import('./features/you.js').then(m => ({ view: m.statsView, enter: m.enterStats })),
  'you/diary': () => import('./features/you.js').then(m => ({ view: m.diaryView, enter: m.enterDiary })),
  'you/settings': () => import('./features/settings.js').then(m => ({ view: m.settingsView, enter: m.enterSettings })),
  'you/help': () => import('./features/help.js').then(m => ({ view: m.helpView })),
  profile: () => import('./features/people.js').then(m => ({ view: m.profileView, enter: m.enterProfile })),
  list: () => import('./features/people.js').then(m => ({ view: m.listView, enter: m.enterList })),
  'you/lists': () => import('./features/people.js').then(m => ({ view: m.myListsView, enter: m.enterMyLists })),
  'you/people': () => import('./features/people.js').then(m => ({ view: m.peopleView, enter: m.enterPeople })),
  'you/inbox': () => import('./features/people.js').then(m => ({ view: m.inboxView, enter: m.enterInbox })),
  admin: () => import('./features/admin.js').then(m => ({ view: m.adminView, enter: m.enterAdmin })),
  'dev/ui': () => import('./dev/gallery.js').then(m => ({ view: m.galleryView })),
};

// registerScreen lets later features add routes (lists, profiles, import…).
export function registerScreen(name, loader) {
  SCREENS[name] = loader;
}

const loaded = new Map();
const TITLES = {
  home: 'Home', library: 'Library', game: 'Game', discover: 'Discover', 'discover/search': 'Search', you: 'You',
  'you/stats': 'Stats', 'you/diary': 'Diary', 'you/settings': 'Settings', 'you/help': 'Help', 'dev/ui': 'Gallery',
  profile: 'Profile', list: 'List', 'you/lists': 'Lists', 'you/people': 'People', 'you/inbox': 'Inbox', admin: 'Admin',
};

let route = { name: 'home', query: {} };
let firstRoute = true;
const scroll = new Map();

function screenFor(r) {
  if (r.name === 'home' || r.name === 'log') return { view: homeView };
  return loaded.get(r.name) ?? null;
}

function navItem(name, label, iconName, badge = 0) {
  const current = tab(route.name) === name;
  return html`<a class="nav-item" href=${`#${name}`} aria-current=${current ? 'page' : nothing} data-nav=${name}
    aria-label=${badge ? `${label}, ${plural(badge, 'new notification')}` : nothing}>
    ${icon(iconName)}<span>${label}</span>${badge ? html`<span class="badge num" aria-hidden="true">${badge > 99 ? '99+' : badge}</span>` : nothing}</a>`;
}

function statusLine() {
  const pending = app.store?.pending ?? 0;
  // Online, a write normally lands within a request: only say "Saving"
  // once one had to be retried, instead of flashing it on every tap.
  const retrying = (app.store?.retryDelay ?? 0) > 2000;
  if (app.online && (!pending || !retrying)) return nothing;
  return html`<span class="timer-pill" style="background:var(--surface-2);color:var(--text-muted)" role="status">
    ${icon(app.online ? 'refresh-cw' : 'wifi-off')}${app.online ? `Saving ${plural(pending, 'change')}` : pending ? `Offline · ${plural(pending, 'change')} waiting` : 'Offline'}</span>`;
}

function shell(content) {
  const name = app.me?.display_name || app.me?.email || '';
  return html`<a class="skip-link" href="#main" @click=${e => { e.preventDefault(); document.getElementById('main')?.focus(); }}>Skip to content</a>
  <div class="app">
    <header class="topbar">
      ${searchBox(route)}
      ${statusLine()}
      ${timerPill()}
      <a class="avatar" href="#you" aria-label="You" title=${name}>${app.me?.avatar ? html`<img src=${app.me.avatar} alt="" referrerpolicy="no-referrer">` : initials(name)}</a>
    </header>
    <main id="main" class="main" tabindex="-1">${content}</main>
    <nav class="nav" aria-label="Main">
      <a class="brand-side" href="#home"><img src="/favicon.svg" alt="" width="28" height="28">Cato</a>
      ${navItem('home', 'Home', 'house')}
      ${navItem('library', 'Library', 'layout-grid')}
      <button type="button" class="nav-item nav-log" aria-label="Log a game" @click=${() => openLogSheet()}>
        <span class="log-disc">${icon('plus')}</span><span class="nav-label-desktop">Log a game</span></button>
      ${navItem('discover', 'Discover', 'compass')}
      ${navItem('you', 'You', 'user', app.unread)}
    </nav>
  </div>`;
}

function draw() {
  const screen = screenFor(route);
  let content;
  if (!screen) content = html`<div class="page">${skeletonTiles(6)}</div>`;
  else {
    try {
      content = screen.view(route);
    } catch (err) {
      console.error(err);
      content = html`<div class="page">${empty({ iconName: 'triangle-alert', title: 'Something went wrong', body: String(err.message || err) })}</div>`;
    }
  }
  const root = document.getElementById('app');
  // The static first-paint skeleton in index.html isn't lit's to manage.
  if (!root.dataset.rendered) {
    root.replaceChildren();
    root.dataset.rendered = '1';
  }
  render(shell(content), root);
}
app.redraw = () => {
  if (redrawQueued) return;
  redrawQueued = true;
  queueMicrotask(() => { redrawQueued = false; draw(); });
};
let redrawQueued = false;

async function onRoute(next) {
  const main = document.getElementById('main');
  if (main) scroll.set(route.name + JSON.stringify(route.query) + (route.id ?? ''), main.scrollTop);
  closeMenu();
  if (!anySheetOpen() || next.name !== route.name) closeAllSheets();
  // A filter change (same screen, new query) is not a navigation: focus
  // stays where it is, e.g. in the Library search box while typing.
  const navigated = next.name !== route.name || next.id !== route.id;
  route = next;
  if (navigated && next.name === 'home') beginHomeVisit();
  document.title = `${TITLES[next.name] ?? 'Cato'} · Cato`;
  if (next.name === 'log') {
    openLogSheet(next.query.q || '');
    go('#home', { replace: true });
    return;
  }
  if (!screenFor(next) && SCREENS[next.name]) {
    draw();
    try {
      const mod = await SCREENS[next.name]();
      mod.init?.();
      loaded.set(next.name, mod);
    } catch (err) {
      toast("Couldn't load that screen. Check your connection.", { kind: 'error' });
      return;
    }
    if (route !== next) return;
  }
  draw();
  const key = next.name + JSON.stringify(next.query) + (next.id ?? '');
  const m = document.getElementById('main');
  if (m && navigated) {
    m.scrollTop = scroll.get(key) ?? 0;
    if (!firstRoute) m.focus({ preventScroll: true });
  }
  firstRoute = false;
  // After the (possibly lazily loaded) screen has rendered its search box.
  if (next.focusSearch) focusSearch();
  if (next.name === 'home') enterHome();
  else screenFor(next)?.enter?.(next);
}

// Keyboard: 1–4 tabs, L log, / search, Cmd/Ctrl-K palette. Not while
// typing, and not while a sheet or menu is open.
function onKey(e) {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
    e.preventDefault();
    import('./features/palette.js').then(m => m.openPalette());
    return;
  }
  if (e.metaKey || e.ctrlKey || e.altKey || e.defaultPrevented) return;
  if (e.target.closest?.('input, textarea, select, [contenteditable="true"]') || anySheetOpen() || document.querySelector('.menu')) return;
  const keys = { 1: '#home', 2: '#library', 3: '#discover', 4: '#you' };
  if (keys[e.key]) {
    e.preventDefault();
    go(keys[e.key]);
  } else if (e.key === 'l' && route.name !== 'library') {
    e.preventDefault();
    openLogSheet();
  } else if (e.key === 'L') {
    e.preventDefault();
    openLogSheet();
  } else if (e.key === '/') {
    e.preventDefault();
    focusSearch();
  }
}

function registerServiceWorker() {
  if (!('serviceWorker' in navigator)) return;
  navigator.serviceWorker.register(swURL).catch(() => {});
  let hadController = !!navigator.serviceWorker.controller;
  navigator.serviceWorker.addEventListener('controllerchange', () => {
    if (!hadController) { hadController = true; return; }
    toast('A new version of Cato is ready', { duration: 30000, action: { label: 'Reload', run: () => location.reload() } });
  });
  navigator.serviceWorker.addEventListener('message', e => {
    if (e.data?.type === 'flush-outbox') app.store?.retryNow();
  });
}

// refreshUnread updates the inbox badge (on You and the app icon).
async function refreshUnread() {
  try {
    const res = await api.get('/api/v1/notifications?limit=1');
    if (res.unread !== app.unread) {
      app.unread = res.unread;
      try { res.unread ? navigator.setAppBadge?.(res.unread)?.catch(() => {}) : navigator.clearAppBadge?.()?.catch(() => {}); } catch { /* unsupported */ }
      app.redraw();
    }
  } catch { /* offline */ }
}

function offlineScreen(message) {
  render(html`<div class="login-page"><div class="login-card">
    <div class="brand"><img src="/favicon.svg" alt="">Cato</div>
    <h1 style="font-size:var(--fs-22)">Can't reach Cato</h1>
    <p class="pitch">${message}</p>
    <button type="button" class="btn btn-primary" @click=${() => location.reload()}>Try again</button>
  </div></div>`, document.getElementById('app'));
}

// sendWithSession is the store's transport. After an offline start there is
// no CSRF token yet: fetch it before the first queued write goes out (a
// failure stays retryable, so nothing queued is lost).
async function sendWithSession(method, path, opts) {
  if (method !== 'GET' && !getCSRF()) {
    const me = await api.get('/api/v1/me');
    setCSRF(me.csrf_token);
    app.me = me.user;
    app.prefs = me.prefs || app.prefs;
  }
  return request(method, path, opts);
}

async function boot() {
  registerServiceWorker();
  prepareToasts();
  const kv = await openKV();
  let me;
  try {
    me = await api.get('/api/v1/me');
    setCSRF(me.csrf_token);
    await kv.put('meta', 'me', { user: me.user, prefs: me.prefs });
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      location.href = '/login' + location.hash;
      return;
    }
    // Offline: open from the copy on this device if there is one.
    const cached = await kv.get('meta', 'me');
    if (!cached) {
      offlineScreen('Check your connection, then try again.');
      return;
    }
    me = cached;
    app.online = false;
  }
  app.me = me.user;
  app.prefs = me.prefs || {};
  app.unread = me.unread || 0;
  applyTheme(app.prefs.theme);
  try { app.config = await api.get('/api/config'); } catch { app.config = {}; }

  app.store = new Store({
    api: { request: sendWithSession },
    kv,
    lock: navigator.locks ? (name, fn) => navigator.locks.request(name, fn) : null,
  });
  await app.store.init(app.me.id);
  app.store.addEventListener('change', () => app.redraw());
  app.store.addEventListener('rejected', e => toast(e.detail.error.message || 'That change was rejected', { kind: 'error' }));
  // A write that couldn't reach the server means offline, whatever
  // navigator.onLine says (it reports a network interface, not a route).
  app.store.addEventListener('offline', () => { app.online = false; app.redraw(); });
  app.store.addEventListener('sent', () => { if (!app.online) { app.online = true; app.redraw(); } });

  addEventListener('cato:unauthorized', () => { location.href = '/login'; });
  addEventListener('online', () => { app.online = true; app.store.retryNow(); app.redraw(); });
  addEventListener('offline', () => { app.online = false; app.redraw(); });
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState !== 'visible') return;
    app.store.sync().catch(() => {});
    refreshTimer();
    refreshUnread();
  });
  matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => applyTheme(app.prefs.theme));
  document.addEventListener('keydown', onKey);
  if (!app.config.dev) delete SCREENS['dev/ui'];

  startRouter(onRoute);
  if (app.online) {
    app.store.sync().then(() => app.store.flush()).catch(() => {});
    refreshTimer();
    refreshUnread();
  } else {
    app.store.synced = true;
    app.redraw();
  }
}

boot();
