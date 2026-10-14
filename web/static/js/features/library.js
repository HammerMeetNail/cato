// library.js — Library 2.0: instant search over the on-device index
// ($tag and @platform become visible chips), shelf chips with counts,
// sorting, grid or list, density, saved views, multi-select bulk actions,
// long-press / right-click quick actions and desktop keyboard shortcuts.
import { html, nothing, repeat, live, ref } from '../ui/lit.js';
import { icon } from '../ui/icons.js';
import { cover, statusPill, empty, skeletonTiles } from '../ui/parts.js';
import { stars } from '../ui/stars.js';
import { longPress, openMenu } from '../ui/menu.js';
import { openSheet } from '../ui/sheet.js';
import { toast } from '../ui/toast.js';
import { query, parseSearch, formatSearch, shelfCounts, SORTS, tagSuggestions, platformSuggestions } from '../core/filter.js';
import { SHELVES, STATUSES, formatMinutes, releaseYear, plural } from '../core/format.js';
import { href, go } from '../core/router.js';
import { app, ratingDisplay, savePrefs } from './app-state.js';
import { gameMenu, openRateSheet, openTimeSheet } from './sheets.js';
import * as actions from './actions.js';

const PAGE = 120;
const ui = { shown: PAGE, selecting: false, selected: new Set(), draft: null, lastKey: '' };

function routeState(route) {
  const q = route.query;
  return {
    shelf: q.shelf || '',
    text: q.q || '',
    sort: q.sort || app.prefs.library_sort || 'updated',
    view: q.view || app.prefs.library_layout || 'grid',
    dense: q.dense ? q.dense === '1' : app.prefs.density === 'compact',
  };
}

function update(route, patch) {
  const s = { ...routeState(route), ...patch };
  const query = {
    shelf: s.shelf,
    q: s.text,
    sort: s.sort !== (app.prefs.library_sort || 'updated') ? s.sort : '',
    view: s.view !== (app.prefs.library_layout || 'grid') ? s.view : '',
    dense: s.dense !== (app.prefs.density === 'compact') ? (s.dense ? '1' : '0') : '',
  };
  ui.shown = PAGE;
  go(href('library', query), { replace: true });
}

