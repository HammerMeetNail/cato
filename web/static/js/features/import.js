// import.js — You → Import (plan §8.1): bring a library in from Steam, a
// CSV from another tracker, a Cato export or RetroAchievements. Every
// source shows a preview first: confident matches are ticked, uncertain
// ones ask "Is this right?", and nothing is written until you confirm. A
// whole import can be undone later from History.
import { html, nothing, repeat } from '../ui/lit.js';
import { icon } from '../ui/icons.js';
import { cover, segmented, empty, chip } from '../ui/parts.js';
import { openSheet, confirmSheet } from '../ui/sheet.js';
import { toast, toastError } from '../ui/toast.js';
import { api } from '../core/api.js';
import { go } from '../core/router.js';
import { STATUSES, statusLabel, formatMinutes, plural, formatDay, timestampDay } from '../core/format.js';
import { app } from './app-state.js';

const SOURCES = [
  { value: 'steam', label: 'Steam', icon: 'gamepad-2' },
  { value: 'file', label: 'File', icon: 'upload' },
  { value: 'ra', label: 'RetroAchievements', icon: 'trophy' },
  { value: 'history', label: 'History', icon: 'history' },
];

const st = {
  tab: 'steam', status: null, loading: false, error: '',
  preview: null, // {source, items, extras?, headers?, mapping?, preset?}
  picks: new Map(), // index → {include, gameId, game, status}
  filter: 'all', page: 0, done: null, profile: '', raUser: '', raKey: '', raRemember: true, csvText: '',
};
const PAGE = 100;

export async function enterImport(route) {
  st.tab = SOURCES.some(s => s.value === route.query.source) ? route.query.source : st.tab;
  try { st.status = await api.get('/api/v1/import'); } catch (err) { st.error = err.message; }
  if (st.status?.steam && !st.profile) st.profile = st.status.steam.steam_id;
  if (st.status?.ra_user && !st.raUser) st.raUser = st.status.ra_user;
  app.redraw();
}

function setPreview(source, res) {
  st.preview = { source, ...res };
  st.picks = new Map();
  for (const c of res.items) {
    st.picks.set(c.index, {
      include: c.state === 'matched',
      gameId: c.match?.id ?? 0,
      game: c.match ?? null,
      status: c.row.status || 'backlog',
    });
  }
  st.filter = res.items.some(c => c.state === 'review') ? 'review' : 'all';
  st.page = 0;
  st.done = null;
  app.redraw();
}

async function run(fn) {
  st.loading = true; st.error = ''; app.redraw();
  try { await fn(); } catch (err) { st.error = err.message || String(err); }
  st.loading = false; app.redraw();
}

const previewSteam = () => run(async () => setPreview('steam', await api.post('/api/v1/import/steam/preview', { profile: st.profile })));
const previewRA = () => run(async () => setPreview('retroachievements', await api.post('/api/v1/import/ra/preview', { user: st.raUser, key: st.raKey, remember: st.raRemember })));

