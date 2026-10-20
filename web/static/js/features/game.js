// game.js — the game page, the hub for everything about one game: hero,
// your status / rating / time, your playthroughs and sessions, details you
// keep (platforms, format, tags, notes) and what the catalog knows.
import { html, nothing, repeat } from '../ui/lit.js';
import { icon } from '../ui/icons.js';
import { cover, coverSources, statusControl, empty } from '../ui/parts.js';
import { decodeCovers } from '../ui/decode.js';
import { ratingInput, stars } from '../ui/stars.js';
import { openSheet, confirmSheet } from '../ui/sheet.js';
import { toast, toastError } from '../ui/toast.js';
import { api } from '../core/api.js';
import {
  STATUSES, formatMinutes, formatDay, formatRelease, releaseYear, completionLabel, COMPLETIONS, localDay, statusLabel,
} from '../core/format.js';
import { tagSuggestions } from '../core/filter.js';
import { app, ratingDisplay } from './app-state.js';
import * as actions from './actions.js';
import { timeChips, openFinishSheet } from './sheets.js';
import { startTimer } from './timer.js';
import { gameExtras } from './game-extras.js';
import { gameSocialSection, loadGameSocial, addToList } from './people.js';

const state = { id: null, data: null, error: null, loading: false, aboutOpen: false, seq: 0 };

// Game pages seen this session: going back to one draws it from here if
// the server is slow to answer.
const seen = new Map();

// The shell keeps the current screen up until this settles (for at most
// 250 ms, see app.js onRoute), so a quick answer draws the page whole: the
// details, the reviews and the cover (the backdrop is the same image; a
// tile's cover is often a smaller size). Past that the page draws what it
// has: the seen copy, else the tapped tile.
export function enterGame(route) {
  if (state.id !== route.id) {
    state.id = route.id;
    state.data = seen.get(route.id) ?? null;
    state.error = null;
    state.aboutOpen = false;
  }
  return Promise.all([loadGameSocial(route.id), reload().then(() => decodeCovers([state.data?.game.cover]))]);
}

// backdrop is the blurred cover behind the hero: an <img> with the cover's
// own sources, so it reuses the image the cover already loaded (a CSS
// background at another size arrived a moment after the page).
function backdrop(url) {
  const { src, srcset } = coverSources(url);
  return html`<div class="backdrop">${src ? html`<img src=${src} srcset=${srcset || nothing} alt="" decoding="sync">` : nothing}</div>`;
}

// A touch or click on a game link starts its request before the tap ends
// (features/idle.js); the page's own load then picks it up, once. A press
// that arrives after that (a click's own pointerdown can) asks nothing.
let prefetched = null;
export function prefetchGame(id) {
  if (prefetched?.id === id && Date.now() - prefetched.at < 2000) return;
  const req = api.get(`/api/v1/games/${id}`);
  req.catch(() => {});
  prefetched = { id, at: Date.now(), req };
}

function fetchGame(id) {
  const p = prefetched;
  if (p?.id !== id || p.used || Date.now() - p.at >= 2000) return api.get(`/api/v1/games/${id}`);
  p.used = true;
  return p.req;
}

async function reload() {
  const id = state.id;
  const seq = ++state.seq;
  state.loading = true;
  app.redraw();
  try {
    const data = await fetchGame(id);
    seen.delete(id);
    seen.set(id, data);
    if (seen.size > 50) seen.delete(seen.keys().next().value);
    if (seq !== state.seq) return;
    state.data = data;
    state.error = null;
  } catch (err) {
    if (seq !== state.seq) return;
    state.error = err;
  } finally {
    if (seq === state.seq) {
      state.loading = false;
      app.redraw();
    }
  }
}