function tile(e, route, index) {
  const lp = longPress(pos => gameMenu(e.id, pos));
  const selected = ui.selected.has(e.id);
  const onClick = ev => {
    lp.click(ev);
    if (ev.defaultPrevented) return;
    if (ui.selecting) {
      ev.preventDefault();
      if (selected) ui.selected.delete(e.id);
      else ui.selected.add(e.id);
      app.redraw();
    }
  };
  const meta = html`${e.rating ? stars(e.rating, { display: ratingDisplay() }) : nothing}
    ${e.minutes ? html`<span class="num">${formatMinutes(e.minutes)}</span>` : nothing}`;
  return html`<a class="game-tile" href=${`#game/${e.id}`} data-game-id=${e.id} data-index=${index}
      aria-selected=${ui.selecting ? String(selected) : nothing} aria-label=${`${e.name}, ${STATUSES.find(s => s.value === e.status)?.label ?? ''}`}
      @click=${onClick} @contextmenu=${lp.contextmenu} @pointerdown=${lp.pointerdown} @pointerup=${lp.pointerup}
      @pointermove=${lp.pointermove} @pointercancel=${lp.pointercancel}>
    <span class="tile-status">${statusPill(e.status)}</span>
    ${ui.selecting ? html`<span class="tile-check" aria-hidden="true">${selected ? icon('check') : nothing}</span>` : nothing}
    ${cover(e, { eager: index < 12 })}
    <span class="tile-name">${e.name}</span>
    <span class="tile-meta">${meta}</span>
  </a>`;
}

function row(e, route, index) {
  const lp = longPress(pos => gameMenu(e.id, pos));
  const selected = ui.selected.has(e.id);
  return html`<a class="list-row game-tile" href=${`#game/${e.id}`} data-game-id=${e.id} data-index=${index}
      aria-selected=${ui.selecting ? String(selected) : nothing}
      @click=${ev => {
        lp.click(ev);
        if (ev.defaultPrevented) return;
        if (ui.selecting) { ev.preventDefault(); selected ? ui.selected.delete(e.id) : ui.selected.add(e.id); app.redraw(); }
      }}
      @contextmenu=${lp.contextmenu} @pointerdown=${lp.pointerdown} @pointerup=${lp.pointerup} @pointermove=${lp.pointermove}>
    ${ui.selecting ? html`<span class="tile-check" style="position:static" aria-hidden="true">${selected ? icon('check') : nothing}</span>` : nothing}
    ${cover(e, { cls: 'thumb', eager: index < 20 })}
    <div class="grow">
      <div class="title">${e.name}</div>
      <div class="sub">${releaseYear(e.release) || ''}${e.platforms.length ? ` · ${e.platforms.join(', ')}` : ''}${e.tags.length ? ` · ${e.tags.map(t => '#' + t).join(' ')}` : ''}</div>
    </div>
    <div class="right">${statusPill(e.status)}${e.rating ? stars(e.rating, { display: ratingDisplay() }) : nothing}
      ${e.minutes ? html`<span class="num muted">${formatMinutes(e.minutes)}</span>` : nothing}</div>
  </a>`;
}

function filterChips(route, s, search) {
  if (!search.tags.length && !search.platforms.length) return nothing;
  const remove = (kind, v) => () => {
    const next = { ...search, [kind]: search[kind].filter(x => x !== v) };
    update(route, { text: formatSearch(next) });
  };
  return html`<div class="chips" aria-label="Active filters">
    ${search.tags.map(t => html`<button type="button" class="chip is-on chip-remove" @click=${remove('tags', t)} aria-label=${`Remove tag filter ${t}`}>${icon('tag')}${t}${icon('x')}</button>`)}
    ${search.platforms.map(p => html`<button type="button" class="chip is-on chip-remove" @click=${remove('platforms', p)} aria-label=${`Remove platform filter ${p}`}>${icon('gamepad-2')}${p}${icon('x')}</button>`)}
  </div>`;
}

// suggestions offers tag/platform completions while a $ or @ token is typed.
function tokenSuggestions(route, s, entries) {
  const m = /(^|\s)([$@])([^\s"]*)$/.exec(s.text);
  if (!m) return nothing;
  const [, , sigil, prefix] = m;
  const list = sigil === '$' ? tagSuggestions(entries, prefix) : platformSuggestions(entries, prefix);
  if (!list.length) return nothing;
  const base = s.text.slice(0, s.text.length - prefix.length - 1);
  return html`<div class="chips" role="listbox" aria-label=${sigil === '$' ? 'Tags' : 'Platforms'}>
    ${list.map(v => html`<button type="button" role="option" class="chip" @click=${() => {
      const token = /\s/.test(v) ? `${sigil}"${v}"` : `${sigil}${v}`;
      update(route, { text: `${base}${token} ` });
      requestAnimationFrame(() => document.getElementById('lib-search')?.focus());
    }}>${sigil === '$' ? icon('tag') : icon('gamepad-2')}${v}</button>`)}
  </div>`;
}

function savedViewsMenu(route, anchor) {
  const views = app.prefs.saved_views || [];
  const current = location.hash.split('?')[1] || '';
  openMenu({
    anchor,
    label: 'Saved views',
    items: [
      { heading: 'Saved views' },
      ...views.map(v => ({ label: v.name, icon: 'bookmark', checked: v.query === current, run: () => go(`#library?${v.query}`) })),
      ...(views.length ? [{ separator: true }] : []),
      { label: 'Save this view…', icon: 'plus', run: () => saveViewSheet(current) },
      ...(views.length ? [{ label: 'Manage views…', icon: 'pencil', run: () => manageViews() }] : []),
    ],
  });
}

function saveViewSheet(queryString) {
  let name = '';
  openSheet({
    title: 'Save view',
    content: s => html`<label class="field"><span>Name</span>
      <input class="input" maxlength="40" autofocus placeholder="Short games on Switch" @input=${e => { name = e.target.value; s.update(); }}
        @keydown=${e => { if (e.key === 'Enter' && name.trim()) s.close(true); }}></label>`,
    footer: s => html`<button type="button" class="btn btn-primary btn-block" ?disabled=${!name.trim()} @click=${() => s.close(true)}>Save</button>`,
  }).closed.then(ok => {
    if (!ok) return;
    const views = [...(app.prefs.saved_views || []), { name: name.trim(), query: queryString }];
    savePrefs({ saved_views: views }).then(() => toast(`Saved “${name.trim()}”`), err => toast(err.message, { kind: 'error' }));
  });
}

function manageViews() {
  openSheet({
    title: 'Saved views',
    content: s => html`<ul class="list">${(app.prefs.saved_views || []).map((v, i) => html`<li class="list-row">
      <div class="grow"><div class="title">${v.name}</div><div class="sub">${v.query || 'All games'}</div></div>
      <button type="button" class="btn btn-ghost btn-icon" aria-label=${`Delete ${v.name}`} @click=${async () => {
        const views = app.prefs.saved_views.filter((_, j) => j !== i);
        await savePrefs({ saved_views: views }).catch(() => {});
        s.update();
      }}>${icon('trash-2')}</button></li>`)}</ul>`,
  });
}

