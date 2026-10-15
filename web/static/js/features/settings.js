// settings.js — You → Settings: profile, appearance, ratings, library
// defaults, feedback, devices, data and account.
import { html, nothing } from '../ui/lit.js';
import { icon } from '../ui/icons.js';
import { segmented } from '../ui/parts.js';
import { confirmSheet, openSheet } from '../ui/sheet.js';
import { toast, toastError } from '../ui/toast.js';
import { api, setCSRF } from '../core/api.js';
import { SORTS } from '../core/filter.js';
import { formatDay, timestampDay } from '../core/format.js';
import { app, savePrefs } from './app-state.js';
import { reachSettings, loadReach } from './reach.js';

const st = { devices: null, platforms: null, platformQuery: '' };

// Later phases add sections (notifications, connected accounts, privacy).
export const extraSettings = [];

export async function enterSettings(route) {
  loadReach();
  try {
    st.devices = (await api.get('/api/v1/devices')).items;
  } catch {
    st.devices = null;
  }
  app.redraw();
  if (route?.query?.section) {
    requestAnimationFrame(() => document.getElementById(`set-${route.query.section}`)?.scrollIntoView({ block: 'start' }));
  }
}

const save = patch => savePrefs(patch).then(() => toast('Saved'), err => toastError(err));

function deviceName(ua) {
  if (!ua) return 'Unknown browser';
  const os = /iPhone|iPad/.test(ua) ? 'iOS' : /Android/.test(ua) ? 'Android' : /Mac OS X/.test(ua) ? 'macOS' : /Windows/.test(ua) ? 'Windows' : /Linux/.test(ua) ? 'Linux' : '';
  const browser = /Edg\//.test(ua) ? 'Edge' : /Firefox\//.test(ua) ? 'Firefox' : /Chrome\//.test(ua) ? 'Chrome' : /Safari\//.test(ua) ? 'Safari' : 'Browser';
  return os ? `${browser} on ${os}` : browser;
}

async function pickPlatforms() {
  if (!st.platforms) {
    try {
      st.platforms = (await api.get('/api/v1/platforms')).items.map(p => p.name).sort((a, b) => a.localeCompare(b));
    } catch (err) {
      toastError(err);
      return;
    }
  }
  const chosen = new Set(app.prefs.default_platforms || []);
  let q = '';
  openSheet({
    title: 'Your platforms',
    cls: 'full',
    content: s => {
      const list = st.platforms.filter(p => !q || p.toLowerCase().includes(q.toLowerCase()));
      return html`<p class="muted">These are pre-selected as “owned on” when you add a game.</p>
        <div class="search" style="margin:var(--sp-3) 0">${icon('search', 'icon-search')}
          <input type="search" placeholder="Filter platforms" aria-label="Filter platforms" @input=${e => { q = e.target.value; s.update(); }}></div>
        <div class="chips">${list.map(p => html`<button type="button" class="chip" aria-pressed=${String(chosen.has(p))}
          @click=${() => { chosen.has(p) ? chosen.delete(p) : chosen.add(p); s.update(); }}>${p}</button>`)}</div>`;
    },
    footer: s => html`<button type="button" class="btn btn-primary btn-block" @click=${() => s.close(true)}>Save ${chosen.size ? `(${chosen.size})` : ''}</button>`,
  }).closed.then(ok => ok && save({ default_platforms: [...chosen] }));
}

function changePassword() {
  const f = { current: '', next: '' };
  openSheet({
    title: 'Change password',
    content: s => html`<form class="form-grid" @submit=${e => { e.preventDefault(); s.close(true); }}>
      ${app.me?.has_password ? html`<label class="field"><span>Current password</span>
        <input class="input" type="password" autocomplete="current-password" required @input=${e => { f.current = e.target.value; }}></label>` : nothing}
      <label class="field"><span>New password (at least 8 characters)</span>
        <input class="input" type="password" autocomplete="new-password" minlength="8" required @input=${e => { f.next = e.target.value; }}></label>
      <button class="btn btn-primary" type="submit">Change password</button></form>`,
  }).closed.then(async ok => {
    if (!ok) return;
    try {
      await api.post('/api/auth/password', { current_password: f.current, new_password: f.next });
      toast('Password changed; other devices were signed out');
      enterSettings();
    } catch (err) {
      toastError(err);
    }
  });
}

