// idle.js — what the shell does once it's idle after the first paint, so
// none of it counts against the initial load.
//
// Prefetch: the tabs and the game page load now, so a first visit draws
// at once instead of flashing a placeholder while their code loads. Pressing
// on a game link starts that game's request before the tap ends.
//
// Unexpected reloads: each in-app navigation is noted for this tab, with
// whether the page then closed cleanly and its last few events (link taps,
// navigations and who began them, errors). A page that starts again within
// 5 s of one was reloaded under the app (a killed page, an extension, a
// gesture): it tells the server, which logs it with the user agent, so
// devices we can't test on can say why.
import { api } from '../core/api.js';

const NAV_KEY = 'cato:last-nav';

function store(fn) {
  try { return fn(sessionStorage); } catch { return null; }
}

export function start(loadScreen) {
  // When this page started, on the same clock as Date.now():
  // performance.timeOrigin can drift from it by seconds.
  const pageStart = Date.now() - performance.now();
  if (!navigator.connection?.saveData) {
    for (const name of ['library', 'discover', 'you', 'game']) loadScreen(name).catch(() => {});
  }
  addEventListener('pointerdown', e => {
    const id = Number(e.target.closest?.('a[href^="#game/"]')?.getAttribute('href').slice(6));
    if (id) import('./game.js').then(m => m.prefetchGame(id)).catch(() => {});
  }, { capture: true, passive: true });

  // The last navigation the previous page made (recording starts below,
  // so this page hasn't written one yet).
  const prev = store(s => { const v = JSON.parse(s.getItem(NAV_KEY) || 'null'); s.removeItem(NAV_KEY); return v; });
  const since = prev ? Math.round(pageStart - prev.at) : Infinity;
  // A navigation noted while the reload was already starting comes out a
  // little negative: it still counts.
  if (since > -2000 && since < 5000) {
    const nav = performance.getEntriesByType?.('navigation')[0];
    api.post('/api/v1/diagnostics/reload', {
      nav_type: nav?.type || '',
      since_nav_ms: Math.max(0, since),
      clean_exit: !!prev.clean,
      from: String(prev.from ?? '').slice(0, 200),
      to: String(prev.to ?? '').slice(0, 200),
      uptime_ms: Math.max(0, prev.at - prev.started),
      persisted: !!prev.persisted,
      trail: String(prev.trail ?? '').slice(-1200),
      next_url: (location.pathname + location.search + location.hash).slice(0, 200),
      redirects: nav?.redirectCount || 0,
    }).catch(() => {});
  }

  // The note is rewritten with every event, so it survives a page that's
  // killed before pagehide. Events after pagehide (visibilitychange comes
  // next) keep its clean exit.
  let last = null;
  let exit = {};
  const trail = [];
  const keep = () => { if (last) store(s => s.setItem(NAV_KEY, JSON.stringify({ ...last, ...exit, trail: trail.join('; ') }))); };
  const note = text => {
    trail.push(`${Math.round(performance.now())} ${text}`.slice(0, 140));
    if (trail.length > 8) trail.shift();
    keep();
  };
  const where = url => { const u = new URL(url, location.href); return u.pathname + u.search + u.hash; };
  addEventListener('hashchange', e => {
    const hash = u => new URL(u).hash || '#home';
    last = { at: Date.now(), started: Math.round(pageStart), from: hash(e.oldURL), to: hash(e.newURL) };
    note(`hashchange ${last.to}`);
  });
  addEventListener('click', e => {
    const a = e.target.closest?.('a[href]');
    if (a) note(`tap ${a.getAttribute('href')}`);
  }, true);
  // Where the Navigation API exists, every navigation says what it is and
  // whether a person, a link or a script began it.
  window.navigation?.addEventListener('navigate', e => {
    const from = e.sourceElement ? ` from ${e.sourceElement.tagName.toLowerCase()} ${e.sourceElement.getAttribute('href') || ''}` : '';
    note(`navigate ${e.navigationType} ${where(e.destination.url)}${e.hashChange ? ' hash' : ''}${e.userInitiated ? ' user' : ''}${from}`);
  });
  addEventListener('error', e => note(`error ${e.message}`));
  addEventListener('unhandledrejection', e => note(`rejection ${e.reason?.message || e.reason}`));
  document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'hidden') note('hidden'); });
  addEventListener('pagehide', e => {
    exit = { clean: true, persisted: e.persisted };
    note(`pagehide${e.persisted ? ' persisted' : ''}`);
  });
  // Back from the back/forward cache: the page lives on.
  addEventListener('pageshow', e => {
    if (!e.persisted) return;
    exit = {};
    note('pageshow persisted');
  });
}