function bulkBar(entries) {
  const ids = [...ui.selected];
  const done = () => { ui.selected.clear(); ui.selecting = false; app.redraw(); };
  return html`<div class="bulk-bar" role="toolbar" aria-label="Selected games">
    <strong>${ids.length} selected</strong>
    <button type="button" class="btn btn-sm btn-ghost" @click=${() => { entries.forEach(e => ui.selected.add(e.id)); app.redraw(); }}>Select all ${entries.length}</button>
    <span class="spacer"></span>
    <button type="button" class="btn btn-sm btn-secondary" ?disabled=${!ids.length} @click=${ev => openMenu({
      anchor: ev.currentTarget, label: 'Move to', items: STATUSES.map(s => ({ label: s.label, icon: s.icon, run: () => { actions.bulk(ids, { status: s.value }).catch(() => {}); done(); } })),
    })}>${icon('layers')}Status</button>
    <button type="button" class="btn btn-sm btn-secondary" ?disabled=${!ids.length} @click=${() => {
      let tag = '';
      openSheet({
        title: `Tag ${plural(ids.length, 'game')}`,
        content: s => html`<label class="field"><span>Tag</span><input class="input" maxlength="40" autofocus @input=${e => { tag = e.target.value; s.update(); }}></label>`,
        footer: s => html`<button type="button" class="btn btn-primary btn-block" ?disabled=${!tag.trim()} @click=${() => s.close(true)}>Add tag</button>`,
      }).closed.then(ok => { if (ok) { actions.bulk(ids, { add_tags: [tag.trim()] }).catch(() => {}); done(); } });
    }}>${icon('tag')}Tag</button>
    <button type="button" class="btn btn-sm btn-danger" ?disabled=${!ids.length} @click=${() => { actions.bulk(ids, { remove: true }).catch(() => {}); done(); }}>${icon('trash-2')}Remove</button>
    <button type="button" class="btn btn-sm btn-ghost" @click=${done}>Done</button>
  </div>`;
}

// keyboard: j/k (or arrows) move between games, Enter opens, s status,
// r rate, l log time, x select.
function onKey(e) {
  if (e.target.closest('input, textarea, select, [role="slider"]') || e.metaKey || e.ctrlKey || e.altKey) return;
  const tiles = [...document.querySelectorAll('.game-tile')];
  if (!tiles.length) return;
  const i = tiles.indexOf(document.activeElement);
  const id = Number(document.activeElement?.dataset?.gameId);
  const cols = Math.max(1, Math.round(document.querySelector('.lib-grid')?.clientWidth / (tiles[0].offsetWidth || 1)) || 1);
  let next = null;
  switch (e.key) {
    case 'j': case 'ArrowRight': next = Math.min(tiles.length - 1, i + 1); break;
    case 'k': case 'ArrowLeft': next = Math.max(0, i - 1); break;
    case 'ArrowDown': next = Math.min(tiles.length - 1, i < 0 ? 0 : i + (document.querySelector('.lib-grid') ? cols : 1)); break;
    case 'ArrowUp': next = Math.max(0, i - (document.querySelector('.lib-grid') ? cols : 1)); break;
    case 's': if (id) { e.preventDefault(); gameMenu(id, { anchor: document.activeElement }); } return;
    case 'r': if (id) { e.preventDefault(); openRateSheet(id); } return;
    case 'l': if (id) { e.preventDefault(); openTimeSheet(id); } return;
    default: return;
  }
  if (next !== null && next >= 0) {
    e.preventDefault();
    tiles[next].focus();
    tiles[next].scrollIntoView({ block: 'nearest' });
  }
}

let observer = null;
function sentinel(total) {
  return ref(el => {
    observer?.disconnect();
    if (!el || ui.shown >= total) return;
    observer = new IntersectionObserver(entries => {
      if (entries.some(x => x.isIntersecting)) {
        ui.shown += PAGE;
        app.redraw();
      }
    }, { rootMargin: '600px' });
    observer.observe(el);
  });
}