// tappedTile reads the name and cover off the game tile just tapped (the
// shell keeps the click target in app.tapped), so the page draws them at
// once instead of a placeholder while it loads.
function tappedTile(id) {
  const link = app.tapped?.closest?.('a[href^="#game/"]');
  if (!link || link.getAttribute('href') !== `#game/${id}`) return null;
  const card = link.closest('.upnext-card, .tile, .list-row, .feed-item') || link;
  const img = link.querySelector('.cover img') || card.querySelector('.cover img');
  const name = (img?.alt || '').replace(/^Cover of /, '') || link.getAttribute('aria-label')?.replace(/^Open /, '') || link.textContent.trim();
  return { id, name, cover: img?.getAttribute('src') || '' };
}

// Writes made from this page refresh it once the server confirms; a write
// from elsewhere drops the game's seen copy.
export function watchGameWrites(store) {
  store.addEventListener('sent', ev => {
    const op = ev.detail.op;
    const touched = op.effect?.id ?? op.effect?.entry?.id ?? op.effect?.ids?.[0];
    for (const id of op.effect?.ids ?? [touched]) seen.delete(id);
    if (state.id && touched === state.id && location.hash.startsWith('#game/')) reload();
  });
}

function editChange(id, change, msg) {
  return actions.editItem(id, change, msg).then(() => reload()).catch(() => {});
}

function addButtons(game) {
  const add = status => () => {
    actions.addGame(game, status).then(() => reload()).catch(() => {});
    if (status === 'completed') setTimeout(() => openFinishSheet(game.id), 50);
  };
  return html`<div class="card"><h2 class="visually-hidden">Add to your library</h2>
    <p class="muted" style="margin-bottom:var(--sp-3)">Not in your library yet.</p>
    <div class="chips">
      ${STATUSES.filter(s => s.value !== 'paused' && s.value !== 'abandoned').map(s => html`<button type="button" class="chip" data-add-status=${s.value}
        @click=${add(s.value)}>${icon(s.icon)}${s.label}</button>`)}
      <button type="button" class="chip" @click=${() => addToList(game)}>${icon('list')}Add to a list</button>
    </div></div>`;
}

function yourRow(e, item) {
  return html`<section class="your-row card" aria-label="Your game">
    ${statusControl(e.status, status => {
      if (status === e.status) return;
      if (status === 'completed') openFinishSheet(e.id);
      else actions.setStatus(e.id, status).catch(() => {});
    })}
    <div class="row row-wrap">
      ${ratingInput(e.rating, v => actions.rate(e.id, v).catch(() => {}), { display: ratingDisplay() })}
      <span class="spacer"></span>
      <span class="muted"><strong class="num" style="color:var(--text)">${formatMinutes(e.minutes)}</strong> played</span>
    </div>
    ${timeChips(e.id, { finishChip: e.status !== 'completed' })}
    <div class="row row-wrap">
      ${app.timer?.game_id === e.id ? html`<span class="muted">Session running…</span>`
        : html`<button type="button" class="btn btn-sm btn-secondary" @click=${() => startTimer(e.id)}>${icon('play')}Start session timer</button>`}
      <button type="button" class="btn btn-sm btn-ghost" @click=${() => addToList({ id: e.id, name: e.name })}>${icon('list')}Add to list</button>
      ${item?.completed_at && e.status === 'completed' ? html`<span class="muted">Finished ${formatDay(item.completed_at.slice(0, 10))}</span>` : nothing}
    </div>
  </section>`;
}