function deleteAccount() {
  let password = '';
  let typed = '';
  openSheet({
    title: 'Delete your account',
    content: s => html`<div class="form-grid">
      <p>This permanently deletes your library, journal, settings and account. Download an export first if you want a copy.</p>
      <p><a href="/api/v1/export.json" download>Download everything (JSON)</a></p>
      ${app.me?.has_password ? html`<label class="field"><span>Password</span>
        <input class="input" type="password" autocomplete="current-password" @input=${e => { password = e.target.value; s.update(); }}></label>` : nothing}
      <label class="field"><span>Type DELETE to confirm</span><input class="input" autocapitalize="characters" @input=${e => { typed = e.target.value; s.update(); }}></label>
    </div>`,
    footer: s => html`<button type="button" class="btn btn-danger btn-block" ?disabled=${typed !== 'DELETE' || (app.me?.has_password && !password)}
      @click=${() => s.close(true)}>Delete account</button>`,
  }).closed.then(async ok => {
    if (!ok) return;
    try {
      await api.del('/api/me', password ? { confirm: 'DELETE', password } : { confirm: 'DELETE' });
      await app.store.clear();
      setCSRF(null);
      location.href = '/login';
    } catch (err) {
      toastError(err);
    }
  });
}

export function settingsView() {
  const p = app.prefs;
  return html`<div class="page"><div class="settings-grid">
    <header class="page-head"><h1>Settings</h1></header>

    <section id="set-profile" aria-labelledby="h-profile"><h2 id="h-profile" class="section-head">Profile</h2>
      <div class="settings-list">
        <form class="form-grid" @submit=${async e => {
          e.preventDefault();
          const name = new FormData(e.target).get('name').trim();
          try {
            const res = await api.patch('/api/me', { display_name: name });
            app.me = { ...app.me, display_name: res.display_name ?? name };
            toast('Name saved');
            app.redraw();
          } catch (err) { toastError(err); }
        }}>
          <label class="field"><span>Display name</span><input class="input" name="name" maxlength="64" .value=${app.me?.display_name || ''}></label>
          <div class="muted">Signed in as ${app.me?.email}</div>
          <div><button class="btn btn-secondary" type="submit">Save name</button></div>
        </form>
      </div></section>

    <section id="set-appearance" aria-labelledby="h-appearance"><h2 id="h-appearance" class="section-head">Appearance</h2>
      <div class="settings-list">
        <div class="field"><span class="label">Theme</span>${segmented({ label: 'Theme', value: p.theme || 'system', onChange: v => save({ theme: v }),
          options: [{ value: 'system', label: 'System', icon: 'monitor' }, { value: 'light', label: 'Light', icon: 'sun' }, { value: 'dark', label: 'Dark', icon: 'moon' }] })}</div>
        <div class="field"><span class="label">Ratings</span>${segmented({ label: 'Rating display', value: p.rating_display || 'stars', onChange: v => save({ rating_display: v }),
          options: [{ value: 'stars', label: '★ 5 stars' }, { value: 'ten', label: '10 points' }, { value: 'hundred', label: '100 points' }] })}</div>
        <div class="field"><span class="label">Week starts on</span>${segmented({ label: 'Week start', value: p.week_start || 'mon', onChange: v => save({ week_start: v }),
          options: [{ value: 'mon', label: 'Monday' }, { value: 'sun', label: 'Sunday' }] })}</div>
      </div></section>

    <section id="set-platforms" aria-labelledby="h-library"><h2 id="h-library" class="section-head">Library</h2>
      <div class="settings-list">
        <div class="row row-wrap"><div class="grow"><div class="label">Your platforms</div>
          <div class="muted">${(p.default_platforms || []).length ? p.default_platforms.join(', ') : 'None chosen'}</div></div>
          <button type="button" class="btn btn-secondary" @click=${pickPlatforms}>Choose</button></div>
        <div class="field"><span class="label">Layout</span>${segmented({ label: 'Library layout', value: p.library_layout || 'grid', onChange: v => save({ library_layout: v }),
          options: [{ value: 'grid', label: 'Grid', icon: 'layout-grid' }, { value: 'list', label: 'List', icon: 'rows-3' }] })}</div>
        <div class="field"><span class="label">Density</span>${segmented({ label: 'Density', value: p.density || 'comfortable', onChange: v => save({ density: v }),
          options: [{ value: 'comfortable', label: 'Comfortable' }, { value: 'compact', label: 'Compact' }] })}</div>
        <label class="field"><span>Default sort</span><select class="select" @change=${e => save({ library_sort: e.target.value })}>
          ${SORTS.map(o => html`<option value=${o.value} ?selected=${(p.library_sort || 'updated') === o.value}>${o.label}</option>`)}</select></label>
      </div></section>

    <section aria-labelledby="h-feedback"><h2 id="h-feedback" class="section-head">Home and feedback</h2>
      <div class="settings-list">
        <label class="switch"><span>Show my play streak on Home</span><input type="checkbox" .checked=${p.streaks !== false} @change=${e => save({ streaks: e.target.checked })}></label>
        <label class="switch"><span>Vibrate when logging (Android)</span><input type="checkbox" .checked=${p.haptics !== false} @change=${e => save({ haptics: e.target.checked })}></label>
        <label class="switch"><span>Confetti when I finish a game</span><input type="checkbox" .checked=${p.confetti !== false} @change=${e => save({ confetti: e.target.checked })}></label>
        <label class="switch"><span>Swipe right or left to go back or forward (touch screens)</span><input type="checkbox" .checked=${p.swipe_nav !== false} @change=${e => save({ swipe_nav: e.target.checked })}></label>
        <label class="switch"><span>Show the first-run guide on Home</span><input type="checkbox" .checked=${!p.onboarding_done} @change=${e => save({ onboarding_done: !e.target.checked })}></label>
      </div></section>

    ${reachSettings()}
    ${extraSettings.map(fn => fn())}

    <section id="set-data" aria-labelledby="h-data"><h2 id="h-data" class="section-head">Your data</h2>
      <div class="settings-list">
        <a class="list-row" href="/api/v1/export.json" download>${icon('download')}<div class="grow"><div class="title">Export everything (JSON)</div><div class="sub">Library, playthroughs, sessions and settings; Cato can import it again</div></div></a>
        <a class="list-row" href="/api/v1/export.csv" download>${icon('download')}<div class="grow"><div class="title">Export a spreadsheet (CSV)</div><div class="sub">One row per game</div></div></a>
        <a class="list-row" href="#you/import">${icon('upload')}<div class="grow"><div class="title">Import</div><div class="sub">Steam, CSV from other trackers, or a Cato export</div></div>${icon('chevron-right')}</a>
      </div></section>

    <section id="set-devices" aria-labelledby="h-devices"><h2 id="h-devices" class="section-head">Sessions and devices</h2>
      <div class="settings-list">
        ${st.devices === null ? html`<div class="muted">Unavailable offline.</div>` : st.devices.map(d => html`<div class="row">
          ${icon(/iPhone|Android|iPad/.test(d.user_agent) ? 'gamepad-2' : 'monitor')}
          <div class="grow"><div class="title">${deviceName(d.user_agent)}${d.current ? html` <span class="muted">(this device)</span>` : nothing}</div>
            <div class="sub muted">Signed in ${formatDay(timestampDay(d.created_at))} · last used ${formatDay(timestampDay(d.last_seen_at))}</div></div>
          ${d.current ? nothing : html`<button type="button" class="btn btn-sm btn-secondary" @click=${async () => {
            try { await api.del(`/api/v1/devices/${d.id}`); toast('Signed out'); enterSettings(); } catch (err) { toastError(err); }
          }}>Sign out</button>`}
        </div>`)}
        ${st.devices?.length > 1 ? html`<div><button type="button" class="btn btn-secondary" @click=${async () => {
          if (!await confirmSheet({ title: 'Sign out everywhere else?', message: 'Every other browser and app signed in to your account will need to sign in again.', confirm: 'Sign out others' })) return;
          try { const r = await api.post('/api/v1/devices/revoke-others'); toast(`Signed out ${r.revoked} other device${r.revoked === 1 ? '' : 's'}`); enterSettings(); } catch (err) { toastError(err); }
        }}>Sign out all other devices</button></div>` : nothing}
      </div></section>

    <section id="set-account" aria-labelledby="h-account"><h2 id="h-account" class="section-head">Account</h2>
      <div class="settings-list">
        <button type="button" class="list-row" @click=${changePassword}>${icon('key-round')}<div class="grow"><div class="title">${app.me?.has_password ? 'Change password' : 'Set a password'}</div></div></button>
        <button type="button" class="list-row" @click=${async () => {
          try { await api.post('/api/auth/logout'); } catch { /* offline: drop local state anyway */ }
          await app.store.clear();
          setCSRF(null);
          location.href = '/login';
        }}>${icon('log-out')}<div class="grow"><div class="title">Sign out</div></div></button>
        <button type="button" class="list-row" style="color:var(--danger)" @click=${deleteAccount}>${icon('trash-2')}<div class="grow"><div class="title">Delete account…</div></div></button>
      </div></section>
  </div></div>`;
}
