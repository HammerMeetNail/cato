// search-box.js — the one search box in the header: your library first
// (instant, on-device), then every game, then "see all results". On the
// Library screen it filters the grid directly.
import { html, nothing, live } from '../ui/lit.js';
import { icon } from '../ui/icons.js';
import { cover, statusPill } from '../ui/parts.js';
import { api } from '../core/api.js';
import { query, parseSearch } from '../core/filter.js';
import { releaseYear } from '../core/format.js';
import { href, go } from '../core/router.js';
import { app } from './app-state.js';
import * as actions from './actions.js';

const st = { q: '', open: false, active: -1, catalog: [], loading: false };
let timer = null;
let ctl = null;

function close() {
  st.open = false;
  st.active = -1;
  app.redraw();
}

function results() {
  const q = st.q.trim();
  if (!q) return [];
  const mine = query(app.store.entries(), { search: parseSearch(q) }).slice(0, 5)
    .map(e => ({ kind: 'mine', id: e.id, entry: e }));
  const ids = new Set(mine.map(m => m.id));
  const cat = st.catalog.filter(g => !ids.has(g.id)).slice(0, 6).map(g => ({ kind: 'catalog', id: g.id, entry: g }));
  return [...mine, ...cat, { kind: 'all', id: 'all' }];
}

function fetchCatalog() {
  clearTimeout(timer);
  ctl?.abort();
  const q = st.q.trim();
  if (q.length < 2 || q.startsWith('$') || q.startsWith('@')) {
    st.catalog = [];
    return;
  }
  st.loading = true;
  timer = setTimeout(async () => {
    ctl = new AbortController();
    try {
      st.catalog = (await api.get(`/api/v1/typeahead?q=${encodeURIComponent(q)}`, { signal: ctl.signal })).catalog;
    } catch {
      st.catalog = [];
    }
    st.loading = false;
    app.redraw();
  }, 150);
}

function choose(item) {
  const q = st.q.trim();
  close();
  if (!item || item.kind === 'all') {
    go(href('discover/search', { q }));
  } else {
    go(`#game/${item.id}`);
  }
  st.q = '';
  document.getElementById('global-search')?.blur();
}

export function focusSearch() {
  const el = document.querySelector('#lib-search') || document.getElementById('global-search');
  el?.focus();
  el?.select?.();
}

export function searchBox(route) {
  // On the Library the page's own search box filters in place.
  if (route.name === 'library') return html`<span class="spacer"></span>`;
  const list = st.open ? results() : [];
  return html`<div class="search" role="search" style="max-width:640px" @focusout=${e => {
      if (!e.currentTarget.contains(e.relatedTarget)) setTimeout(close, 120);
    }}>
    ${icon('search', 'icon-search')}
    <input id="global-search" type="search" placeholder="Search your library and all games" aria-label="Search your library and all games"
      role="combobox" aria-expanded=${String(st.open && list.length > 0)} aria-controls="global-search-list" aria-autocomplete="list"
      aria-activedescendant=${st.active >= 0 ? `gs-${st.active}` : nothing}
      autocomplete="off" autocapitalize="none" spellcheck="false" enterkeyhint="search" .value=${live(st.q)}
      @focus=${() => { st.open = true; app.redraw(); }}
      @input=${e => { st.q = e.target.value; st.open = true; st.active = -1; fetchCatalog(); app.redraw(); }}
      @keydown=${e => {
        const items = results();
        if (e.key === 'ArrowDown') { e.preventDefault(); st.active = Math.min(items.length - 1, st.active + 1); app.redraw(); }
        else if (e.key === 'ArrowUp') { e.preventDefault(); st.active = Math.max(-1, st.active - 1); app.redraw(); }
        else if (e.key === 'Enter' && st.q.trim()) {
          e.preventDefault();
          choose(st.active >= 0 ? items[st.active] : (st.q.trim().match(/^[$@]/) ? null : items.find(i => i.kind !== 'all') ?? null));
        } else if (e.key === 'Escape') { st.q = ''; close(); e.target.blur(); }
      }}>
    <kbd aria-hidden="true" class="hide-mobile">/</kbd>
    ${st.open && list.length ? html`<div class="menu search-dropdown" id="global-search-list" role="listbox" aria-label="Search results">
      ${list.map((it, i) => it.kind === 'all'
        ? html`<a role="option" id=${`gs-${i}`} aria-selected=${String(i === st.active)} class="list-row" href=${href('discover/search', { q: st.q.trim() })}
            @click=${e => { e.preventDefault(); choose(it); }}>${icon('compass')}<div class="grow"><div class="title">Search all games for “${st.q.trim()}”</div></div></a>`
        : html`<div class="list-row" role="option" id=${`gs-${i}`} aria-selected=${String(i === st.active)}>
            <a href=${`#game/${it.id}`} @click=${e => { e.preventDefault(); choose(it); }} class="row grow" style="color:inherit;text-decoration:none;min-width:0">
              ${cover(it.entry, { cls: 'thumb' })}
              <div class="grow"><div class="title">${it.entry.name}</div>
                <div class="sub">${it.kind === 'mine' ? 'In your library' : releaseYear(it.entry.release_date) || 'TBA'}</div></div></a>
            ${it.kind === 'mine' || it.entry.status ? statusPill(it.kind === 'mine' ? it.entry.status : it.entry.status)
              : html`<button type="button" class="chip" @mousedown=${e => e.preventDefault()} @click=${() => actions.addGame(it.entry, 'wishlist').catch(() => {})}>Want</button>`}
          </div>`)}
      ${st.loading ? html`<div class="menu-label">Searching…</div>` : nothing}
    </div>` : nothing}
  </div>`;
}