function playthroughList(pts) {
  if (!pts.length) return nothing;
  return html`<section class="section" aria-labelledby="pt-title">
    <div class="section-head"><h2 id="pt-title">Playthroughs</h2></div>
    <ol class="timeline">${pts.map(p => html`<li>
      <div class="row row-wrap">
        <strong>${p.started_on ? formatDay(p.started_on) : 'Start unknown'}${p.finished_on ? html` → ${formatDay(p.finished_on)}` : p.current ? ' → now' : ''}</strong>
        ${p.completion ? html`<span class="status ${p.completion === 'dropped' ? 'status-abandoned' : 'status-completed'}">${completionLabel(p.completion)}</span>` : nothing}
        ${p.rating ? stars(p.rating, { display: ratingDisplay() }) : nothing}
        <span class="spacer"></span>
        <button type="button" class="btn btn-sm btn-ghost" aria-label="Edit playthrough" @click=${() => editPlaythrough(p)}>${icon('pencil')}</button>
      </div>
      <div class="muted">${formatMinutes(p.minutes)}${p.platform ? ` · ${p.platform}` : ''}${p.note ? ` · ${p.note}` : ''}</div>
    </li>`)}</ol>
  </section>`;
}

function editPlaythrough(p) {
  const draft = { started_on: p.started_on || '', finished_on: p.finished_on || '', completion: p.completion || '', note: p.note || '', platform: p.platform || '' };
  openSheet({
    title: 'Playthrough',
    content: s => html`<div class="form-grid">
      <label class="field"><span>Started</span><input class="input" type="date" .value=${draft.started_on} @change=${e => { draft.started_on = e.target.value; }}></label>
      <label class="field"><span>Finished</span><input class="input" type="date" .value=${draft.finished_on} @change=${e => { draft.finished_on = e.target.value; }}></label>
      <label class="field"><span>Completion</span><select class="select" @change=${e => { draft.completion = e.target.value; }}>
        <option value="" ?selected=${!draft.completion}>Not set</option>
        ${[...COMPLETIONS, { value: 'dropped', label: 'Dropped' }].map(c => html`<option value=${c.value} ?selected=${draft.completion === c.value}>${c.label}</option>`)}
      </select></label>
      <label class="field"><span>Platform</span><input class="input" maxlength="64" .value=${draft.platform} @input=${e => { draft.platform = e.target.value; }}></label>
      <label class="field"><span>Note</span><textarea class="textarea" .value=${draft.note} @input=${e => { draft.note = e.target.value; }}></textarea></label>
    </div>`,
    footer: s => html`<button type="button" class="btn btn-danger" @click=${() => s.close('delete')}>${icon('trash-2')}Delete</button>
      <span class="spacer"></span><button type="button" class="btn btn-primary" @click=${() => s.close('save')}>Save</button>`,
  }).closed.then(async choice => {
    try {
      if (choice === 'save') {
        const res = await api.patch(`/api/v1/playthroughs/${p.id}`, draft);
        undoable('Playthrough saved', res);
      } else if (choice === 'delete') {
        const res = await api.del(`/api/v1/playthroughs/${p.id}`);
        undoable('Playthrough deleted', res);
      }
    } catch (err) {
      toastError(err);
    }
  });
}

async function undoable(message, res) {
  await app.store.sync();
  await reload();
  toast(message, res?.undo_id ? { action: { label: 'Undo', run: async () => { await api.post(`/api/v1/undo/${res.undo_id}`); await app.store.sync(); await reload(); } } } : {});
}

function sessionList(sessions) {
  if (!sessions.length) return nothing;
  return html`<section class="section" aria-labelledby="ss-title">
    <div class="section-head"><h2 id="ss-title">Sessions</h2></div>
    <ul class="list">${repeat(sessions, s => s.id, s => html`<li class="list-row">
      <div class="grow"><div class="title num">${formatMinutes(s.minutes)}</div>
        <div class="sub">${formatDay(s.played_on)}${s.source === 'timer' ? ' · timer' : ''}${s.note ? ` · ${s.note}` : ''}</div></div>
      <button type="button" class="btn btn-ghost btn-icon" aria-label=${`Delete the ${formatMinutes(s.minutes)} session on ${formatDay(s.played_on)}`}
        @click=${async () => {
          try { undoable('Session deleted', await api.del(`/api/v1/sessions/${s.id}`)); } catch (err) { toastError(err); }
        }}>${icon('trash-2')}</button>
    </li>`)}</ul>
  </section>`;
}

