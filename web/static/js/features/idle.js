// idle.js — what the shell does once it's idle after the first paint, so
// none of it counts against the initial load.
//
// Prefetch: the tabs and the game page load now, so a first visit draws
// at once instead of flashing a placeholder while their code loads.
//
// Unexpected reloads: each in-app navigation is noted for this tab, with
// whether the page then closed cleanly. A page that starts again within 5 s
// of one was reloaded under the app (a killed page, an extension, a
// gesture): it tells the server, which logs it with the user agent, so
// devices we can't test on can say why.
import { api } from '../core/api.js';

const NAV_KEY = 'cato:last-nav';

function store(fn) {
  try { return fn(sessionStorage); } catch { return null; }
}

export function start(loadScreen) {
  if (!navigator.connection?.saveData) {
    for (const name of ['library', 'discover', 'you', 'game']) loadScreen(name).catch(() => {});
  }

  // The last navigation the previous page made (recording starts below,
  // so this page hasn't written one yet).
  const prev = store(s => { const v = JSON.parse(s.getItem(NAV_KEY) || 'null'); s.removeItem(NAV_KEY); return v; });
  const since = prev ? Math.round(performance.timeOrigin) - prev.at : Infinity;
  if (since >= 0 && since < 5000) {
    api.post('/api/v1/diagnostics/reload', {
      nav_type: performance.getEntriesByType?.('navigation')[0]?.type || '',
      since_nav_ms: since,
      clean_exit: !!prev.clean,
      from: String(prev.from ?? '').slice(0, 200),
      to: String(prev.to ?? '').slice(0, 200),
      uptime_ms: Math.max(0, prev.at - prev.started),
    }).catch(() => {});
  }

  let last = null;
  addEventListener('hashchange', e => {
    const hash = u => new URL(u).hash || '#home';
    last = { at: Date.now(), started: Math.round(performance.timeOrigin), from: hash(e.oldURL), to: hash(e.newURL) };
    store(s => s.setItem(NAV_KEY, JSON.stringify(last)));
  });
  addEventListener('pagehide', () => {
    if (last) store(s => s.setItem(NAV_KEY, JSON.stringify({ ...last, clean: true })));
  });
}
