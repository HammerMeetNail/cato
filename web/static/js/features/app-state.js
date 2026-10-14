// app-state.js — what every screen shares: the signed-in user, settings,
// the library store and a redraw hook.
import { api } from '../core/api.js';
import { localDay } from '../core/format.js';

export const app = {
  me: null,
  prefs: {},
  config: {},
  store: null,
  timer: null,
  unread: 0,
  serverSkew: 0,
  online: typeof navigator === 'undefined' ? true : navigator.onLine,
  redraw: () => {},
};

// applyTheme sets data-theme (theme-init.js reads the same key before
// first paint) and the theme-color meta.
export function applyTheme(theme) {
  const root = document.documentElement;
  if (theme === 'light' || theme === 'dark') root.setAttribute('data-theme', theme);
  else root.removeAttribute('data-theme');
  try {
    if (theme === 'light' || theme === 'dark') localStorage.setItem('cato-theme', theme);
    else localStorage.removeItem('cato-theme');
  } catch { /* private mode */ }
  const dark = theme === 'dark' || (theme !== 'light' && matchMedia('(prefers-color-scheme: dark)').matches);
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', dark ? '#0d0f14' : '#f4f5f8');
}

export async function savePrefs(patch) {
  const before = app.prefs;
  app.prefs = { ...app.prefs, ...patch };
  if ('theme' in patch) applyTheme(app.prefs.theme);
  app.redraw();
  try {
    const res = await api.patch('/api/v1/me/prefs', patch);
    app.prefs = res.prefs;
    app.redraw();
    return app.prefs;
  } catch (err) {
    app.prefs = before;
    if ('theme' in patch) applyTheme(before.theme);
    app.redraw();
    throw err;
  }
}

export function today() {
  return localDay();
}

export const ratingDisplay = () => app.prefs.rating_display || 'stars';