// Draft fields bind .value without live(): a background redraw (a sync, a
// write elsewhere) must not reset what's being typed to the saved value.
// Suggested mood tags from the catalog (cozy, co-op, short…) that the
// game doesn't have yet: one tap adds one.
function moodSuggestions(e, item) {
  const have = new Set((item.tags || []).map(t => t.toLowerCase()));
  const sugs = (state.data?.moods || []).filter(m => !have.has(m));
  if (!sugs.length) return nothing;
  return html`<div class="chips" style="margin-top:var(--sp-2)" aria-label="Suggested tags"><span class="muted">Suggested:</span>
    ${sugs.map(m => html`<button type="button" class="chip" @click=${() => editChange(e.id, { tags: [...(item.tags || []), m] }, `Tagged ${m}`)}>${icon('plus')}${m}</button>`)}</div>`;
}

function details(e, item, game) {
  const tagSugs = tagSuggestions(app.store.entries(), '', 30);
  const platformChoices = [...new Set([...(game.platforms || []), ...(item.platforms || []), ...(app.prefs.default_platforms || [])])];
  const owned = new Set(item.platforms || []);
  return html`<section class="card form-grid" aria-labelledby="mine-title">
    <h2 id="mine-title" style="font-size:var(--fs-18)">Your details</h2>
    <div class="field"><span>Owned on</span>
      <div class="chips">${platformChoices.map(p => html`<button type="button" class="chip" aria-pressed=${String(owned.has(p))}
        @click=${() => editChange(e.id, { platforms: owned.has(p) ? [...owned].filter(x => x !== p) : [...owned, p] }, 'Platforms saved')}>${p}</button>`)}
        ${platformChoices.length ? nothing : html`<span class="muted">No platform data.</span>`}</div></div>
    <label class="field"><span>Format</span>
      <select class="select" @change=${ev => editChange(e.id, { medium: ev.target.value }, 'Format saved')}>
        ${[['', 'Not set'], ['digital', 'Digital'], ['physical', 'Physical']].map(([v, l]) => html`<option value=${v} ?selected=${item.medium === v}>${l}</option>`)}
      </select></label>
    <div class="field"><span>Tags</span>
      <div class="chips">${(item.tags || []).map(t => html`<button type="button" class="chip is-on chip-remove" aria-label=${`Remove tag ${t}`}
        @click=${() => editChange(e.id, { tags: item.tags.filter(x => x !== t) }, 'Tag removed')}>${t}${icon('x')}</button>`)}</div>
      <input class="input" list="tag-options" maxlength="40" placeholder="Add a tag and press Enter" @keydown=${ev => {
        if (ev.key !== 'Enter' || !ev.target.value.trim()) return;
        ev.preventDefault();
        const t = ev.target.value.trim();
        ev.target.value = '';
        editChange(e.id, { tags: [...(item.tags || []), t] }, `Tagged ${t}`);
      }}>
      <datalist id="tag-options">${tagSugs.map(t => html`<option value=${t}></option>`)}</datalist>
      ${moodSuggestions(e, item)}</div>
    <label class="field"><span>Notes (private)</span>
      <textarea class="textarea" maxlength="10000" .value=${item.notes || ''}
        @change=${ev => ev.target.value !== (item.notes || '') && editChange(e.id, { notes: ev.target.value }, 'Notes saved')}></textarea></label>
    <div class="row row-wrap">
      <label class="field date-field"><span>Started</span><input class="input" type="date" .value=${(item.started_at || '').slice(0, 10)} max=${localDay()}
        @change=${ev => editChange(e.id, { started_at: ev.target.value }, 'Start date saved')}></label>
      <label class="field date-field"><span>Finished</span><input class="input" type="date" .value=${(item.completed_at || '').slice(0, 10)} max=${localDay()}
        ?disabled=${e.status !== 'completed'} @change=${ev => editChange(e.id, { completed_at: ev.target.value }, 'Finish date saved')}></label>
    </div>
    <button type="button" class="btn btn-danger" @click=${async () => {
      if (await confirmSheet({ title: `Remove ${e.name}?`, message: 'Its playthroughs, sessions and notes go too. You can undo for a few minutes.', confirm: 'Remove', danger: true })) {
        actions.removeGame(e.id).then(() => reload()).catch(() => {});
      }
    }}>${icon('trash-2')}Remove from library</button>
  </section>`;
}