async function previewFile(file) {
  if (!file) return;
  if (file.size > 20 * 1024 * 1024) { st.error = 'That file is too big (20 MB at most).'; app.redraw(); return; }
  const text = await file.text();
  if (/^\s*[{[]/.test(text)) {
    await run(async () => setPreview('cato-json', await api.post('/api/v1/import/json/preview', { data: text })));
  } else {
    st.csvText = text;
    await previewCSV();
  }
}

const previewCSV = (mapping = null) => run(async () => {
  const res = await api.post('/api/v1/import/csv/preview', mapping ? { csv: st.csvText, mapping } : { csv: st.csvText });
  setPreview(res.preset?.id || 'csv', res);
});

function sourceForm() {
  const s = st.status;
  if (st.tab === 'steam') {
    if (s && !s.steam_configured) {
      return html`<p class="muted">${icon('info')} Steam import needs a Steam Web API key on the server (STEAM_WEB_API_KEY). Ask whoever runs this Cato.</p>`;
    }
    return html`<form class="form-grid" @submit=${e => { e.preventDefault(); previewSteam(); }}>
      <label class="field"><span>Your Steam profile</span>
        <input class="input" placeholder="steamcommunity.com/id/yourname, a custom URL name or a SteamID" .value=${st.profile}
          autocapitalize="none" spellcheck="false" @input=${e => { st.profile = e.target.value; }}></label>
      <p class="muted">Steam only shares your games when your profile and <strong>Game details</strong> are Public
        (Steam → your profile → Edit Profile → Privacy Settings). Cato stores your SteamID and, if you turn on sync, how long you've played each game.</p>
      <div><button class="btn btn-primary" type="submit" ?disabled=${st.loading || !st.profile.trim()}>${icon('search')}Find my games</button></div>
    </form>`;
  }
  if (st.tab === 'file') {
    return html`<div class="form-grid">
      <label class="dropzone" @dragover=${e => e.preventDefault()} @drop=${e => { e.preventDefault(); previewFile(e.dataTransfer.files[0]); }}>
        ${icon('upload')}<span><strong>Choose a file</strong> or drop it here</span>
        <span class="muted">A CSV from Grouvee, HowLongToBeat, Infinite Backlog, a Backloggd export script or any spreadsheet, or a Cato export (.json)</span>
        <input type="file" accept=".csv,.tsv,.txt,.json,text/csv,application/json" class="visually-hidden" @change=${e => previewFile(e.target.files[0])}>
      </label>
      <details><summary>Where do I get a file?</summary>
        <ul class="help-list">
          <li><strong>Grouvee:</strong> Settings → Export → CSV.</li>
          <li><strong>HowLongToBeat:</strong> your profile → Games → Options → Export (if offered).</li>
          <li><strong>Backloggd:</strong> has no export; community scripts can make a CSV from your profile page.</li>
          <li><strong>Any spreadsheet:</strong> a header row with columns like Title, Platform, Status, Rating, Hours, Finished. You can tell Cato which column is which.</li>
          <li><strong>Another Cato:</strong> Settings → Your data → Export everything (JSON). Reviews and lists come along.</li>
        </ul></details>
    </div>`;
  }
  if (st.tab === 'ra') {
    return html`<form class="form-grid" @submit=${e => { e.preventDefault(); previewRA(); }}>
      <label class="field"><span>RetroAchievements user name</span><input class="input" autocapitalize="none" .value=${st.raUser} @input=${e => { st.raUser = e.target.value; }}></label>
      <label class="field"><span>Web API key</span><input class="input" type="password" autocomplete="off" placeholder=${st.status?.ra_user ? 'Saved — leave empty to reuse it' : ''}
        @input=${e => { st.raKey = e.target.value; }}>
        <span class="muted">From retroachievements.org → Settings → Keys. It's used to read your progress.</span></label>
      <label class="row"><input type="checkbox" .checked=${st.raRemember} @change=${e => { st.raRemember = e.target.checked; }}><span>Remember it for next time</span></label>
      <div><button class="btn btn-primary" type="submit" ?disabled=${st.loading}>${icon('search')}Find my games</button></div>
    </form>`;
  }
  return history();
}

function history() {
  const bs = st.status?.batches || [];
  if (!bs.length) return html`<p class="muted">Nothing imported yet.</p>`;
  return html`<ul class="list">${bs.map(b => html`<li class="list-row">${icon(b.source === 'steam' ? 'gamepad-2' : 'upload')}
    <div class="grow"><div class="title">${plural(b.items, 'game')} from ${sourceName(b.source)}</div>
      <div class="sub">${formatDay(timestampDay(b.created_at))}${b.undone ? ' · undone' : ''}</div></div>
    ${b.undone ? nothing : html`<button type="button" class="btn btn-sm btn-secondary" @click=${() => undoBatch(b.id, b.items)}>${icon('undo-2')}Undo</button>`}
  </li>`)}</ul>`;
}

const sourceName = s => ({ steam: 'Steam', 'cato-json': 'a Cato export', retroachievements: 'RetroAchievements', grouvee: 'Grouvee', hltb: 'HowLongToBeat',
  infinite_backlog: 'Infinite Backlog', backloggd: 'Backloggd', cato: 'a Cato CSV', generic: 'a spreadsheet', csv: 'a CSV' })[s] || s;

async function undoBatch(id, n) {
  if (!await confirmSheet({ title: 'Undo this import?', message: `The ${plural(n, 'game')} it touched go back to how they were before it, and games it added are removed — including anything you've changed on them since.`, confirm: 'Undo import', danger: true })) return;
  try {
    const r = await api.post(`/api/v1/import/batches/${id}/undo`);
    toast(`Restored ${plural(r.restored, 'game')}`);
    await app.store.sync();
    st.done = null;
    enterImport({ query: { source: st.tab } });
  } catch (err) { toastError(err); }
}

// ---- the review list ----------------------------------------------------------

function counts() {
  const c = { matched: 0, review: 0, none: 0, have: 0, chosen: 0 };
  for (const it of st.preview.items) {
    c[it.state]++;
    if (it.in_library) c.have++;
    if (st.picks.get(it.index)?.include) c.chosen++;
  }
  return c;
}

function visible() {
  const items = st.preview.items;
  switch (st.filter) {
    case 'review': return items.filter(c => c.state === 'review');
    case 'none': return items.filter(c => c.state === 'none' && !st.picks.get(c.index).gameId);
    case 'have': return items.filter(c => c.in_library);
    case 'chosen': return items.filter(c => st.picks.get(c.index).include);
  }
  return items;
}

function choose(c, game) {
  const p = st.picks.get(c.index);
  p.gameId = game.id;
  p.game = game;
  p.include = true;
  app.redraw();
}

function searchFor(c) {
  let q = c.row.title;
  let results = [];
  let seq = 0;
  const find = async s => {
    const mine = ++seq;
    if (q.trim().length < 2) { results = []; s.update(); return; }
    try {
      const r = await api.get(`/api/v1/typeahead?q=${encodeURIComponent(q)}`);
      if (mine === seq) {
        results = [...r.library.map(i => ({ id: i.game_id, name: i.game.name, cover: i.game.cover, release_date: i.game.release_date })), ...r.catalog];
        s.update();
      }
    } catch { /* keep the old results */ }
  };
  const sheet = openSheet({
    title: 'Find the right game',
    content: s => html`<p class="muted">From the file: <strong>${c.row.title}</strong>${c.row.year ? ` (${c.row.year})` : ''}${c.row.platform ? ` · ${c.row.platform}` : ''}</p>
      <input class="input" type="search" autofocus .value=${q} @input=${e => { q = e.target.value; find(s); }} aria-label="Search games">
      <div class="list" style="margin-top:var(--sp-3)">${results.slice(0, 12).map(g => html`<button type="button" class="list-row" @click=${() => {
        choose(c, { id: g.id, name: g.name, cover: g.cover, year: g.release_date ? new Date(g.release_date * 1000).getUTCFullYear() : 0 }); s.close();
      }}>${cover(g, { cls: 'thumb' })}<div class="grow"><div class="title">${g.name}</div>
        <div class="sub">${g.release_date ? new Date(g.release_date * 1000).getUTCFullYear() : ''}${g.status ? ` · ${statusLabel(g.status)} in your library` : ''}</div></div></button>`)}</div>`,
  });
  find(sheet);
}

function row(c) {
  const p = st.picks.get(c.index);
  const g = p.game;
  const facts = [c.row.year, c.row.platform, c.row.minutes ? `${formatMinutes(c.row.minutes)} played` : '', c.row.rating ? `${Math.round(c.row.rating / 20 * 2) / 2}★` : ''].filter(Boolean).join(' · ');
  return html`<li class=${`import-row ${p.include ? 'on' : ''}`}>
    <input type="checkbox" .checked=${p.include} ?disabled=${!p.gameId} aria-label=${`Import ${c.row.title}`}
      @change=${e => { p.include = e.target.checked; app.redraw(); }}>
    <div class="import-from"><div class="title">${c.row.title}</div><div class="sub">${facts}</div></div>
    <div class="import-to">${g ? html`${cover(g, { cls: 'thumb' })}<div class="grow"><div class="title">${g.name}</div>
        <div class="sub">${g.year || ''}${c.in_library ? html` · <span class="status status-backlog">In your library: fills gaps only</span>` : ''}</div>
        ${c.state === 'review' && !p.confirmed ? html`<div class="row row-wrap" style="margin-top:var(--sp-1)">
          <span class="muted">Is this right?</span>
          <button type="button" class="btn btn-sm btn-secondary" @click=${() => { p.confirmed = true; p.include = true; app.redraw(); }}>Yes</button>
          ${(c.alternatives || []).map(a => html`<button type="button" class="chip" @click=${() => { p.confirmed = true; choose(c, a); }}>${a.name}${a.year ? ` (${a.year})` : ''}</button>`)}
          <button type="button" class="btn btn-sm btn-ghost" @click=${() => searchFor(c)}>${icon('search')}Search</button></div>` : nothing}
      </div>${c.state !== 'review' || p.confirmed ? html`<button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label=${`Change the match for ${c.row.title}`} @click=${() => searchFor(c)}>${icon('pencil')}</button>` : nothing}`
      : html`<div class="grow muted">Not found</div><button type="button" class="btn btn-sm btn-secondary" @click=${() => searchFor(c)}>${icon('search')}Find it</button>`}</div>
    <select class="select select-sm" aria-label=${`Status for ${c.row.title}`} .value=${p.status} ?disabled=${!!c.in_library}
      @change=${e => { p.status = e.target.value; }}>${STATUSES.map(s => html`<option value=${s.value} ?selected=${p.status === s.value}>${s.label}</option>`)}</select>
  </li>`;
}

function mappingEditor() {
  const pv = st.preview;
  if (!pv.headers) return nothing;
  const cols = { ...(pv.mapping?.columns || {}) };
  const fields = [['title', 'Title'], ['platform', 'Platform'], ['status', 'Status'], ['rating', 'Rating'], ['hours', 'Hours played'], ['minutes', 'Minutes played'],
    ['notes', 'Notes / review'], ['tags', 'Tags'], ['started', 'Started'], ['finished', 'Finished'], ['year', 'Release year'], ['steam_appid', 'Steam app ID'], ['igdb_id', 'IGDB ID']];
  let scale = pv.mapping?.rating_scale || 0;
  return html`<details class="section" ?open=${!pv.items.length}><summary>${pv.preset ? `Read as ${pv.preset.label}` : 'Columns'} · ${plural(pv.rows, 'row')} — check the columns</summary>
    ${pv.message ? html`<p class="field-error">${pv.message}</p>` : nothing}
    ${pv.preset?.note ? html`<p class="muted">${pv.preset.note}</p>` : nothing}
    <div class="mapping">${fields.map(([f, label]) => html`<label class="field"><span>${label}</span>
      <select class="select" @change=${e => { const v = Number(e.target.value); if (v < 0) delete cols[f]; else cols[f] = v; }}>
        <option value="-1" ?selected=${cols[f] === undefined}>—</option>
        ${pv.headers.map((h, i) => html`<option value=${i} ?selected=${cols[f] === i}>${h || `Column ${i + 1}`}</option>`)}</select></label>`)}
      <label class="field"><span>Ratings are out of</span><select class="select" @change=${e => { scale = Number(e.target.value); }}>
        ${[[0, 'Detect'], [5, '5'], [10, '10'], [100, '100']].map(([v, l]) => html`<option value=${v} ?selected=${scale === v}>${l}</option>`)}</select></label>
    </div>
    <button type="button" class="btn btn-secondary" @click=${() => previewCSV({ columns: cols, rating_scale: scale })}>${icon('refresh-cw')}Read again</button>
  </details>`;
}

async function commit() {
  const pv = st.preview;
  const entries = [];
  for (const c of pv.items) {
    const p = st.picks.get(c.index);
    if (p.include && p.gameId) entries.push({ game_id: p.gameId, row: { ...c.row, status: p.status } });
  }
  if (!entries.length) return;
  await run(async () => {
    const res = await api.post('/api/v1/import/commit', { source: pv.source, entries, extras: pv.extras || undefined });
    st.done = res;
    st.preview = null;
    await app.store.sync().catch(() => {});
    st.status = await api.get('/api/v1/import').catch(() => st.status);
  });
}

function review() {
  const pv = st.preview;
  const c = counts();
  const list = visible();
  const pages = Math.ceil(list.length / PAGE);
  const filters = [['all', `All ${pv.items.length}`], ['review', `To check ${c.review}`], ['none', `Not found ${c.none}`], ['have', `Already have ${c.have}`], ['chosen', `Chosen ${c.chosen}`]];
  return html`
    ${mappingEditor()}
    ${pv.items.length ? html`<section class="section">
      <p>${plural(c.matched, 'game')} matched${c.review ? html`, <strong>${c.review} to check</strong>` : ''}${c.none ? `, ${c.none} not found` : ''}.
        ${pv.extras && (pv.extras.reviews?.length || pv.extras.lists?.length) ? html` ${plural(pv.extras.reviews?.length || 0, 'review')} and ${plural(pv.extras.lists?.length || 0, 'list')} come along.` : ''}</p>
      <div class="chips chips-scroll" role="radiogroup" aria-label="Show">${filters.map(([v, l]) => html`<button type="button" class="chip" role="radio"
        aria-checked=${String(st.filter === v)} @click=${() => { st.filter = v; st.page = 0; app.redraw(); }}>${l}</button>`)}</div>
      <div class="row row-wrap" style="margin:var(--sp-3) 0">
        ${chip('Tick all matched', { iconName: 'square-check', onClick: () => { for (const it of pv.items) { const p = st.picks.get(it.index); if (p.gameId) p.include = true; } app.redraw(); } })}
        ${chip('Untick all', { iconName: 'square', onClick: () => { for (const p of st.picks.values()) p.include = false; app.redraw(); } })}
      </div>
      <ul class="import-list">${repeat(list.slice(st.page * PAGE, (st.page + 1) * PAGE), x => x.index, row)}</ul>
      ${pages > 1 ? html`<div class="row" style="justify-content:center;margin-top:var(--sp-3)">
        <button type="button" class="btn btn-sm btn-secondary" ?disabled=${st.page === 0} @click=${() => { st.page--; app.redraw(); }}>${icon('chevron-left')}Previous</button>
        <span class="num">${st.page + 1} / ${pages}</span>
        <button type="button" class="btn btn-sm btn-secondary" ?disabled=${st.page >= pages - 1} @click=${() => { st.page++; app.redraw(); }}>Next${icon('chevron-right')}</button></div>` : nothing}
    </section>
    <div class="import-bar">
      <span class="grow">${plural(c.chosen, 'game')} chosen. Nothing is changed until you import; you can undo the whole import later.</span>
      <button type="button" class="btn btn-ghost" @click=${() => { st.preview = null; app.redraw(); }}>Cancel</button>
      <button type="button" class="btn btn-primary" ?disabled=${!c.chosen || st.loading} @click=${commit}>${icon('download')}Import ${plural(c.chosen, 'game')}</button>
    </div>` : nothing}`;
}

function doneView() {
  const d = st.done;
  const steam = st.status?.steam;
  return html`<section class="card section">
    <h2 style="font-size:var(--fs-18)">${icon('circle-check')} Imported</h2>
    <p>${plural(d.added, 'game')} added${d.updated ? `, ${d.updated} filled in` : ''}${d.unchanged ? `, ${d.unchanged} already up to date` : ''}.
      ${d.reviews ? ` ${plural(d.reviews, 'review')}.` : ''}${d.lists ? ` ${plural(d.lists, 'list')}.` : ''}</p>
    ${steam && !steam.sync ? html`<label class="switch"><span>Keep my Steam play time in sync every day</span>
      <input type="checkbox" @change=${async e => { try { st.status = await api.patch('/api/v1/import/steam', { sync: e.target.checked }); toast('Steam sync is on'); app.redraw(); } catch (err) { toastError(err); } }}></label>` : nothing}
    <div class="row row-wrap">
      <a class="btn btn-primary" href="#library">${icon('layout-grid')}Open your library</a>
      ${d.batch_id ? html`<button type="button" class="btn btn-secondary" @click=${() => undoBatch(d.batch_id, d.added + d.updated)}>${icon('undo-2')}Undo this import</button>` : nothing}
    </div>
  </section>`;
}

export function importView() {
  return html`<div class="page">
    <header class="page-head"><h1>Import</h1></header>
    ${segmented({ label: 'Import from', cls: 'segmented-wrap', value: st.tab, options: SOURCES, onChange: v => { st.tab = v; st.preview = null; st.error = ''; go(`#you/import?source=${v}`, { replace: true }); } })}
    <div class="section">
      ${st.done ? doneView() : nothing}
      ${!st.preview ? sourceForm() : nothing}
      ${st.loading ? html`<p class="muted" role="status">${icon('refresh-cw')} Working… big libraries take a moment.</p>` : nothing}
      ${st.error ? html`<p class="field-error" role="alert">${st.error}</p>` : nothing}
      ${st.preview ? review() : nothing}
      ${!st.status && !st.error ? empty({ iconName: 'upload', title: 'Loading…' }) : nothing}
    </div>
  </div>`;
}
