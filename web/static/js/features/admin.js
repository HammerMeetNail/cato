// admin.js — the admin pages (#admin): people and roles, invites and the
// sign-up mode, reports and moderation, server health, and the audit log.
// The server checks the role on every call; this only hides the UI.
import { html, nothing } from '../ui/lit.js';
import { icon } from '../ui/icons.js';
import { segmented, skeletonRows, empty } from '../ui/parts.js';
import { confirmSheet } from '../ui/sheet.js';
import { toast, toastError } from '../ui/toast.js';
import { api } from '../core/api.js';
import { plural, relativeDay, timestampDay, formatMinutes } from '../core/format.js';
import { app } from './app-state.js';
import { avatar, personName, personLink, showLink } from './people.js';

const TABS = [
  { value: 'users', label: 'People', icon: 'users' },
  { value: 'reports', label: 'Reports', icon: 'flag' },
  { value: 'invites', label: 'Sign-up', icon: 'ticket' },
  { value: 'system', label: 'System', icon: 'monitor' },
  { value: 'audit', label: 'Audit log', icon: 'history' },
];

const st = { tab: 'users', data: {}, error: null };

const ago = ts => (ts ? relativeDay(timestampDay(ts)) : '—');
const bytes = n => (n >= 1e9 ? `${(n / 1e9).toFixed(1)} GB` : n >= 1e6 ? `${(n / 1e6).toFixed(1)} MB` : `${Math.round((n || 0) / 1e3)} kB`);

export async function enterAdmin(route) {
  st.tab = TABS.some(t => t.value === route.query.tab) ? route.query.tab : 'users';
  await load();
}

async function load() {
  const tab = st.tab;
  try {
    let data;
    if (tab === 'users') data = await api.get('/api/v1/admin/users');
    if (tab === 'reports') data = await api.get('/api/v1/admin/reports');
    if (tab === 'invites') {
      const [settings, invites] = await Promise.all([api.get('/api/v1/admin/settings'), api.get('/api/v1/admin/invites')]);
      data = { settings, invites: invites.items };
    }
    if (tab === 'system') data = await api.get('/api/v1/admin/system');
    if (tab === 'audit') data = await api.get('/api/v1/admin/audit');
    st.data[tab] = data;
    st.error = null;
  } catch (err) {
    st.error = err;
  }
  app.redraw();
}

async function call(method, path, body, msg) {
  try {
    await api[method](path, body);
    if (msg) toast(msg);
    await load();
  } catch (err) { toastError(err); }
}

function usersTab(d) {
  return html`<div class="table-scroll"><table class="admin-table">
    <thead><tr><th>Person</th><th>Email</th><th>Games</th><th>Joined</th><th>Last seen</th><th>Role</th><th></th></tr></thead>
    <tbody>${d.items.map(u => html`<tr>
      <td><div class="row">${avatar(u)}<div>${personLink(u)}<div class="muted">@${u.username}${u.disabled ? ' · disabled' : ''}</div></div></div></td>
      <td class="mono">${u.email}${u.google ? html` <span class="muted">(Google)</span>` : nothing}</td>
      <td class="num">${u.games}</td><td>${ago(u.created_at)}</td><td>${ago(u.last_seen_at)}</td>
      <td><select class="input" aria-label=${`Role for ${personName(u)}`} .value=${u.role}
        @change=${e => call('patch', `/api/v1/admin/users/${u.id}`, { role: e.target.value }, 'Role changed')}>
        <option value="user">User</option><option value="admin">Admin</option></select></td>
      <td><div class="row">
        <button type="button" class="btn btn-sm btn-secondary" ?disabled=${!u.password && u.google} title="Make a one-time link to set a new password"
          @click=${async () => {
            try {
              const r = await api.post(`/api/v1/admin/users/${u.id}/reset-link`);
              showLink('Password reset link', location.origin + r.path, `Send this to ${personName(u)}. It works once, for ${r.expires_in_hours} hours, and signs them out everywhere else.`);
            } catch (err) { toastError(err); }
          }}>${icon('key-round')}Reset link</button>
        <button type="button" class=${`btn btn-sm ${u.disabled ? 'btn-secondary' : 'btn-danger'}`} ?disabled=${u.id === app.me.id}
          @click=${async () => {
            if (!u.disabled && !await confirmSheet({ title: `Disable ${personName(u)}?`, message: 'They are signed out everywhere and can’t sign in. Their data stays; you can enable the account again.', confirm: 'Disable', danger: true })) return;
            call('patch', `/api/v1/admin/users/${u.id}`, { disabled: !u.disabled }, u.disabled ? 'Enabled' : 'Disabled');
          }}>${u.disabled ? 'Enable' : 'Disable'}</button></div></td>
    </tr>`)}</tbody></table></div>`;
}