function about(game) {
  const long = (game.summary || '').length > 320;
  return html`<section class="section" aria-labelledby="about-title">
    <div class="section-head"><h2 id="about-title">About</h2></div>
    ${game.summary ? html`<p class=${`summary ${long && !state.aboutOpen ? 'clamped' : ''}`}>${game.summary}</p>
      ${long ? html`<button type="button" class="link" @click=${() => { state.aboutOpen = !state.aboutOpen; app.redraw(); }}>${state.aboutOpen ? 'Less' : 'More'}</button>` : nothing}`
      : html`<p class="muted">No description yet.</p>`}
    ${game.storyline && state.aboutOpen ? html`<p class="summary" style="margin-top:var(--sp-3)">${game.storyline}</p>` : nothing}
  </section>`;
}

export function gameView(route) {
  const d = state.data;
  if (state.error) {
    return html`<div class="page">${empty({ iconName: 'triangle-alert', title: state.error.status === 404 ? 'Game not found' : 'Couldn’t load this game',
      body: state.error.message, action: html`<button type="button" class="btn btn-secondary" @click=${reload}>Try again</button>` })}</div>`;
  }
  const local = app.store.get(route.id);
  if (!d) {
    // Render instantly from the local index, or from the tapped tile,
    // while the page loads.
    const seed = local || tappedTile(route.id);
    return html`<div class="page"><div class="game-hero">${seed ? html`${backdrop(seed.cover)}${cover(seed, { eager: true })}<div><h1>${seed.name}</h1></div>` : html`<div class="cover skeleton"></div><div class="skeleton skeleton-line" style="width:60%"></div>`}</div>
      ${local ? html`<div class="game-layout"><div>${yourRow(local, null)}</div></div>` : nothing}</div>`;
  }
  const game = d.game;
  const e = local && d.item ? { ...local } : null;
  const owned = new Set(d.item?.platforms || []);
  return html`<div class="page">
    <section class="game-hero" aria-labelledby="game-title">
      ${backdrop(game.cover)}
      ${cover({ name: game.name, cover: game.cover }, { eager: true })}
      <div>
        <h1 id="game-title">${game.name}</h1>
        <div class="facts">
          <span><strong>${releaseYear(game.release_date) || 'TBA'}</strong>${game.release_date ? ` · ${formatRelease(game.release_date)}` : ''}</span>
          ${game.critic_count ? html`<span>Critics <strong class="num">${game.critic_rating}</strong></span>` : nothing}
          ${e ? html`<span>${statusLabel(e.status)}</span>` : nothing}
        </div>
        ${game.platforms.length ? html`<div class="chips" style="margin-top:var(--sp-2)" aria-label="Platforms">
          ${game.platforms.map(p => html`<span class=${`chip ${owned.has(p) ? 'is-on' : ''}`} title=${owned.has(p) ? 'You own it here' : ''}>${p}</span>`)}</div>` : nothing}
      </div>
    </section>
    <div class="game-layout">
      <div>
        ${e ? yourRow(e, d.item) : addButtons(game)}
        ${e ? playthroughList(d.playthroughs) : nothing}
        ${about(game)}
        ${gameSocialSection(game)}
        ${gameExtras(d)}
        ${e ? sessionList(d.sessions) : nothing}
      </div>
      <aside>${e && d.item ? details(e, d.item, game) : nothing}</aside>
    </div>
  </div>`;
}
