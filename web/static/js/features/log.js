// log.js — the ⊕ Log sheet: search first; with an empty query it lists
// what you're playing and recently touched, each with one-tap chips. Typing
// shows your library first, then the catalog, with text-labelled shelf
// chips. Two taps to log time to a game you're playing: ⊕ → +1h.
import { html, nothing, repeat, live } from '../ui/lit.js';
import { icon } from '../ui/icons.js';
import { openSheet } from '../ui/sheet.js';
import { cover, statusPill } from '../ui/parts.js';
import { api } from '../core/api.js';
import { query, parseSearch } from '../core/filter.js';
import { formatMinutes, releaseYear, statusLabel } from '../core/format.js';
import { app } from './app-state.js';
import * as actions from './actions.js';
import { openFinishSheet } from './sheets.js';
import { go } from '../core/router.js';

let open = null;

function libraryRow(e, close) {
  const chips = e.status === 'playing' || e.status === 'paused'
    ? html`${[30, 60, 120].map(m => html`<button type="button" class="chip chip-time" @click=${() => { actions.logTime(e.id, m).catch(() => {}); close(); }}>+${formatMinutes(m)}</button>`)}
      <button type="button" class="chip" @click=${() => { close(); openFinishSheet(e.id); }}>${icon('circle-check')}Finish</button>`
    : e.status === 'completed' || e.status === 'abandoned'
      ? html`<button type="button" class="chip" @click=${() => { actions.setStatus(e.id, 'playing').catch(() => {}); close(); }}>${icon('play')}Play again</button>`
      : html`<button type="button" class="chip" @click=${() => { actions.setStatus(e.id, 'playing').catch(() => {}); close(); }}>${icon('play')}Start</button>
        ${e.status !== 'wishlist' ? nothing : html`<button type="button" class="chip" @click=${() => { actions.setStatus(e.id, 'backlog').catch(() => {}); close(); }}>Backlog</button>`}`;
  return html`<div class="log-row" data-game-id=${e.id}>
    ${cover(e, { cls: 'thumb' })}
    <div class="grow" style="min-width:0">
      <a class="name" href=${`#game/${e.id}`} @click=${() => close()}>${e.name}</a>
      <div class="row muted" style="font-size:var(--fs-14)">${statusPill(e.status)}<span class="num">${e.minutes ? formatMinutes(e.minutes) : ''}</span></div>
    </div>
    <div class="chips">${chips}</div>
  </div>`;
}

function catalogRow(g, close) {
  if (g.status) {
    const e = app.store.get(g.id);
    if (e) return libraryRow(e, close);
  }
  const add = status => () => { actions.addGame(g, status).catch(() => {}); close(); };
  return html`<div class="log-row" data-game-id=${g.id}>
    ${cover(g, { cls: 'thumb' })}
    <div class="grow" style="min-width:0">
      <a class="name" href=${`#game/${g.id}`} @click=${() => close()}>${g.name}</a>
      <div class="muted" style="font-size:var(--fs-14)">${releaseYear(g.release_date) || 'TBA'}${g.platforms?.length ? ` · ${g.platforms.slice(0, 3).join(', ')}` : ''}</div>
    </div>
    <div class="chips">
      <button type="button" class="chip" @click=${add('wishlist')}>${icon('bookmark')}${statusLabel('wishlist')}</button>
      <button type="button" class="chip" @click=${add('backlog')}>${icon('layers')}Backlog</button>
      <button type="button" class="chip" @click=${add('playing')}>${icon('play')}Playing</button>
    </div>
  </div>`;
}

// openLogSheet opens the sheet, optionally pre-searched (Share Target).
export function openLogSheet(initial = '') {
  if (open) return open;
  const state = { q: initial, catalog: [], loading: false, error: null };
  let ctl = null;
  let timer = null;
  const search = s => {
    clearTimeout(timer);
    ctl?.abort();
    if (state.q.trim().length < 2) {
      state.catalog = [];
      state.loading = false;
      s.update();
      return;
    }
    state.loading = true;
    timer = setTimeout(async () => {
      ctl = new AbortController();
      try {
        const res = await api.get(`/api/v1/typeahead?q=${encodeURIComponent(state.q.trim())}`, { signal: ctl.signal });
        state.catalog = res.catalog;
        state.error = null;
      } catch (err) {
        if (err.name === 'AbortError') return;
        state.error = err;
        state.catalog = [];
      }
      state.loading = false;
      s.update();
    }, 150);
  };
  open = openSheet({
    cls: 'full',
    label: 'Log',
    // One template for every state: switching templates would replace the
    // search input (and drop focus) on the first keystroke.
    content: s => {
      const close = () => s.close();
      const q = state.q.trim();
      const entries = app.store.entries();
      let groups;
      if (!q) {
        const playing = query(entries, { shelf: 'playing' }).concat(query(entries, { shelf: 'paused' }));
        const recent = query(entries.filter(e => e.status !== 'playing' && e.status !== 'paused'), {}).slice(0, 6);
        groups = [
          playing.length ? html`<h3 class="log-group">Playing</h3>${repeat(playing, e => e.id, e => libraryRow(e, close))}` : nothing,
          recent.length ? html`<h3 class="log-group">Recent</h3>${repeat(recent, e => e.id, e => libraryRow(e, close))}` : nothing,
          !entries.length ? html`<p class="muted" style="margin-top:var(--sp-4)">Search for a game you're playing, want to play or have finished.</p>` : nothing,
        ];
      } else {
        const mine = query(entries, { search: parseSearch(q) }).slice(0, 6);
        const mineIds = new Set(mine.map(e => e.id));
        const others = state.catalog.filter(g => !mineIds.has(g.id));
        groups = [
          mine.length ? html`<h3 class="log-group">In your library</h3>${repeat(mine, e => e.id, e => libraryRow(e, close))}` : nothing,
          html`<h3 class="log-group">All games</h3>`,
          others.length ? repeat(others, g => g.id, g => catalogRow(g, close))
            : html`<p class="muted">${state.loading ? 'Searching…' : state.error ? state.error.message : q.length < 2 ? 'Keep typing…' : 'No other games found.'}</p>`,
          html`<p style="margin-top:var(--sp-4)"><a href=${`#discover/search?q=${encodeURIComponent(q)}`} @click=${close}>See all results for “${q}”</a></p>`,
        ];
      }
      return html`${searchBox(s)}${groups}`;
    },
    onClose: () => { open = null; clearTimeout(timer); ctl?.abort(); },
    initialFocus: 'input[type="search"]',
  });
  function searchBox(s) {
    return html`<div class="row" style="position:sticky;top:0;z-index:1;background:var(--surface-1);padding:var(--sp-1) 0 var(--sp-2)">
      <div class="search">
      ${icon('search', 'icon-search')}
      <input type="search" aria-label="Search your library and all games" placeholder="Log a game…" enterkeyhint="search"
        autocomplete="off" autocapitalize="none" spellcheck="false" .value=${live(state.q)}
        @input=${e => { state.q = e.target.value; search(s); s.update(); }}
        @keydown=${e => {
          if (e.key === 'Enter' && state.q.trim()) { s.close(); go(`#discover/search?q=${encodeURIComponent(state.q.trim())}`); }
          // A search field's Escape would only clear the text: close the sheet.
          else if (e.key === 'Escape') { e.preventDefault(); s.close(); }
        }}>
      </div>
      <button type="button" class="btn btn-ghost btn-icon" aria-label="Close" @click=${() => s.close()}>${icon('x')}</button>
    </div>`;
  }
  if (initial) search(open);
  return open;
}