function reportsTab(d) {
  if (!d.items.length) return empty({ iconName: 'shield', title: 'No open reports', body: 'Reports people send about reviews, lists or profiles show up here.' });
  return html`<ul class="list">${d.items.map(r => html`<li class="list-row" style="align-items:flex-start">
    ${icon('flag')}
    <div class="grow">
      <div class="title">${r.kind === 'review' ? 'Review' : r.kind === 'list' ? 'List' : 'Profile'} reported by ${personLink(r.reporter)} · ${ago(r.created_at)}</div>
      ${r.reason ? html`<p>“${r.reason}”</p>` : nothing}
      <blockquote class="muted bio">${r.excerpt}</blockquote>
      ${r.kind === 'list' ? html`<a href=${`#list/${r.target}`}>Open the list</a>` : nothing}
    </div>
    <div class="row row-wrap">
      ${r.kind !== 'user' ? html`<button type="button" class="btn btn-sm btn-danger" @click=${() => call('post', `/api/v1/admin/reports/${r.id}/resolve`, { hide: true }, 'Hidden')}>Hide it</button>` : nothing}
      <button type="button" class="btn btn-sm btn-secondary" @click=${() => call('post', `/api/v1/admin/reports/${r.id}/resolve`, { hide: false }, 'Dismissed')}>Dismiss</button>
    </div></li>`)}</ul>`;
}

const MODES = [
  { value: 'closed', label: 'Closed', icon: 'lock' },
  { value: 'invite', label: 'Invite only', icon: 'ticket' },
  { value: 'open', label: 'Open', icon: 'globe' },
];

function invitesTab(d) {
  const s = d.settings;
  return html`<section><h2 style="font-size:var(--fs-18)">Who can create an account</h2>
    ${segmented({ label: 'Sign-up mode', value: s.signup, options: MODES, onChange: v => call('patch', '/api/v1/admin/settings', { signup: v }, 'Sign-up mode saved') })}
    <p class="muted" style="margin-top:var(--sp-2)">${s.signup === 'closed' ? 'Only invites made by admins work.' : s.signup === 'invite' ? 'Anyone with an account can invite people; each link works once, for 7 days.' : 'Anyone who can reach this server can sign up. Only use this on a private network.'}
      ${s.signup_override ? html` This overrides CATO_SIGNUP=${s.signup_env}. <button type="button" class="link" @click=${() => call('patch', '/api/v1/admin/settings', { signup: '' }, 'Using CATO_SIGNUP again')}>Use the environment setting</button>` : html` Set by CATO_SIGNUP.`}</p>
  </section>
  <section class="section"><div class="section-head"><h2>Invites</h2><a href="#you/people">Make an invite</a></div>
    ${d.invites.length ? html`<div class="table-scroll"><table class="admin-table"><thead><tr><th>For</th><th>From</th><th>Made</th><th>State</th><th></th></tr></thead>
      <tbody>${d.invites.map(i => html`<tr><td>${i.note || '—'}</td><td>${i.created_by ? personLink(i.created_by) : '—'}</td><td>${ago(i.created_at)}</td>
        <td>${i.state === 'used' && i.used_by ? html`Used by ${personLink(i.used_by)}` : i.state}</td>
        <td>${i.state === 'open' ? html`<button type="button" class="btn btn-sm btn-ghost" @click=${() => call('del', `/api/v1/invites/${i.id}`, undefined, 'Revoked')}>Revoke</button>` : nothing}</td></tr>`)}</tbody></table></div>`
      : html`<p class="muted">No invites yet.</p>`}
  </section>`;
}

function systemTab(d) {
  const jobs = d.jobs || [];
  return html`<div class="stat-tiles">
      <div class="stat-tile"><div class="value">${bytes(d.db_bytes)}</div><div class="label">Database</div></div>
      <div class="stat-tile"><div class="value">${bytes(d.wal_bytes)}</div><div class="label">Write-ahead log</div></div>
      <div class="stat-tile"><div class="value num">${d.cover_queue?.queued ?? 0}</div><div class="label">Covers to fetch${d.cover_queue?.retrying ? ` (${d.cover_queue.retrying} retrying)` : ''}</div></div>
      <div class="stat-tile"><div class="value num">${d.job_lag_seconds ? formatMinutes(Math.round(d.job_lag_seconds / 60)) : 'On time'}</div><div class="label">Job lag${d.most_overdue_job ? ` (${d.most_overdue_job})` : ''}</div></div>
    </div>
    <section class="section"><div class="section-head"><h2>Data</h2></div>
      <dl class="kv"><dt>People</dt><dd class="num">${d.counts?.users}</dd><dt>Games in the catalog</dt><dd class="num">${d.counts?.games}</dd>
        <dt>Library entries</dt><dd class="num">${d.counts?.library_items}</dd>
        <dt>Last backup</dt><dd>${d.last_backup ? html`${ago(d.last_backup.at)} · ${bytes(d.last_backup.bytes)} · ${plural(d.last_backup.count, 'snapshot')} kept <span class="mono">${d.last_backup.name}</span>` : 'None yet (one is taken before each schema upgrade)'}</dd>
        <dt>App version</dt><dd class="mono">${d.asset_version}</dd></dl></section>
    <section class="section"><div class="section-head"><h2>IGDB</h2><span class="muted">${d.igdb_configured ? '4 requests a second, people first' : 'Not configured: search uses the local catalog'}</span></div>
      ${d.igdb_lanes?.length ? html`<div class="table-scroll" tabindex="0" role="region" aria-label="IGDB lanes"><table class="admin-table"><thead><tr><th>Lane</th><th>Waiting</th><th>Requests</th><th>Average wait</th></tr></thead>
        <tbody>${d.igdb_lanes.map(l => html`<tr><td>${l.lane}</td><td class="num">${l.queued}</td><td class="num">${l.requests}</td><td class="num">${l.avg_wait_seconds.toFixed(2)} s</td></tr>`)}</tbody></table></div>` : nothing}</section>
    <section class="section"><div class="section-head"><h2>Background jobs</h2></div>
      <div class="table-scroll"><table class="admin-table"><thead><tr><th>Job</th><th>Last run</th><th>Took</th><th>Next</th><th>Runs / failures</th><th>Last error</th><th></th></tr></thead>
        <tbody>${jobs.map(j => html`<tr><td class="mono">${j.name}${j.running ? html` <span class="status status-playing">running</span>` : nothing}</td>
          <td>${j.last_finished_at && !j.last_finished_at.startsWith('0001') ? ago(j.last_finished_at) : 'never'}</td><td class="num">${j.last_duration_ms} ms</td>
          <td>${j.next_run_at && !j.next_run_at.startsWith('0001') ? new Date(j.next_run_at).toLocaleString() : '—'}${j.lag_seconds ? html` <span class="status status-abandoned">late</span>` : nothing}</td>
          <td class="num">${j.runs} / ${j.failures}</td><td class="mono">${j.last_error || ''}</td>
          <td><button type="button" class="btn btn-sm btn-ghost" ?disabled=${j.running} @click=${() => call('post', `/api/v1/admin/jobs/${encodeURIComponent(j.name)}/run`, undefined, 'Started')}>${icon('play')}Run now</button></td></tr>`)}</tbody></table></div></section>`;
}

// A table with nothing focusable in it takes focus itself, so a keyboard
// can scroll it when it's wider or taller than a phone's screen.
function auditTab(d) {
  if (!d.items.length) return html`<p class="muted">No admin actions yet.</p>`;
  return html`<div class="table-scroll" tabindex="0" role="region" aria-label="Audit log"><table class="admin-table"><thead><tr><th>When</th><th>Who</th><th>What</th><th>Target</th><th>Details</th></tr></thead>
    <tbody>${d.items.map(e => html`<tr><td>${new Date(e.created_at).toLocaleString()}</td><td>@${e.actor}</td><td class="mono">${e.action}</td>
      <td class="mono">${e.target}</td><td class="mono">${e.data ? JSON.stringify(e.data) : ''}</td></tr>`)}</tbody></table></div>`;
}

export function adminView() {
  if (app.me?.role !== 'admin') return html`<div class="page">${empty({ iconName: 'lock', title: 'Admins only' })}</div>`;
  const d = st.data[st.tab];
  const body = st.error ? empty({ iconName: 'triangle-alert', title: 'Couldn’t load this page', body: st.error.message })
    : !d ? skeletonRows(4)
    : st.tab === 'users' ? usersTab(d) : st.tab === 'reports' ? reportsTab(d) : st.tab === 'invites' ? invitesTab(d)
    : st.tab === 'system' ? systemTab(d) : auditTab(d);
  return html`<div class="page">
    <header class="page-head"><h1>Admin</h1><button type="button" class="btn btn-ghost btn-sm" @click=${load}>${icon('refresh-cw')}Refresh</button></header>
    <div class="chips chips-scroll" role="tablist" aria-label="Admin sections">${TABS.map(t => html`<a class="chip" role="tab" href=${`#admin?tab=${t.value}`}
      aria-selected=${String(st.tab === t.value)}>${icon(t.icon)}${t.label}</a>`)}</div>
    <div class="section">${body}</div>
  </div>`;
}