export function libraryView(route) {
  const s = routeState(route);
  const key = `${s.shelf}|${s.text}|${s.sort}`;
  if (key !== ui.lastKey) {
    ui.lastKey = key;
    ui.shown = PAGE;
  }
  const all = app.store.entries();
  const search = parseSearch(s.text);
  const counts = shelfCounts(all, search);
  const results = query(all, { search, shelf: s.shelf, sort: s.sort });
  const visible = results.slice(0, ui.shown);
  const loading = !app.store.synced && !all.length;
  const typing = /(^|\s)[$@][^\s"]*$/.test(s.text);

  return html`<div class="page" @keydown=${onKey}>
    <header class="page-head">
      <h1>Library</h1>
      <span class="lib-count num">${plural(results.length, 'game')}</span>
    </header>
    <div class="lib-toolbar">
      <div class="search">
        ${icon('search', 'icon-search')}
        <input id="lib-search" type="search" placeholder="Search your library — $tag, @platform" aria-label="Search your library"
          autocomplete="off" autocapitalize="none" spellcheck="false" enterkeyhint="search" .value=${live(s.text)}
          @input=${e => update(route, { text: e.target.value })}>
        ${s.text ? html`<button type="button" class="btn btn-ghost btn-icon search-clear" aria-label="Clear search" @click=${() => update(route, { text: '' })}>${icon('x')}</button>` : nothing}
      </div>
      ${typing ? tokenSuggestions(route, s, all) : filterChips(route, s, search)}
      <div class="chips chips-scroll" role="tablist" aria-label="Shelves">
        ${SHELVES.map(sh => html`<button type="button" role="tab" class="chip" aria-selected=${String(s.shelf === sh.value)}
          @click=${() => update(route, { shelf: sh.value })}>${sh.label} <span class="count num">${counts[sh.value] || 0}</span></button>`)}
      </div>
      <div class="lib-controls">
        <label class="visually-hidden" for="lib-sort">Sort by</label>
        <select id="lib-sort" class="select" @change=${e => update(route, { sort: e.target.value })}>
          ${SORTS.map(o => html`<option value=${o.value} ?selected=${o.value === s.sort}>${o.label}</option>`)}
        </select>
        <div class="segmented" role="radiogroup" aria-label="Layout">
          <button type="button" role="radio" aria-checked=${String(s.view === 'grid')} aria-label="Grid" @click=${() => update(route, { view: 'grid' })}>${icon('layout-grid')}</button>
          <button type="button" role="radio" aria-checked=${String(s.view === 'list')} aria-label="List" @click=${() => update(route, { view: 'list' })}>${icon('rows-3')}</button>
        </div>
        <button type="button" class="btn btn-sm btn-ghost" aria-label="Compact layout" aria-pressed=${String(s.dense)} @click=${() => update(route, { dense: !s.dense })}>${icon('grid-3x3')}<span class="label">${s.dense ? 'Compact' : 'Comfortable'}</span></button>
        <span class="spacer"></span>
        <button type="button" class="btn btn-sm btn-ghost" aria-label="Saved views" @click=${e => savedViewsMenu(route, e.currentTarget)}>${icon('bookmark')}<span class="label">Views</span></button>
        <button type="button" class="btn btn-sm btn-ghost" aria-label="Select games" aria-pressed=${String(ui.selecting)} @click=${() => { ui.selecting = !ui.selecting; ui.selected.clear(); app.redraw(); }}>${icon('square-check')}<span class="label">Select</span></button>
      </div>
    </div>

    ${loading ? skeletonTiles()
      : !results.length ? empty(all.length
        ? { iconName: 'search', title: 'No matches', body: 'Nothing in your library matches. Try another word, or search every game.' }
        : { iconName: 'gamepad-2', title: 'Your library is empty', body: 'Add the games you’re playing, want to play or have finished.',
          action: html`<a class="btn btn-primary" href="#log">${icon('plus')}Add games</a>` })
      : s.view === 'list'
        ? html`<div class=${`lib-list ${s.dense ? 'compact' : ''}`} role="list">${repeat(visible, e => e.id, (e, i) => row(e, route, i))}</div>`
        : html`<div class=${`lib-grid ${s.dense ? 'compact' : ''}`} role="list">${repeat(visible, e => e.id, (e, i) => tile(e, route, i))}</div>`}
    ${visible.length < results.length ? html`<div ${sentinel(results.length)} class="muted" style="padding:var(--sp-6);text-align:center">Loading more…</div>` : nothing}

    ${search.text.length >= 2 ? html`<div class="search-all">
      <a class="list-row" href=${href('discover/search', { q: search.text })}>${icon('compass')}<div class="grow"><div class="title">Search all games for “${search.text}”</div>
        <div class="sub">Find games that aren’t in your library yet</div></div>${icon('chevron-right')}</a>
    </div>` : nothing}
    ${ui.selecting ? bulkBar(results) : nothing}
  </div>`;
}
