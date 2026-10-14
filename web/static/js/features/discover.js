// discover.js — Discover: shelves of games to find, and the full catalog
// search results page with facets. Every result shows your status.
import { html, nothing, repeat } from '../ui/lit.js';
import { icon } from '../ui/icons.js';
import { cover, statusPill, empty, skeletonTiles } from '../ui/parts.js';
import { api } from '../core/api.js';
import { releaseYear, statusLabel, formatMinutes } from '../core/format.js';
import { href, go } from '../core/router.js';
import { app } from './app-state.js';
import * as actions from './actions.js';

const shelves = { data: null, error: null, loading: false };
const results = { key: '', items: [], total: 0, next: null, loading: false, error: null };

export async function enterDiscover() {
  if (shelves.loading) return;
  shelves.loading = true;
  try {
    shelves.data = await api.get('/api/v1/discover');
    shelves.error = null;
  } catch (err) {
    shelves.error = err;
  }
  shelves.loading = false;
  app.redraw();
}

// gameCard is a catalog result: cover, title, year, your status or quick
// add chips.
export function gameCard(g) {
  const local = app.store.get(g.id);
  const status = local?.status || g.status;
  return html`<div class="upnext-card" data-game-id=${g.id}>
    <a href=${`#game/${g.id}`} aria-label=${`Open ${g.name}`}>${cover(g)}</a>
    <a class="title" href=${`#game/${g.id}`} style="color:inherit;text-decoration:none">${g.name}</a>
    <span class="reason">${g.reason || releaseYear(g.release_date) || ''}</span>
    ${status ? statusPill(status) : html`<div class="chips">
      <button type="button" class="chip" @click=${() => actions.addGame(g, 'wishlist').catch(() => {})} aria-label=${`Want to play ${g.name}`}>${icon('bookmark')}Want</button>
    </div>`}
  </div>`;
}

export function discoverView() {
  const d = shelves.data;
  return html`<div class="page">
    <header class="page-head"><h1>Discover</h1></header>
    <form class="search" role="search" @submit=${e => {
      e.preventDefault();
      const q = new FormData(e.target).get('q').trim();
      if (q) go(href('discover/search', { q }));
    }}>
      ${icon('search', 'icon-search')}
      <input name="q" type="search" placeholder="Search every game" aria-label="Search every game" enterkeyhint="search" autocomplete="off">
    </form>
    ${!d && shelves.loading ? html`<div class="section">${skeletonTiles(6)}</div>`
      : shelves.error ? empty({ iconName: 'wifi-off', title: 'Discover needs a connection', body: shelves.error.message })
      : (d?.shelves || []).filter(s => s.items.length).map(s => html`<section class="shelf" aria-labelledby=${`shelf-${s.id}`}>
          <div class="section-head"><h2 id=${`shelf-${s.id}`}>${s.title}</h2>${s.subtitle ? html`<span class="muted">${s.subtitle}</span>` : nothing}</div>
          <div class="shelf-row">${repeat(s.items, g => g.id, gameCard)}</div>
        </section>`)}
    ${d ? html`<p class="section muted" style="font-size:var(--fs-12)">Game data from <a href="https://www.igdb.com" target="_blank" rel="noopener noreferrer">IGDB</a>.</p>` : nothing}
  </div>`;
}

// Search results page with facets: platform, release years, rating, and
// whether the game is in your library.
const FACET_KEYS = ['platform', 'year_from', 'year_to', 'min_rating', 'in_library', 'sort', 'genre', 'max_ttb'];
let genres = null;

async function loadGenres() {
  if (genres) return;
  genres = [];
  try { genres = (await api.get('/api/v1/genres')).items; } catch { /* the facet just stays empty */ }
  app.redraw();
}

export async function enterSearch(route, more = false) {
  loadGenres();
  const q = (route.query.q || '').trim();
  const params = new URLSearchParams({ q, limit: '30' });
  for (const k of FACET_KEYS) if (route.query[k]) params.set(k, route.query[k]);
  const key = params.toString();
  if (!more && key === results.key && results.items.length) return;
  if (!more) {
    results.key = key;
    results.items = [];
    results.next = null;
    results.total = 0;
  }
  if (q.length < 2) {
    app.redraw();
    return;
  }
  if (more && results.next) params.set('offset', String(results.next));
  results.loading = true;
  app.redraw();
  try {
    const res = await api.get(`/api/v1/search?${params}`);
    if (results.key !== key) return;
    results.items = more ? [...results.items, ...res.items] : res.items;
    results.total = res.total;
    results.next = res.next_offset ?? null;
    results.error = null;
  } catch (err) {
    results.error = err;
  }
  results.loading = false;
  app.redraw();
}

function facet(route, key, value) {
  go(href('discover/search', { ...route.query, [key]: value }), { replace: true });
}

export function searchView(route) {
  const q = route.query;
  const year = new Date().getFullYear();
  return html`<div class="page">
    <header class="page-head"><h1>${q.q ? html`Results for “${q.q}”` : 'Search games'}</h1></header>
    <div class="results-layout">
      <aside class="form-grid" aria-label="Filters">
        <form class="search" role="search" @submit=${e => {
          e.preventDefault();
          facet(route, 'q', new FormData(e.target).get('q').trim());
        }}>${icon('search', 'icon-search')}<input name="q" type="search" aria-label="Search every game" .value=${q.q || ''} enterkeyhint="search"></form>
        <label class="field"><span>Platform</span><input class="input" placeholder="Switch, PS5, PC…" .value=${q.platform || ''}
          @change=${e => facet(route, 'platform', e.target.value.trim())}></label>
        <div class="row">
          <label class="field" style="flex:1"><span>From</span><input class="input" type="number" min="1970" max=${year + 5} placeholder="1990" .value=${q.year_from || ''}
            @change=${e => facet(route, 'year_from', e.target.value)}></label>
          <label class="field" style="flex:1"><span>To</span><input class="input" type="number" min="1970" max=${year + 5} placeholder=${String(year)} .value=${q.year_to || ''}
            @change=${e => facet(route, 'year_to', e.target.value)}></label>
        </div>
        ${genres?.length ? html`<label class="field"><span>Genre</span>
          <select class="select" @change=${e => facet(route, 'genre', e.target.value)}>
            <option value="">Any</option>
            ${genres.map(g => html`<option value=${String(g.id)} ?selected=${q.genre === String(g.id)}>${g.name}</option>`)}
          </select></label>` : nothing}
        <label class="field"><span>Time to beat</span>
          <select class="select" @change=${e => facet(route, 'max_ttb', e.target.value)}>
            ${[['', 'Any length'], ['2', 'Up to 2 hours'], ['5', 'Up to 5 hours'], ['10', 'Up to 10 hours'], ['20', 'Up to 20 hours'], ['40', 'Up to 40 hours']]
              .map(([v, l]) => html`<option value=${v} ?selected=${(q.max_ttb || '') === v}>${l}</option>`)}
          </select></label>
        <label class="field"><span>Critic rating at least</span>
          <select class="select" @change=${e => facet(route, 'min_rating', e.target.value)}>
            ${[['', 'Any'], ['70', '70+'], ['80', '80+'], ['90', '90+']].map(([v, l]) => html`<option value=${v} ?selected=${(q.min_rating || '') === v}>${l}</option>`)}
          </select></label>
        <label class="field"><span>In my library</span>
          <select class="select" @change=${e => facet(route, 'in_library', e.target.value)}>
            ${[['', 'Either'], ['0', 'Not in my library'], ['1', 'In my library']].map(([v, l]) => html`<option value=${v} ?selected=${(q.in_library || '') === v}>${l}</option>`)}
          </select></label>
        <label class="field"><span>Sort</span>
          <select class="select" @change=${e => facet(route, 'sort', e.target.value)}>
            ${[['', 'Best match'], ['popularity', 'Popular'], ['release_new', 'Newest'], ['release_old', 'Oldest'], ['critic_rating', 'Critic rating'], ['name', 'Title']]
              .map(([v, l]) => html`<option value=${v} ?selected=${(q.sort || '') === v}>${l}</option>`)}
          </select></label>
      </aside>
      <div>
        ${results.total ? html`<p class="muted" style="margin-bottom:var(--sp-3)">${results.total.toLocaleString()} games</p>` : nothing}
        ${results.loading && !results.items.length ? skeletonTiles(9)
          : results.error ? empty({ iconName: 'triangle-alert', title: 'Search failed', body: results.error.message })
          : !results.items.length ? empty({ iconName: 'search', title: (q.q || '').length < 2 ? 'Type at least two letters' : 'No games found', body: 'Try fewer filters or another spelling.' })
          : html`<div class="lib-grid">${repeat(results.items, g => g.id, g => html`<div class="game-tile">
              <a href=${`#game/${g.id}`} aria-label=${`Open ${g.name}`}>${cover(g)}</a>
              <a class="tile-name" href=${`#game/${g.id}`} style="color:inherit;text-decoration:none">${g.name}</a>
              <span class="tile-meta">${releaseYear(g.release_date) || 'TBA'}${g.ttb_minutes ? ` · ${formatMinutes(g.ttb_minutes)}` : ''}${(app.store.get(g.id)?.status || g.status) ? html` · ${statusLabel(app.store.get(g.id)?.status || g.status)}` : nothing}</span>
              ${(app.store.get(g.id)?.status || g.status) ? nothing : html`<div class="chips">
                <button type="button" class="chip" @click=${() => actions.addGame(g, 'wishlist').catch(() => {})}>Want</button>
                <button type="button" class="chip" @click=${() => actions.addGame(g, 'backlog').catch(() => {})}>Backlog</button></div>`}
            </div>`)}</div>
            ${results.next ? html`<p style="text-align:center;margin-top:var(--sp-6)"><button type="button" class="btn btn-secondary" ?disabled=${results.loading}
              @click=${() => enterSearch(route, true)}>${results.loading ? 'Loading…' : 'More results'}</button></p>` : nothing}`}
      </div>
    </div>
  </div>`;
}
