import { library, getCoverURL, getGame, searchGamesFull, parseTagQuery, formatTagForQuery, autocompleteTags, autocompletePlatforms, autocompleteGlobalPlatforms } from './api.js';
import { formatYear, releaseLabel, releaseStatus, modalReleaseLabel } from './dates.js';

const VALID_STATUSES = ['wishlist', 'backlog', 'playing', 'completed', 'abandoned'];

// Human labels for statuses (the raw values are lowercase internal codes).
const STATUS_LABELS = {
  wishlist: 'Wishlist',
  backlog: 'Backlog',
  playing: 'Playing',
  completed: 'Completed',
  abandoned: 'Abandoned',
};

// statusBadgeLabel maps an owned game's status to the badge text shown on
// search results ("Completed ✓"), so the badge says WHICH list the game is
// in rather than a generic "In library". Unknown/missing statuses fall back.
export function statusBadgeLabel(status) {
  return `${STATUS_LABELS[status] || 'In library'} ✓`;
}

// Hash routing utilities
export function getHashStatus() {
  const hash = window.location.hash.slice(1);
  if (!hash || hash.startsWith('game/') || hash.startsWith('search/')) return '';
  if (VALID_STATUSES.includes(hash)) return hash;
  return '';
}

export function getHashGameId() {
  const hash = window.location.hash.slice(1);
  if (!hash.startsWith('game/')) return null;
  const id = parseInt(hash.slice(5), 10);
  return isNaN(id) ? null : id;
}

export function getHashSearch() {
  const hash = window.location.hash.slice(1);
  if (!hash.startsWith('search/')) return null;
  return decodeURIComponent(hash.slice(7));
}

export function setHash(status) {
  if (status) {
    const target = '#' + status;
    if (window.location.hash !== target) {
      window.location.hash = target;
    }
  } else {
    if (window.location.hash) {
      history.replaceState(null, '', window.location.pathname + window.location.search);
    }
  }
}

export function setGameHash(gameId) {
  const target = '#game/' + gameId;
  if (window.location.hash !== target) {
    history.replaceState(null, '', window.location.pathname + window.location.search + target);
  }
}
const PAGE_SIZE = 60;
const SEARCH_PAGE_SIZE = 24;

// Pagination state — statuses is multi-select (empty = All), like Nabu's historyChoreFilter.
// currentStatus is kept as legacy alias for single-status hash deep-links; statuses is the source of truth.
let paginationState = {
  currentStatus: '', // deprecated alias, kept for hash compat (first element of statuses)
  statuses: [], // [] = All, otherwise array of VALID_STATUSES
  tagFilter: '',
  platformFilter: '',
  ownedPlatformFilter: '',
  formatFilter: '',
  sort: '',
  offset: 0,
  loading: false,
  hasMore: true,
  pageSize: PAGE_SIZE,
  mode: 'library', // 'library' or 'search'
  searchQuery: '',
  yearFrom: '',
  yearTo: '',
  releaseFrom: '',
  releaseTo: '',
};

function normalizeStatuses(input) {
  if (Array.isArray(input)) {
    return input.map(s => String(s).trim().toLowerCase()).filter(s => VALID_STATUSES.includes(s));
  }
  if (!input) return [];
  const str = String(input).trim();
  if (!str) return [];
  if (str.includes(',')) {
    return str.split(',').map(s => s.trim().toLowerCase()).filter(s => VALID_STATUSES.includes(s));
  }
  return VALID_STATUSES.includes(str.toLowerCase()) ? [str.toLowerCase()] : [];
}

function currentStatuses() {
  return Array.isArray(paginationState.statuses) ? paginationState.statuses : [];
}

function setStatuses(arr) {
  const norm = normalizeStatuses(arr);
  paginationState.statuses = norm;
  paginationState.currentStatus = norm[0] || '';
  return norm;
}

// Library release-date filters (persist across reloads in this session).
const libraryFilters = {
  yearFrom: '',
  yearTo: '',
  releaseFrom: '',
  releaseTo: '',
  format: '',
  sort: '',
};

let scrollListenerAttached = false;

// Contemporary platform ranking for autocomplete: when user types "ps", ps5/ps4 should surface first.
const CONTEMPORARY_PLATFORM_RANK = {
  'ps5': 0, 'playstation 5': 0,
  'ps4': 1, 'playstation 4': 1,
  'ps3': 2, 'playstation 3': 2,
  'ps2': 3, 'playstation 2': 3,
  'ps1': 4, 'playstation': 4, 'psx': 4,
  'xbox series x|s': 5, 'xsx': 5, 'xss': 5, 'series x': 5,
  'nintendo switch 2': 6, 'sw2': 6, 'switch 2': 6, 'ns2': 6,
  'nintendo switch': 7, 'switch': 7, 'ns': 7, 'swi': 7,
  'xbox one': 8, 'xb1': 8, 'xone': 8,
  'xbox 360': 9, 'x360': 9,
  'pc (microsoft windows)': 10, 'win': 10, 'pc': 10,
};
function rankPlatformSuggestions(list, query) {
  const q = String(query || '').trim().toLowerCase();
  if (!q || !Array.isArray(list) || list.length === 0) return list;
  const isShort = q.length <= 2;
  return [...list].sort((a, b) => {
    const al = String(a).toLowerCase(), bl = String(b).toLowerCase();
    const ar = CONTEMPORARY_PLATFORM_RANK[al] ?? 99;
    const br = CONTEMPORARY_PLATFORM_RANK[bl] ?? 99;
    if (isShort && ar !== br) return ar - br;
    const aPrefix = al.startsWith(q) ? 0 : 1;
    const bPrefix = bl.startsWith(q) ? 0 : 1;
    if (aPrefix !== bPrefix) return aPrefix - bPrefix;
    if (ar !== br) return ar - br;
    return 0;
  });
}

// Active sort/filters for search mode. Scoped to one query: switching
// searches resets them.
const searchFilters = {
  sort: '', yearFrom: '', yearTo: '', minRating: '', includeEditions: false,
  platform: '', ownedPlatform: '', tags: '', tagOp: 'and',
  releaseFrom: '', releaseTo: '',
  inLibrary: '', libraryStatus: '',
};
let searchFiltersQuery = '';
let searchLoadVersion = 0;
let libraryLoadVersion = 0;

function resetSearchFilters(query) {
  if (searchFiltersQuery === query) return;
  searchFiltersQuery = query;
  searchFilters.sort = '';
  searchFilters.yearFrom = '';
  searchFilters.yearTo = '';
  searchFilters.minRating = '';
  searchFilters.includeEditions = false;
  searchFilters.platform = '';
  searchFilters.ownedPlatform = '';
  searchFilters.tags = '';
  searchFilters.tagOp = 'and';
  searchFilters.releaseFrom = '';
  searchFilters.releaseTo = '';
  searchFilters.inLibrary = '';
  searchFilters.libraryStatus = '';
}

// itemsById indexes the currently rendered library items by game_id so that a
// card click (or a #game/<id> deep link) can open the edit modal with the
// item's existing status/rating/playtime/notes without an extra API round-trip.
const itemsById = new Map();

// searchResultsById maps result id to result object for search mode
const searchResultsById = new Map();

// ownedStatuses maps game IDs confirmed (via /api/library/check) to be in
// the user's library to their status, so search-mode cards can badge WHICH
// list a game is in and clicks can open the edit form instead of the
// destructive add form.
const ownedStatuses = new Map();

function indexItems(items, replace = false) {
  if (replace) itemsById.clear();
  if (!items) return;
  for (const item of items) {
    itemsById.set(String(item.game_id), item);
  }
}

function indexSearchResults(results, replace = false) {
  if (replace) searchResultsById.clear();
  if (!results) return;
  for (const result of results) {
    searchResultsById.set(String(result.id), result);
  }
}

// activateStatusTab highlights the tab matching `status` (or the "All" tab when
// status is empty/unknown). Centralized here because library.js already owns
// tab visibility (show/hide on library vs search mode), so the Clear button and
// loadLibrary keep the highlight in sync without duplicating the logic in the
// page bootstrap. With the Nabu-style FAB, the same selection is mirrored onto
// the floating panel's chips.
export function activateStatusTab(status) {
  const statusTabs = document.getElementById('statusTabs');
  if (statusTabs) {
    statusTabs.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
    const tab = statusTabs.querySelector(`.tab[data-status="${status || ''}"]`);
    if (tab) {
      tab.classList.add('active');
    } else {
      const allTab = statusTabs.querySelector('.tab[data-status=""]');
      if (allTab) allTab.classList.add('active');
    }
    statusTabs.querySelectorAll('.tab').forEach(t => t.setAttribute('aria-selected', String(t.classList.contains('active'))));
  }
  try { if (typeof syncStatusFilterPanel === 'function') syncStatusFilterPanel(); } catch {}
}

export async function loadSearchResults(query) {
  const grid = document.getElementById('gameGrid');
  if (!grid) return;

  const loadVersion = ++searchLoadVersion;
  libraryLoadVersion++;

  resetSearchFilters(query);

  // Reset pagination state to search mode
  paginationState = {
    currentStatus: '',
    statuses: [],
    tagFilter: '',
    platformFilter: '',
    offset: 0,
    // Held true across the fetch below so a scroll event landing in this
    // window can't trigger a concurrent loadMore() that appends the same
    // page twice. renderPagedItems clears it once rendered.
    loading: true,
    hasMore: true,
    pageSize: SEARCH_PAGE_SIZE,
    mode: 'search',
    searchQuery: query,
  };

  // Hide both FABs while in search mode (search has its own filterbar)
  const fab = document.getElementById('libFilterFab');
  if (fab) {
    fab.hidden = true;
    closeLibFilterPanel();
  }
  const statusFab = document.getElementById('statusFilterFab');
  if (statusFab) {
    statusFab.hidden = true;
    closeStatusFilterPanel();
  }
  const statusTabs = document.getElementById('statusTabs');
  if (statusTabs) statusTabs.style.display = 'none';
  // Do not leave lifetime library totals visible while this query loads.
  updateSearchStatsStrip(null);

  // Create and show results header (+ sort/filter bar + total count)
  const existingHeader = document.getElementById('searchResultsHeader');
  if (existingHeader) existingHeader.remove();

  const header = document.createElement('div');
  header.id = 'searchResultsHeader';
  header.className = 'search-results-header';
  header.innerHTML = `
    <div class="search-results-header-content">
      <div class="search-results-title">Results for "<span id="searchQueryDisplay">${escapeHTML(query)}</span>"<span class="search-results-count" id="searchTotal"></span></div>
      <button class="search-results-clear" aria-label="Clear search" type="button">✕</button>
    </div>
    ${buildSearchFilterBarHTML()}
  `;

  const searchWrap = document.querySelector('.search-wrap');
  // Search results header is inserted right after the search box. When the
  // SPA tabbed UI is active, .search-wrap lives inside #libraryView, not
  // directly under .container, so insert via the parent element.
  const parent = searchWrap ? searchWrap.parentElement : document.querySelector('.container');
  if (parent) parent.insertBefore(header, searchWrap.nextSibling);

  wireSearchFilterBar(header, query);

  const clearBtn = header.querySelector('.search-results-clear');
  clearBtn.addEventListener('click', () => {
    history.replaceState(null, '', window.location.pathname + window.location.search);
    loadLibrary('');
  });

  grid.innerHTML = '<div class="loading">Loading results...</div>';

  try {
    const { results, total } = await searchGamesFull(query, {
      limit: SEARCH_PAGE_SIZE,
      offset: 0,
      ...filterParams(),
    });
    if (loadVersion !== searchLoadVersion || paginationState.mode !== 'search' || paginationState.searchQuery !== query) return;
    updateSearchTotal(total);
    // Learn which results are already in the library so cards get a badge
    // and clicks open the edit form instead of a destructive "Add" form.
    ownedStatuses.clear();
    const ids = results.map(r => r.id);
    const owned = await library.check(ids);
    if (loadVersion !== searchLoadVersion || paginationState.mode !== 'search' || paginationState.searchQuery !== query) return;
    for (const it of owned) {
      ownedStatuses.set(Number(it.game_id), it.status);
    }
    renderPagedItems(grid, results, true);
  } catch (err) {
    if (loadVersion !== searchLoadVersion || paginationState.mode !== 'search' || paginationState.searchQuery !== query) return;
    paginationState.loading = false;
    grid.innerHTML = `<div class="empty-state">Failed to load results: ${err.message}</div>`;
  }
}

// refreshSearchResults keeps a search view in sync after a library mutation
// such as moving an item between Wishlist and Backlog. It is a no-op for the
// normal library view.
export function refreshSearchResults() {
  if (paginationState.mode !== 'search' || !paginationState.searchQuery) return Promise.resolve();
  return loadSearchResults(paginationState.searchQuery);
}

// filterParams maps searchFilters onto the API's query params.
function filterParams() {
  const p = {};
  if (searchFilters.sort) p.sort = searchFilters.sort;
  if (searchFilters.yearFrom) p.yearFrom = Number(searchFilters.yearFrom);
  if (searchFilters.yearTo) p.yearTo = Number(searchFilters.yearTo);
  if (searchFilters.releaseFrom) p.releaseFrom = searchFilters.releaseFrom;
  if (searchFilters.releaseTo) p.releaseTo = searchFilters.releaseTo;
  if (searchFilters.minRating) p.minRating = Number(searchFilters.minRating);
  if (searchFilters.platform) p.platform = searchFilters.platform;
  if (searchFilters.ownedPlatform) p.ownedPlatform = searchFilters.ownedPlatform;
  if (searchFilters.tags) {
    // tags is raw string like 'rpg "switch 2"'; parse into list for API
    const parsed = parseTagQuery(searchFilters.tags);
    p.tags = parsed.tags;
    p.tagOp = parsed.op;
  }
  if (searchFilters.inLibrary === 'owned') p.inLibrary = true;
  else if (searchFilters.inLibrary === 'not_owned') p.inLibrary = false;
  if (searchFilters.libraryStatus) p.libraryStatus = searchFilters.libraryStatus;
  if (searchFilters.includeEditions) p.includeEditions = true;
  return p;
}

function updateSearchTotal(total) {
  const count = Number(total);
  const el = document.getElementById('searchTotal');
  if (!Number.isFinite(count)) return;
  if (el) el.textContent = count === 1 ? ' · 1 game' : ` · ${count} games`;
  updateSearchStatsStrip(count);
}

// Search results are catalog matches, not lifetime library stats. Keep the
// strip useful in search mode without implying that the finished/playtime
// values apply to the query.
function updateSearchStatsStrip(total) {
  const el = document.getElementById('statsStrip');
  if (!el) return;
  el.dataset.context = 'search';
  el.removeAttribute('role');
  el.removeAttribute('tabindex');
  el.removeAttribute('title');
  if (total == null) {
    el.style.display = 'none';
    return;
  }
  const count = Number(total);
  if (!Number.isFinite(count) || count < 0) {
    el.style.display = 'none';
    return;
  }
  el.textContent = `${count} ${count === 1 ? 'game' : 'games'} found`;
  el.style.display = '';
}

// buildSearchFilterBarHTML renders the collapsible sort/filter controls.
// Friendly, grouped layout with clear section headings and mobile-tuned sizing.
function buildSearchFilterBarHTML() {
  return `
    <details class="search-filterbar">
      <summary>
        <span class="sf-summary-icon" aria-hidden="true"><svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polygon points="22 3 2 3 10 12.46 10 19 14 21 14 12.46 22 3"></polygon></svg></span>
        <span>Filters</span>
        <span class="sf-badge" id="sfBadge" hidden>0</span>
        <span class="sf-chevron" aria-hidden="true"></span>
      </summary>
      <div class="sf-panel">
        <div class="sf-section">
          <h4 class="sf-section-title">Sort &amp; Quality</h4>
          <div class="sf-grid sf-grid--2">
            <label class="sf-field">
              <span class="sf-label">Sort by</span>
              <select id="sfSort">
                <option value="">Relevance</option>
                <option value="release_new">Newest first</option>
                <option value="release_old">Oldest first</option>
                <option value="critic_rating">Critic rating: Highest first</option>
                <option value="critic_rating_low">Critic rating: Lowest first</option>
                <option value="popularity">Most popular</option>
                <option value="name">Name A–Z</option>
              </select>
            </label>
            <label class="sf-field">
              <span class="sf-label">Min critic rating</span>
              <select id="sfMinRating">
                <option value="">Any critic rating</option>
                <option value="60">60+ Good</option>
                <option value="75">75+ Great</option>
                <option value="85">85+ Excellent</option>
                <option value="95">95+ Masterpiece</option>
              </select>
            </label>
          </div>
        </div>

        <div class="sf-section">
          <h4 class="sf-section-title">Platform &amp; Tags</h4>
          <div class="sf-grid sf-grid--2">
            <label class="sf-field">
              <span class="sf-label">Platform (available)</span>
              <input id="sfPlatform" list="sfPlatformList" type="text" placeholder="e.g. ps5, sw2, xsx, win" autocomplete="off" inputmode="search">
              <datalist id="sfPlatformList"></datalist>
            </label>
            <label class="sf-field">
              <span class="sf-label">Tags</span>
              <input id="sfTags" list="sfTagsList" type="text" placeholder='e.g. rpg "co-op"' autocomplete="off" inputmode="search">
              <datalist id="sfTagsList"></datalist>
            </label>
          </div>
          <p class="sf-hint">Platform: names or shortnames (<code>ps5</code>, <code>xsx</code>, <code>sw2</code>, <code>win</code>…). Tags: space = AND, <code>|</code> = OR, quotes for multi-word.</p>
        </div>

        <div class="sf-section">
          <h4 class="sf-section-title">Ownership</h4>
          <div class="sf-grid sf-grid--2">
            <label class="sf-field">
              <span class="sf-label">Owned on</span>
              <input id="sfOwnedPlatform" list="sfOwnedPlatformList" type="text" placeholder="e.g. ps5, sw2, xsx" autocomplete="off" inputmode="search">
              <datalist id="sfOwnedPlatformList"></datalist>
            </label>
          </div>
          <p class="sf-hint">Completed + owned on <code>ps5</code> = only ps5-owned completed (vs available on ps5).</p>
        </div>

        <div class="sf-section">
          <h4 class="sf-section-title">Release Date</h4>
          <div class="sf-grid sf-grid--range">
            <label class="sf-field">
              <span class="sf-label">Year from</span>
              <input id="sfYearFrom" type="number" min="1900" max="2100" inputmode="numeric" placeholder="1994">
            </label>
            <label class="sf-field">
              <span class="sf-label">Year to</span>
              <input id="sfYearTo" type="number" min="1900" max="2100" inputmode="numeric" placeholder="2024">
            </label>
            <label class="sf-field">
              <span class="sf-label">Exact from</span>
              <input id="sfReleaseFrom" type="date">
            </label>
            <label class="sf-field">
              <span class="sf-label">Exact to</span>
              <input id="sfReleaseTo" type="date">
            </label>
          </div>
          <p class="sf-hint">Year and exact date combine as a range. Leave empty for any date.</p>
        </div>

        <div class="sf-section">
          <h4 class="sf-section-title">Library &amp; Editions</h4>
          <div class="sf-grid sf-grid--2">
            <label class="sf-field">
              <span class="sf-label">Collection</span>
              <select id="sfInLibrary">
                <option value="">All games</option>
                <option value="owned">In my library</option>
                <option value="not_owned">Not in library</option>
              </select>
            </label>
            <label class="sf-field" id="sfLibraryStatusWrap" style="display:none">
              <span class="sf-label">Status</span>
              <select id="sfLibraryStatus">
                <option value="">Any status</option>
                <option value="wishlist">Wishlist</option>
                <option value="backlog">Backlog</option>
                <option value="playing">Playing</option>
                <option value="completed">Completed</option>
                <option value="abandoned">Abandoned</option>
              </select>
            </label>
          </div>
          <label class="sf-check"><input id="sfEditions" type="checkbox"> <span>Include editions &amp; packs</span></label>
        </div>

        <div class="sf-actions">
          <button id="sfClear" class="btn btn-secondary" type="button">Clear</button>
          <button id="sfApply" class="btn btn-primary" type="button">Apply filters</button>
        </div>
      </div>
    </details>`;
}

// wireSearchFilterBar restores control values and applies changes. Applying
// re-runs the whole search (page 1, fresh totals); Clear resets to defaults.
function wireSearchFilterBar(header, query) {
  const sortSel = header.querySelector('#sfSort');
  const yearFrom = header.querySelector('#sfYearFrom');
  const yearTo = header.querySelector('#sfYearTo');
  const releaseFrom = header.querySelector('#sfReleaseFrom');
  const releaseTo = header.querySelector('#sfReleaseTo');
  const minRating = header.querySelector('#sfMinRating');
  const platformEl = header.querySelector('#sfPlatform');
  const ownedPlatformEl = header.querySelector('#sfOwnedPlatform');
  const tagsEl = header.querySelector('#sfTags');
  const inLibrary = header.querySelector('#sfInLibrary');
  const libraryStatusWrap = header.querySelector('#sfLibraryStatusWrap');
  const libraryStatus = header.querySelector('#sfLibraryStatus');
  const editions = header.querySelector('#sfEditions');
  if (!sortSel) return;

  sortSel.value = searchFilters.sort;
  yearFrom.value = searchFilters.yearFrom;
  yearTo.value = searchFilters.yearTo;
  if (releaseFrom) releaseFrom.value = searchFilters.releaseFrom;
  if (releaseTo) releaseTo.value = searchFilters.releaseTo;
  minRating.value = searchFilters.minRating;
  if (platformEl) platformEl.value = searchFilters.platform;
  if (ownedPlatformEl) ownedPlatformEl.value = searchFilters.ownedPlatform;
  if (tagsEl) tagsEl.value = searchFilters.tags;
  if (inLibrary) inLibrary.value = searchFilters.inLibrary;
  if (libraryStatus) libraryStatus.value = searchFilters.libraryStatus;
  if (libraryStatusWrap) libraryStatusWrap.style.display = searchFilters.inLibrary === 'owned' ? '' : 'none';
  if (editions) editions.checked = !!searchFilters.includeEditions;

  const updateSearchBadge = () => {
    const badge = header.querySelector('#sfBadge');
    if (!badge) return;
    const cur = {
      sort: sortSel.value,
      yearFrom: yearFrom.value.trim(),
      yearTo: yearTo.value.trim(),
      releaseFrom: releaseFrom ? releaseFrom.value.trim() : '',
      releaseTo: releaseTo ? releaseTo.value.trim() : '',
      minRating: minRating.value,
      platform: platformEl ? platformEl.value.trim() : '',
      ownedPlatform: ownedPlatformEl ? ownedPlatformEl.value.trim() : '',
      tags: tagsEl ? tagsEl.value.trim() : '',
      inLibrary: inLibrary ? inLibrary.value : '',
      libraryStatus: libraryStatus ? libraryStatus.value : '',
      editions: editions && editions.checked ? '1' : '',
    };
    const n = Object.values(cur).filter(Boolean).length;
    badge.textContent = n;
    badge.hidden = n === 0;
    badge.setAttribute('aria-label', n ? `${n} filters active` : '');
  };
  updateSearchBadge();
  // Live badge as user edits
  [sortSel, yearFrom, yearTo, releaseFrom, releaseTo, minRating, platformEl, ownedPlatformEl, tagsEl, inLibrary, libraryStatus, editions].forEach(el => {
    if (!el) return;
    el.addEventListener('input', updateSearchBadge);
    el.addEventListener('change', updateSearchBadge);
  });

  // Populate datalists for platform/tags (best-effort).
  if (platformEl) {
    const platList = header.querySelector('#sfPlatformList');
    autocompleteGlobalPlatforms('').then(list => {
      if (platList && Array.isArray(list)) {
        platList.innerHTML = list.slice(0, 20).map(p => `<option value="${escapeHTML(p)}"></option>`).join('');
      }
    }).catch(() => {});
    // Live autocomplete as user types (debounced) — global list for search.
    let platTimer = null;
    platformEl.addEventListener('input', () => {
      clearTimeout(platTimer);
      platTimer = setTimeout(async () => {
        const q = platformEl.value.trim();
        if (q.length < 1) return;
        try {
          const list = await autocompleteGlobalPlatforms(q);
          const ranked = rankPlatformSuggestions(list, q);
          if (platList && Array.isArray(list)) platList.innerHTML = ranked.map(p => `<option value="${escapeHTML(p)}"></option>`).join('');
        } catch {}
      }, 250);
    });
  }
  if (ownedPlatformEl) {
    const ownedList = header.querySelector('#sfOwnedPlatformList');
    autocompleteGlobalPlatforms('').then(list => {
      if (ownedList && Array.isArray(list)) {
        ownedList.innerHTML = list.slice(0, 20).map(p => `<option value="${escapeHTML(p)}"></option>`).join('');
      }
    }).catch(() => {});
    let ownedTimer = null;
    ownedPlatformEl.addEventListener('input', () => {
      clearTimeout(ownedTimer);
      ownedTimer = setTimeout(async () => {
        const q = ownedPlatformEl.value.trim();
        if (q.length < 1) return;
        try {
          const list = await autocompleteGlobalPlatforms(q);
          const ranked = rankPlatformSuggestions(list, q);
          if (ownedList && Array.isArray(list)) ownedList.innerHTML = ranked.map(p => `<option value="${escapeHTML(p)}"></option>`).join('');
        } catch {}
      }, 250);
    });
  }
  if (tagsEl) {
    const tagsList = header.querySelector('#sfTagsList');
    autocompleteTags('', 100).then(list => {
      if (tagsList && Array.isArray(list)) {
        tagsList.innerHTML = list.map(t => `<option value="${escapeHTML(t)}"></option>`).join('');
      }
    }).catch(() => {});
    let tagTimer = null;
    tagsEl.addEventListener('input', () => {
      clearTimeout(tagTimer);
      const raw = tagsEl.value;
      // Extract last segment being typed for autocomplete
      const last = raw.split(/[\s|]+/).pop().replace(/^"|"$/g, '').trim();
      if (last.length < 1) return;
      tagTimer = setTimeout(async () => {
        try {
          const list = await autocompleteTags(last);
          if (tagsList && Array.isArray(list)) {
            tagsList.innerHTML = list.map(t => `<option value="${escapeHTML(t)}"></option>`).join('');
          }
        } catch {}
      }, 250);
    });
  }

  const apply = () => {
    const clampYear = (v) => {
      const n = parseInt(v, 10);
      return Number.isFinite(n) && n >= 1900 && n <= 2100 ? String(n) : '';
    };
    const clampDate = (v) => {
      const s = String(v || '').trim();
      if (!s) return '';
      // accept YYYY-MM-DD or YYYY
      if (/^\d{4}-\d{2}-\d{2}$/.test(s)) return s;
      if (/^\d{4}$/.test(s)) {
        const y = parseInt(s, 10);
        return y >= 1900 && y <= 2100 ? `${y}-01-01` : '';
      }
      return '';
    };
    searchFilters.sort = sortSel.value;
    searchFilters.yearFrom = clampYear(yearFrom.value);
    searchFilters.yearTo = clampYear(yearTo.value);
    searchFilters.releaseFrom = releaseFrom ? clampDate(releaseFrom.value) : '';
    searchFilters.releaseTo = releaseTo ? clampDate(releaseTo.value) : '';
    searchFilters.minRating = minRating.value;
    searchFilters.platform = platformEl ? platformEl.value.trim().slice(0, 64) : '';
    searchFilters.ownedPlatform = ownedPlatformEl ? ownedPlatformEl.value.trim().slice(0, 64) : '';
    searchFilters.tags = tagsEl ? tagsEl.value.trim().slice(0, 200) : '';
    searchFilters.inLibrary = inLibrary ? inLibrary.value : '';
    searchFilters.libraryStatus = libraryStatus ? libraryStatus.value : '';
    if (searchFilters.inLibrary !== 'owned') searchFilters.libraryStatus = '';
    searchFilters.includeEditions = !!(editions && editions.checked);
    loadSearchResults(query);
  };
  header.querySelector('#sfApply').addEventListener('click', apply);
  if (editions) editions.addEventListener('change', apply);
  if (inLibrary) inLibrary.addEventListener('change', () => {
    if (libraryStatusWrap) libraryStatusWrap.style.display = inLibrary.value === 'owned' ? '' : 'none';
    // Don't auto-apply on change to allow picking status, but if switching away from owned, apply immediately to clear status
    if (inLibrary.value !== 'owned' && searchFilters.libraryStatus) apply();
  });
  // Enter on any input applies
  [yearFrom, yearTo, releaseFrom, releaseTo, platformEl, ownedPlatformEl, tagsEl].forEach(el => {
    if (el) el.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); apply(); } });
  });
  header.querySelector('#sfClear').addEventListener('click', () => {
    sortSel.value = '';
    yearFrom.value = '';
    yearTo.value = '';
    if (releaseFrom) releaseFrom.value = '';
    if (releaseTo) releaseTo.value = '';
    minRating.value = '';
    if (platformEl) platformEl.value = '';
    if (ownedPlatformEl) ownedPlatformEl.value = '';
    if (tagsEl) tagsEl.value = '';
    if (inLibrary) inLibrary.value = '';
    if (libraryStatus) libraryStatus.value = '';
    if (libraryStatusWrap) libraryStatusWrap.style.display = 'none';
    if (editions) editions.checked = false;
    apply();
  });
}

// --- library filters — split into two FABs (Nabu-style) --------------------
// Status: bottom-left, multi-select like Nabu's history filter (All = empty array)
// Advanced: bottom-right, Platform/Tags/Format/Release (badge shows active count)

const STATUS_CHIP_COLORS = {
  wishlist: '#2196f3',
  backlog: '#ff9800',
  playing: '#4caf50',
  completed: '#9c27b0',
  abandoned: '#f44336',
};

// Status FAB state (Nabu parity)
let statusFilterOpen = false;
let statusFilterWired = false;

// While a filter sheet is open, background scroll must not steal wheel/touch
// from the panel. CSS overflow:hidden on .app-shell handles most browsers,
// but iOS Safari and wheel over non-scrollable chrome (header/footer) still
// need JS containment: block scroll outside the active panel.
let filterScrollLockHandler = null;
let filterTouchLockHandler = null;

function isEventInsideFilterPanel(target) {
  if (!target || !target.closest) return false;
  return !!(target.closest('#libFilterPanel') || target.closest('#statusFilterPanel') ||
    target.closest('#libFilterBackdrop') || target.closest('#statusFilterBackdrop'));
}
function lockFilterBodyScroll() {
  if (filterScrollLockHandler) return;
  filterScrollLockHandler = (e) => {
    if (isEventInsideFilterPanel(e.target)) return;
    if (e.cancelable) e.preventDefault();
  };
  filterTouchLockHandler = (e) => {
    if (isEventInsideFilterPanel(e.target)) return;
    if (e.cancelable) e.preventDefault();
  };
  document.addEventListener('wheel', filterScrollLockHandler, { passive: false });
  document.addEventListener('touchmove', filterTouchLockHandler, { passive: false });
}
function unlockFilterBodyScroll() {
  if (libFilterOpen || statusFilterOpen) return;
  if (filterScrollLockHandler) {
    document.removeEventListener('wheel', filterScrollLockHandler, { passive: false });
    filterScrollLockHandler = null;
  }
  if (filterTouchLockHandler) {
    document.removeEventListener('touchmove', filterTouchLockHandler, { passive: false });
    filterTouchLockHandler = null;
  }
}

function buildStatusFilterPanelHTML() {
  const sortedStatuses = [...VALID_STATUSES];
  const statusChips = [
    { status: '', label: 'All' },
    ...sortedStatuses.map(s => ({ status: s, label: STATUS_LABELS[s] || s })),
  ].map(({ status, label }) => {
    const color = STATUS_CHIP_COLORS[status];
    const style = color ? ` style="--chip-color:${color}"` : '';
    return `<button type="button" class="lib-filter-chip${status === '' ? ' lib-filter-all status-all' : ''}" data-status="${escapeHTML(status)}"${style} role="option" aria-selected="false">${escapeHTML(label)}</button>`;
  }).join('');
  return `
    <div class="lib-filter-panel-inner status-filter-inner">
      <div class="lib-filter-chips" id="statusChips" role="listbox" aria-label="Filter by status">
        ${statusChips}
      </div>
    </div>`;
}

function syncStatusFilterPanel() {
  const panel = document.getElementById('statusFilterPanel');
  if (!panel) return;
  const active = currentStatuses();
  const hasFilter = active.length > 0;
  panel.querySelectorAll('.lib-filter-chip[data-status]').forEach(chip => {
    const s = chip.dataset.status || '';
    const isAll = s === '';
    const isActive = isAll ? !hasFilter : active.includes(s);
    chip.classList.toggle('active', isActive);
    chip.setAttribute('aria-selected', String(isActive));
  });
  // Keep legacy hidden .tab row in sync (first status or All)
  const statusTabs = document.getElementById('statusTabs');
  if (statusTabs) {
    const first = active[0] || '';
    statusTabs.querySelectorAll('.tab').forEach(t => {
      const isActive = (t.dataset.status || '') === first && active.length <= 1;
      // For multi, no single tab is active except All when empty
      const shouldActive = !hasFilter ? (t.dataset.status === '') : false;
      // Simpler: only highlight exact single match, otherwise none (All handles empty)
      if (!hasFilter) {
        const isAll = t.dataset.status === '';
        t.classList.toggle('active', isAll);
        t.setAttribute('aria-selected', String(isAll));
      } else if (active.length === 1) {
        const match = t.dataset.status === active[0];
        t.classList.toggle('active', match);
        t.setAttribute('aria-selected', String(match));
      } else {
        t.classList.remove('active');
        t.setAttribute('aria-selected', 'false');
      }
    });
  }
}

function wireStatusFilterPanel(panel) {
  if (!panel || statusFilterWired) return;
  statusFilterWired = true;
  panel.querySelectorAll('.lib-filter-chip[data-status]').forEach(chip => {
    chip.addEventListener('click', () => {
      const status = chip.dataset.status || '';
      const cur = currentStatuses();
      let next;
      if (status === '') {
        // All — clear filter
        next = [];
      } else {
        if (cur.includes(status)) {
          next = cur.filter(s => s !== status);
        } else {
          next = [...cur, status];
        }
      }
      setStatuses(next);
      // If hash was showing a single status, clear it — multi-select is local state
      if (window.location.hash && VALID_STATUSES.includes(window.location.hash.slice(1))) {
        history.replaceState(null, '', window.location.pathname + window.location.search);
      }
      syncStatusFilterPanel();
      // Keep advanced filter's library chips in sync when status FAB changes
      try {
        const advPanel = document.getElementById('libFilterPanel');
        if (advPanel && typeof advPanel._syncLibraryChips === 'function') advPanel._syncLibraryChips();
      } catch {}
      // Reload library with new statuses + existing tag/platform/owned/year filters
      loadLibrary(paginationState.statuses, paginationState.tagFilter, paginationState.platformFilter, paginationState.ownedPlatformFilter);
      // Keep panel open for multi-select (Nabu behavior) — don't auto-close
    });
  });
}

function openStatusFilterPanel() {
  const fab = document.getElementById('statusFilterFab');
  const panel = document.getElementById('statusFilterPanel');
  const btn = document.getElementById('statusFilterBtn');
  const backdrop = document.getElementById('statusFilterBackdrop');
  if (!fab || !panel || !btn) return;
  // Also close the other FAB if open
  if (libFilterOpen) closeLibFilterPanel();
  if (!panel.innerHTML.trim()) {
    panel.innerHTML = buildStatusFilterPanelHTML();
    wireStatusFilterPanel(panel);
  }
  syncStatusFilterPanel();
  panel.hidden = false;
  document.body.classList.add('lib-status-open');
  lockFilterBodyScroll();
  if (backdrop) {
    backdrop.hidden = false;
    backdrop.style.display = 'block';
    if (!backdrop.dataset.wired) {
      backdrop.dataset.wired = '1';
      backdrop.addEventListener('click', (e) => {
        e.preventDefault();
        e.stopPropagation();
        closeStatusFilterPanel();
      });
    }
  }
  // Keep wheel inside the panel from chaining to the page: if the chip
  // list can still scroll in the wheel direction, consume the event.
  const chipsEl = panel.querySelector('.lib-filter-chips');
  if (chipsEl && !chipsEl.dataset.wheelWired) {
    chipsEl.dataset.wheelWired = '1';
    chipsEl.addEventListener('wheel', (e) => {
      const el = e.currentTarget;
      const delta = e.deltaY;
      const atTop = el.scrollTop <= 0;
      const atBottom = el.scrollTop + el.clientHeight >= el.scrollHeight - 1;
      const goingUp = delta < 0;
      const goingDown = delta > 0;
      const canScrollUp = !atTop && goingUp;
      const canScrollDown = !atBottom && goingDown;
      if (canScrollUp || canScrollDown) {
        e.stopPropagation();
      } else if (atTop && atBottom) {
        // Not scrollable at all — still eat the wheel so background doesn't move
        e.preventDefault();
      } else {
        // At boundary but trying to overscroll — block chain
        e.preventDefault();
        e.stopPropagation();
      }
    }, { passive: false });
  }
  if (!panel.dataset.wheelWired) {
    panel.dataset.wheelWired = '1';
    panel.addEventListener('wheel', (e) => {
      if (e.target.closest && e.target.closest('.lib-filter-chips')) return;
      if (e.cancelable) e.preventDefault();
    }, { passive: false });
    panel.addEventListener('touchmove', (e) => {
      if (e.target.closest && e.target.closest('.lib-filter-chips')) return;
      if (e.cancelable) e.preventDefault();
    }, { passive: false });
  }
  requestAnimationFrame(() => {
    panel.classList.add('lib-filter-panel--open');
    if (backdrop) backdrop.classList.add('lib-filter-backdrop--open');
  });
  btn.classList.add('lib-filter-btn--open');
  btn.setAttribute('aria-expanded', 'true');
  statusFilterOpen = true;
}

function closeStatusFilterPanel() {
  const panel = document.getElementById('statusFilterPanel');
  const btn = document.getElementById('statusFilterBtn');
  const backdrop = document.getElementById('statusFilterBackdrop');
  if (!panel || !btn) return;
  panel.classList.remove('lib-filter-panel--open');
  if (backdrop) backdrop.classList.remove('lib-filter-backdrop--open');
  btn.classList.remove('lib-filter-btn--open');
  btn.setAttribute('aria-expanded', 'false');
  statusFilterOpen = false;
  document.body.classList.remove('lib-status-open');
  unlockFilterBodyScroll();
  setTimeout(() => {
    if (!statusFilterOpen) {
      panel.hidden = true;
      if (backdrop) {
        backdrop.hidden = true;
        backdrop.style.display = '';
      }
    }
  }, 180);
}

function toggleStatusFilterPanel() {
  if (statusFilterOpen) closeStatusFilterPanel();
  else openStatusFilterPanel();
}

function ensureStatusFilterFab() {
  const fab = document.getElementById('statusFilterFab');
  const panel = document.getElementById('statusFilterPanel');
  const btn = document.getElementById('statusFilterBtn');
  if (!fab || !panel || !btn) return null;
  if (paginationState.mode !== 'library') {
    fab.hidden = true;
    if (statusFilterOpen) closeStatusFilterPanel();
    return null;
  }
  const libraryView = document.getElementById('libraryView');
  if (libraryView && libraryView.hidden) {
    fab.hidden = true;
    if (statusFilterOpen) closeStatusFilterPanel();
    return null;
  }
  fab.hidden = false;
  if (!panel.innerHTML.trim()) {
    panel.innerHTML = buildStatusFilterPanelHTML();
    wireStatusFilterPanel(panel);
  }
  syncStatusFilterPanel();
  if (!fab.dataset.wired) {
    fab.dataset.wired = '1';
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      toggleStatusFilterPanel();
    });
    document.addEventListener('click', (e) => {
      if (!statusFilterOpen) return;
      if (fab.contains(e.target)) return;
      if (panel.contains(e.target)) return;
      if (e.target.closest('#tagFilterBar')) return;
      // Don't close when clicking the other filter (advanced inline)
      if (e.target.closest('#libFilterPanel') || e.target.closest('#searchAdvancedBtn') || e.target.closest('#libFilterFab')) return;
      // Dismiss before the click reaches a game card or another background
      // control. The filter popup should consume this first outside click.
      e.preventDefault();
      e.stopPropagation();
      closeStatusFilterPanel();
    }, true);
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && statusFilterOpen) {
        e.stopPropagation();
        closeStatusFilterPanel();
      }
    });
  }
  return fab;
}

// Advanced FAB — bottom-right: Platform/Tags/Release (no status)
let libFilterOpen = false;
let libFilterWired = false;

function buildLibFilterPanelHTML() {
  return `
    <div class="lib-filter-panel-inner lib-filter-modern">
      <div class="lib-filter-sheet-handle" aria-hidden="true"><span></span></div>
      <div class="lib-filter-header">
        <div class="lib-filter-header-text">
          <h3 class="lib-filter-title">Filters</h3>
          <p class="lib-filter-subtitle">Refine your library</p>
        </div>
        <button type="button" class="lib-filter-close lib-filter-apply-close" id="libFilterClose" aria-label="Apply filters">
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polyline points="20 6 9 17 4 12"></polyline></svg>
        </button>
      </div>
      <div class="lib-filter-body">
        <div class="lib-filter-card">
          <h4 class="lib-filter-section-title">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><rect x="3" y="3" width="7" height="7" rx="1"></rect><rect x="14" y="3" width="7" height="7" rx="1"></rect><rect x="14" y="14" width="7" height="7" rx="1"></rect><rect x="3" y="14" width="7" height="7" rx="1"></rect></svg>
            Library
          </h4>
          <div class="lib-filter-chips" id="lfLibraryChips" role="listbox" aria-label="Filter by library status"></div>
        </div>
        <div class="lib-filter-card">
          <h4 class="lib-filter-section-title">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><path d="M3 6h18"></path><path d="M7 12h10"></path><path d="M10 18h4"></path></svg>
            Sorting
          </h4>
          <div class="lib-filter-grid">
            <label class="lib-filter-field">
              <span class="lib-filter-label">Sort by</span>
              <select id="lfSort">
                <option value="">Recently updated</option>
                <option value="added">Recently added</option>
                <option value="name">Name A–Z</option>
                <option value="owned_platform">Owned on: A–Z</option>
                <option value="owned_platform_desc">Owned on: Z–A</option>
                <option value="available_platform">Available on: A–Z</option>
                <option value="available_platform_desc">Available on: Z–A</option>
                <option value="release_new">Release: Newest first</option>
                <option value="release_old">Release: Oldest first</option>
                <option value="my_rating">My rating: Highest first</option>
                <option value="my_rating_low">My rating: Lowest first</option>
                <option value="critic_rating">Critic rating: Highest first</option>
                <option value="critic_rating_low">Critic rating: Lowest first</option>
              </select>
            </label>
          </div>
        </div>
        <div class="lib-filter-card">
          <h4 class="lib-filter-section-title">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><path d="M20 12V8H6a2 2 0 0 1-2-2c0-1.1.9-2 2-2h12v4"></path><path d="M20 12v4H6a2 2 0 0 0-2 2c0 1.1.9 2 2 2h12v-4"></path><path d="M12 12h.01"></path></svg>
            Platform &amp; Tags
          </h4>
          <div class="lib-filter-grid lib-filter-grid--2">
            <label class="lib-filter-field">
              <span class="lib-filter-label">Platform (available)</span>
              <div class="lib-filter-input-wrap">
                <svg class="lib-filter-input-icon" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><rect x="2" y="6" width="20" height="12" rx="2"></rect><path d="M6 12h4"></path><path d="M8 10v4"></path><circle cx="15" cy="11" r="1" fill="currentColor" stroke="none"></circle><circle cx="18" cy="13" r="1" fill="currentColor" stroke="none"></circle></svg>
                <input id="lfPlatform" type="text" placeholder="ps5, sw2, xsx, win" autocomplete="off" inputmode="search">
              </div>
              <div id="lfPlatformList" class="lib-filter-autocomplete" hidden></div>
            </label>
            <label class="lib-filter-field">
              <span class="lib-filter-label">Tags</span>
              <div class="lib-filter-input-wrap">
                <svg class="lib-filter-input-icon" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M20 12V8H6a2 2 0 0 1-2-2c0-1.1.9-2 2-2h12v4"></path><path d="M20 12v4H6a2 2 0 0 0-2 2c0 1.1.9 2 2 2h12v-4"></path></svg>
                <input id="lfTags" type="text" placeholder='rpg, "co-op"' autocomplete="off" inputmode="search">
              </div>
              <div id="lfTagsList" class="lib-filter-autocomplete" hidden></div>
            </label>
          </div>
        </div>
        <div class="lib-filter-card">
          <h4 class="lib-filter-section-title">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><circle cx="12" cy="8" r="6"></circle><path d="M15.05 14.05a7 7 0 0 1-6.1 0"></path><path d="M12 14v6"></path><path d="M9 18h6"></path></svg>
            Ownership
          </h4>
          <div class="lib-filter-grid lib-filter-grid--2">
            <label class="lib-filter-field">
              <span class="lib-filter-label">Owned on</span>
              <div class="lib-filter-input-wrap">
                <svg class="lib-filter-input-icon" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><path d="M12 2l7 4v6c0 5-3.5 9-7 10-3.5-1-7-5-7-10V6l7-4z"></path><path d="M9 12l2 2 4-4"></path></svg>
                <input id="lfOwnedPlatform" type="text" placeholder="ps5, sw2, xsx" autocomplete="off" inputmode="search">
              </div>
              <div id="lfOwnedPlatformList" class="lib-filter-autocomplete" hidden></div>
            </label>
            <label class="lib-filter-field">
              <span class="lib-filter-label">Format</span>
              <select id="lfFormat">
                <option value="">Any format</option>
                <option value="digital">Digital</option>
                <option value="physical">Physical</option>
                <option value="both">Both (physical or digital)</option>
                <option value="none">None (not set)</option>
              </select>
            </label>
          </div>
          <p class="lib-filter-hint">Completed + owned on ps5 = only ps5-owned completed (not just available on ps5).</p>
         </div>
        <div class="lib-filter-card">
          <h4 class="lib-filter-section-title">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><rect x="3" y="4" width="18" height="18" rx="2"></rect><line x1="16" y1="2" x2="16" y2="6"></line><line x1="8" y1="2" x2="8" y2="6"></line><line x1="3" y1="10" x2="21" y2="10"></line></svg>
            Release Date
          </h4>
          <div class="lib-filter-grid lib-filter-grid--2">
            <label class="lib-filter-field">
              <span class="lib-filter-label">Year from</span>
              <input id="lfYearFrom" type="number" min="1900" max="2100" inputmode="numeric" placeholder="1994">
            </label>
            <label class="lib-filter-field">
              <span class="lib-filter-label">Year to</span>
              <input id="lfYearTo" type="number" min="1900" max="2100" inputmode="numeric" placeholder="2024">
            </label>
          </div>
        </div>
      </div>
      <div class="lib-filter-footer">
        <button id="lfClear" class="btn btn-secondary lib-filter-clear" type="button">Clear all</button>
        <button id="lfApply" class="btn btn-primary lib-filter-apply" type="button">Apply filters</button>
      </div>
    </div>`;
}

function updateLibFilterBadge() {
  const n = [
    paginationState.platformFilter,
    paginationState.ownedPlatformFilter,
    paginationState.tagFilter,
    paginationState.formatFilter || libraryFilters.format,
    paginationState.sort || libraryFilters.sort,
    libraryFilters.yearFrom,
    libraryFilters.yearTo,
  ].filter(v => String(v || '').trim()).length;
  const text = n > 9 ? '9+' : String(n);
  const hidden = n === 0;
  const label = n ? `${n} filters active` : '';
  // Legacy FAB badge
  const badge = document.getElementById('libFilterBadge');
  if (badge) {
    badge.textContent = text;
    badge.hidden = hidden;
    badge.setAttribute('aria-label', label);
  }
  // Inline search advanced badge
  const badgeInline = document.getElementById('libFilterBadgeInline');
  if (badgeInline) {
    badgeInline.textContent = text;
    badgeInline.hidden = hidden;
    badgeInline.setAttribute('aria-label', label);
  }
  const btn = document.getElementById('libFilterBtn');
  if (btn) {
    const l = n ? `Filter library, ${n} active` : 'Filter library';
    btn.setAttribute('aria-label', l);
    btn.title = l;
  }
  const btnInline = document.getElementById('searchAdvancedBtn');
  if (btnInline) {
    const l = n ? `More filters, ${n} active` : 'More filters';
    btnInline.setAttribute('aria-label', l);
    btnInline.title = l;
  }
}

function syncLibFilterInputs() {
  const panel = document.getElementById('libFilterPanel');
  if (!panel) return;
  const platEl = panel.querySelector('#lfPlatform');
  const ownedEl = panel.querySelector('#lfOwnedPlatform');
  const tagsEl = panel.querySelector('#lfTags');
  const formatEl = panel.querySelector('#lfFormat');
  const sortEl = panel.querySelector('#lfSort');
  const yfEl = panel.querySelector('#lfYearFrom');
  const ytEl = panel.querySelector('#lfYearTo');
  const rfEl = panel.querySelector('#lfReleaseFrom');
  const rtEl = panel.querySelector('#lfReleaseTo');
  if (platEl) platEl.value = paginationState.platformFilter || '';
  if (ownedEl) ownedEl.value = paginationState.ownedPlatformFilter || '';
  if (tagsEl) tagsEl.value = paginationState.tagFilter || '';
  if (formatEl) formatEl.value = paginationState.formatFilter || libraryFilters.format || '';
  if (sortEl) sortEl.value = paginationState.sort || libraryFilters.sort || '';
  if (yfEl) yfEl.value = libraryFilters.yearFrom || paginationState.yearFrom || '';
  if (ytEl) ytEl.value = libraryFilters.yearTo || paginationState.yearTo || '';
  if (rfEl) rfEl.value = libraryFilters.releaseFrom || paginationState.releaseFrom || '';
  if (rtEl) rtEl.value = libraryFilters.releaseTo || paginationState.releaseTo || '';
  // Sync library status chips inside advanced panel (mirrors bottom status FAB)
  const libChipsEl = panel.querySelector('#lfLibraryChips');
  if (libChipsEl && typeof panel._syncLibraryChips === 'function') {
    try { panel._syncLibraryChips(); } catch {}
  } else if (libChipsEl) {
    const active = currentStatuses();
    const hasFilter = active.length > 0;
    libChipsEl.querySelectorAll('.lib-filter-chip').forEach(chip => {
      const s = chip.dataset.status || '';
      const isAll = s === '';
      const isActive = isAll ? !hasFilter : active.includes(s);
      chip.classList.toggle('active', isActive);
      chip.setAttribute('aria-selected', String(isActive));
    });
  }
}

function wireLibFilterPanel(panel) {
  if (!panel || libFilterWired) return;
  const platEl = panel.querySelector('#lfPlatform');
  const ownedEl = panel.querySelector('#lfOwnedPlatform');
  const tagsEl = panel.querySelector('#lfTags');
  const formatEl = panel.querySelector('#lfFormat');
  const sortEl = panel.querySelector('#lfSort');
  const yfEl = panel.querySelector('#lfYearFrom');
  const ytEl = panel.querySelector('#lfYearTo');
  const rfEl = panel.querySelector('#lfReleaseFrom');
  const rtEl = panel.querySelector('#lfReleaseTo');
  if (!platEl) return;
  libFilterWired = true;

  // Custom autocomplete — replaces native datalist which overlays the input
  // and hides what the user is typing. Suggestions appear in a small panel
  // below the field (not over it) and never cover the typed text.
  function setupFilterAutocomplete(inputEl, listEl, fetchFn, rankFn) {
    if (!inputEl || !listEl) return;
    const render = (items) => {
      if (!items || items.length === 0) {
        listEl.hidden = true;
        listEl.innerHTML = '';
        return;
      }
      listEl.innerHTML = items.slice(0, 8).map(v => `<div class="lib-filter-autocomplete-option" data-value="${escapeHTML(v)}">${escapeHTML(v)}</div>`).join('');
      listEl.hidden = false;
      listEl.querySelectorAll('.lib-filter-autocomplete-option').forEach(opt => {
        opt.addEventListener('click', () => {
          const val = opt.dataset.value || '';
          // For tags, replace only the last segment being typed
          if (inputEl === tagsEl) {
            const raw = inputEl.value;
            const last = raw.split(/[\s|]+/).pop().replace(/^"|"$/g, '').trim();
            if (last) {
              const { start } = (() => {
                let segStart = 0; let quoted = false;
                for (let i = 0; i < raw.length; i++) {
                  const ch = raw[i];
                  if (ch === '"') quoted = !quoted;
                  else if (!quoted && (ch === ' ' || ch === '|')) segStart = i + 1;
                }
                return { start: segStart };
              })();
              const before = raw.slice(0, segStart);
              const needsQuote = /[\s|"]/.test(val);
              const formatted = needsQuote ? `"${val.replace(/"/g, '')}"` : val;
              inputEl.value = before + formatted + ' ';
            } else {
              inputEl.value = val;
            }
          } else {
            inputEl.value = val;
          }
          listEl.hidden = true;
          listEl.innerHTML = '';
          inputEl.focus();
        });
      });
    };
    const hide = () => { listEl.hidden = true; };
    let timer = null;
    inputEl.addEventListener('input', () => {
      clearTimeout(timer);
      const raw = inputEl.value;
      const q = inputEl === tagsEl ? raw.split(/[\s|]+/).pop().replace(/^"|"$/g, '').trim() : raw.trim();
      if (q.length < 1) { hide(); return; }
      timer = setTimeout(async () => {
        try {
          const list = await fetchFn(q);
          const ranked = rankFn ? rankFn(list, q) : list;
          render(ranked);
        } catch { hide(); }
      }, 200);
    });
    inputEl.addEventListener('focus', () => {
      const raw = inputEl.value;
      const q = inputEl === tagsEl ? raw.split(/[\s|]+/).pop().replace(/^"|"$/g, '').trim() : raw.trim();
      if (q.length >= 1 && listEl.children.length > 0) listEl.hidden = false;
    });
    inputEl.addEventListener('blur', () => {
      // Delay hide to allow click on option to register
      setTimeout(() => hide(), 150);
    });
    // Also hide when panel scrolls or closes
    panel.addEventListener('scroll', hide, true);
  }

  setupFilterAutocomplete(platEl, panel.querySelector('#lfPlatformList'), autocompleteGlobalPlatforms, rankPlatformSuggestions);
  setupFilterAutocomplete(ownedEl, panel.querySelector('#lfOwnedPlatformList'), autocompleteGlobalPlatforms, rankPlatformSuggestions);
  setupFilterAutocomplete(tagsEl, panel.querySelector('#lfTagsList'), async (q) => autocompleteTags(q, 20), null);

  // Library status chips inside advanced filter (mirrors bottom status FAB, library-only)
  const libChipsEl = panel.querySelector('#lfLibraryChips');
  if (libChipsEl) {
    const chips = [
      { status: '', label: 'All' },
      ...VALID_STATUSES.map(s => ({ status: s, label: STATUS_LABELS[s] || s })),
    ].map(({ status, label }) => {
      const color = STATUS_CHIP_COLORS[status];
      const style = color ? ` style="--chip-color:${color}"` : '';
      return `<button type="button" class="lib-filter-chip${status === '' ? ' lib-filter-all' : ''}" data-status="${escapeHTML(status)}"${style} role="option" aria-selected="false">${escapeHTML(label)}</button>`;
    }).join('');
    libChipsEl.innerHTML = chips;
    const syncLibraryChips = () => {
      const active = currentStatuses();
      const hasFilter = active.length > 0;
      libChipsEl.querySelectorAll('.lib-filter-chip').forEach(chip => {
        const s = chip.dataset.status || '';
        const isAll = s === '';
        const isActive = isAll ? !hasFilter : active.includes(s);
        chip.classList.toggle('active', isActive);
        chip.setAttribute('aria-selected', String(isActive));
      });
    };
    panel._syncLibraryChips = syncLibraryChips;
    syncLibraryChips();
    libChipsEl.addEventListener('click', (e) => {
      const chip = e.target.closest('.lib-filter-chip');
      if (!chip) return;
      const status = chip.dataset.status || '';
      const cur = currentStatuses();
      let next;
      if (status === '') next = [];
      else {
        if (cur.includes(status)) next = cur.filter(s => s !== status);
        else next = [...cur, status];
      }
      setStatuses(next);
      if (window.location.hash && VALID_STATUSES.includes(window.location.hash.slice(1))) {
        history.replaceState(null, '', window.location.pathname + window.location.search);
      }
      syncLibraryChips();
      // Keep bottom status FAB in sync
      try { if (typeof syncStatusFilterPanel === 'function') syncStatusFilterPanel(); } catch {}
      // Stage the status change like the other fields — don't reload yet.
      // The advanced panel is transactional (Apply/Clear), so keep the new
      // statuses in paginationState without wiping the other unsaved inputs
      // (platform/tags/sort/year) that would happen if we called loadLibrary
      // here with the stale paginationState.tagFilter etc.
      try { if (typeof updateLibFilterBadge === 'function') updateLibFilterBadge(); } catch {}
    });
  }

  const dismissAutocomplete = () => {
    // Datalist dropdowns are native and stay open while the input retains
    // focus. If the user types "ps" and hits Apply without picking a chip,
    // the dropdown would otherwise linger over the dimmed backdrop or next
    // paint. Blur everything in the panel to force the browser to close it.
    try { if (document.activeElement && panel.contains(document.activeElement)) document.activeElement.blur(); } catch {}
    [platEl, ownedEl, tagsEl, formatEl, yfEl, ytEl, rfEl, rtEl, sortEl].forEach(el => { try { el && el.blur(); } catch {} });
  };

  const apply = () => {
    const clampYear = v => {
      const n = parseInt(v, 10);
      return Number.isFinite(n) && n >= 1900 && n <= 2100 ? String(n) : '';
    };
    const newTag = tagsEl.value.trim().slice(0, 200);
    const newPlat = platEl.value.trim().slice(0, 64);
    const newOwned = ownedEl ? ownedEl.value.trim().slice(0, 64) : '';
    const newFormat = formatEl ? formatEl.value : '';
    const newSort = sortEl ? sortEl.value : '';
    dismissAutocomplete();
    libraryFilters.yearFrom = clampYear(yfEl ? yfEl.value : '');
    libraryFilters.yearTo = clampYear(ytEl ? ytEl.value : '');
    libraryFilters.sort = newSort;
    // Exact dates removed — clear any legacy exact range
    libraryFilters.releaseFrom = '';
    libraryFilters.releaseTo = '';
    paginationState.yearFrom = libraryFilters.yearFrom;
    paginationState.yearTo = libraryFilters.yearTo;
    libraryFilters.format = newFormat;
    paginationState.formatFilter = newFormat;
    paginationState.sort = newSort;
    paginationState.releaseFrom = '';
    paginationState.releaseTo = '';
    closeLibFilterPanel();
    loadLibrary(paginationState.statuses, newTag, newPlat, newOwned);
  };

  const clear = () => {
    dismissAutocomplete();
    platEl.value = ''; tagsEl.value = '';
    if (ownedEl) ownedEl.value = '';
    if (formatEl) formatEl.value = '';
    if (sortEl) sortEl.value = '';
    if (yfEl) yfEl.value = ''; if (ytEl) ytEl.value = '';
    if (rfEl) rfEl.value = ''; if (rtEl) rtEl.value = '';
    libraryFilters.yearFrom = ''; libraryFilters.yearTo = ''; libraryFilters.releaseFrom = ''; libraryFilters.releaseTo = ''; libraryFilters.format = ''; libraryFilters.sort = '';
    paginationState.yearFrom = ''; paginationState.yearTo = ''; paginationState.releaseFrom = ''; paginationState.releaseTo = ''; paginationState.formatFilter = ''; paginationState.sort = '';
    // Clear Library (status) chips as well — Clear should reset everything
    try { setStatuses([]); } catch {}
    try { if (typeof syncStatusFilterPanel === 'function') syncStatusFilterPanel(); } catch {}
    try { const chipsEl = panel.querySelector('#lfLibraryChips'); if (chipsEl && typeof panel._syncLibraryChips === 'function') panel._syncLibraryChips(); } catch {}
    history.replaceState(null, '', window.location.pathname + window.location.search);
    closeLibFilterPanel();
    loadLibrary([], '', '', '', {sort: '', yearFrom: '', yearTo: '', releaseFrom: '', releaseTo: ''});
  };

  // Expose apply for backdrop/X that live outside this closure (openLibFilterPanel)
  panel._applyStagedFilters = apply;
  panel.querySelector('#lfApply')?.addEventListener('click', apply);
  panel.querySelector('#lfClear')?.addEventListener('click', clear);
  // X and backdrop should also apply — users often hit X expecting the staged
  // values (e.g. backlog + oldest) to take effect, and "no obvious Apply"
  // means they miss the footer. Make close-via-X/backdrop transactional.
  panel.querySelector('#libFilterClose')?.addEventListener('click', () => {
    // If there are staged changes, apply them so X doesn't silently discard
    // the user's sort/platform/tags/year selection.
    try { apply(); } catch { closeLibFilterPanel(); }
  });
  [platEl, ownedEl, tagsEl, formatEl, sortEl, yfEl, ytEl, rfEl, rtEl].filter(Boolean).forEach(el => {
    el.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); apply(); } });
  });
  // Sorting is staged like the other fields — don't auto-apply on change.
  // The panel stays open until the user hits Apply/Clear/Close or the backdrop,
  // and Apply shows a toast so it's obvious the filters took effect.
}

function openLibFilterPanel() {
  const fab = document.getElementById('libFilterFab');
  const panel = document.getElementById('libFilterPanel');
  const btn = document.getElementById('libFilterBtn');
  const inlineBtn = document.getElementById('searchAdvancedBtn');
  const backdrop = document.getElementById('libFilterBackdrop');
  if (!panel) return;
  if (!fab || !btn) {
    if (!inlineBtn) return;
  }
  if (statusFilterOpen) closeStatusFilterPanel();
  if (!panel.innerHTML.trim()) {
    panel.innerHTML = buildLibFilterPanelHTML();
    wireLibFilterPanel(panel);
  }
  panel.hidden = false;
  syncLibFilterInputs();
  if (backdrop) {
    backdrop.hidden = false;
    backdrop.style.display = 'block';
    if (!backdrop.dataset.wired) {
      backdrop.dataset.wired = '1';
      backdrop.addEventListener('click', () => {
        const p = document.getElementById('libFilterPanel');
        try {
          if (p && typeof p._applyStagedFilters === 'function') p._applyStagedFilters();
          else closeLibFilterPanel();
        } catch { closeLibFilterPanel(); }
      });
    }
  }
  document.body.classList.add('lib-advanced-open');
  lockFilterBodyScroll();
  // Contain wheel inside the scrollable body so it doesn't bubble to .app-shell.
  const bodyEl = panel.querySelector('.lib-filter-body');
  if (bodyEl && !bodyEl.dataset.wheelWired) {
    bodyEl.dataset.wheelWired = '1';
    bodyEl.addEventListener('wheel', (e) => {
      const el = e.currentTarget;
      const delta = e.deltaY;
      const atTop = el.scrollTop <= 0;
      const atBottom = el.scrollTop + el.clientHeight >= el.scrollHeight - 1;
      const goingUp = delta < 0;
      const goingDown = delta > 0;
      const canScrollUp = !atTop && goingUp;
      const canScrollDown = !atBottom && goingDown;
      if (canScrollUp || canScrollDown) {
        e.stopPropagation();
      } else if (atTop && atBottom) {
        e.preventDefault();
      } else {
        e.preventDefault();
        e.stopPropagation();
      }
    }, { passive: false });
    // Touch: prevent background pan when dragging inside panel header/footer chrome
    panel.addEventListener('touchmove', (e) => {
      const t = e.target;
      // If touch started inside the scrollable body, allow it (CSS touch-action handles)
      if (t.closest && t.closest('.lib-filter-body')) return;
      // Header/footer or chrome — don't let it drag the page behind
      if (e.cancelable) e.preventDefault();
    }, { passive: false });
  }
  // Prevent wheel over non-scrollable chrome (header/footer) from leaking
  if (!panel.dataset.wheelWired) {
    panel.dataset.wheelWired = '1';
    panel.addEventListener('wheel', (e) => {
      if (e.target.closest && e.target.closest('.lib-filter-body')) return;
      if (e.cancelable) e.preventDefault();
    }, { passive: false });
  }
  // allow render before transition
  requestAnimationFrame(() => {
    panel.classList.add('lib-filter-panel--open');
    if (backdrop) backdrop.classList.add('lib-filter-backdrop--open');
  });
  if (btn) { btn.classList.add('lib-filter-btn--open'); btn.setAttribute('aria-expanded', 'true'); }
  if (inlineBtn) { inlineBtn.classList.add('lib-filter-btn--open'); inlineBtn.setAttribute('aria-expanded', 'true'); }
  libFilterOpen = true;
}

function closeLibFilterPanel() {
  const panel = document.getElementById('libFilterPanel');
  const btn = document.getElementById('libFilterBtn');
  const inlineBtn = document.getElementById('searchAdvancedBtn');
  const backdrop = document.getElementById('libFilterBackdrop');
  if (!panel) return;
  // Dismiss any open datalist dropdowns (native) that would otherwise linger
  // when the user hits Apply without picking a suggestion (e.g. typing "ps"
  // and applying "ps" as free text).
  try {
    if (document.activeElement && panel.contains(document.activeElement)) document.activeElement.blur();
    panel.querySelectorAll('input').forEach(el => { try { el.blur(); } catch {} });
  } catch {}
  panel.classList.remove('lib-filter-panel--open');
  if (backdrop) backdrop.classList.remove('lib-filter-backdrop--open');
  if (btn) { btn.classList.remove('lib-filter-btn--open'); btn.setAttribute('aria-expanded', 'false'); }
  if (inlineBtn) { inlineBtn.classList.remove('lib-filter-btn--open'); inlineBtn.setAttribute('aria-expanded', 'false'); }
  libFilterOpen = false;
  document.body.classList.remove('lib-advanced-open');
  unlockFilterBodyScroll();
  // delay hidden for transition (180ms)
  setTimeout(() => {
    if (!libFilterOpen) {
      panel.hidden = true;
      if (backdrop) {
        backdrop.hidden = true;
        backdrop.style.display = '';
      }
    }
  }, 180);
}

function toggleLibFilterPanel() {
  if (libFilterOpen) closeLibFilterPanel();
  else openLibFilterPanel();
}

function ensureLibFilterFab() {
  const fab = document.getElementById('libFilterFab');
  const panel = document.getElementById('libFilterPanel');
  const btn = document.getElementById('libFilterBtn');
  const inlineBtn = document.getElementById('searchAdvancedBtn');
  if (!panel) return null;
  if (paginationState.mode !== 'library') {
    if (fab) fab.hidden = true;
    if (inlineBtn) inlineBtn.hidden = true;
    if (libFilterOpen) closeLibFilterPanel();
    return null;
  }
  const libraryView = document.getElementById('libraryView');
  if (libraryView && libraryView.hidden) {
    if (fab) fab.hidden = true;
    if (inlineBtn) inlineBtn.hidden = true;
    if (libFilterOpen) closeLibFilterPanel();
    return null;
  }
  if (fab) fab.hidden = true; // hidden now, filters are inline
  if (inlineBtn) inlineBtn.hidden = false;
  if (!panel.innerHTML.trim()) {
    panel.innerHTML = buildLibFilterPanelHTML();
    wireLibFilterPanel(panel);
  }
  syncLibFilterInputs();
  updateLibFilterBadge();
  // Wire legacy FAB button (hidden, for backward compat)
  if (fab && btn && !fab.dataset.wired) {
    fab.dataset.wired = '1';
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      toggleLibFilterPanel();
    });
  }
  // Wire inline advanced filter button inside search box
  if (inlineBtn && !inlineBtn.dataset.wired) {
    inlineBtn.dataset.wired = '1';
    inlineBtn.addEventListener('click', (e) => {
      e.stopPropagation();
      toggleLibFilterPanel();
    });
  }
  // Global outside-click and Escape handlers (once)
  if (!ensureLibFilterFab._wiredDoc) {
    ensureLibFilterFab._wiredDoc = true;
    document.addEventListener('click', (e) => {
      if (!libFilterOpen) return;
      if (panel.contains(e.target)) return;
      if (e.target.closest('#searchAdvancedBtn')) return;
      if (fab && fab.contains(e.target)) return;
      // Don't close when clicking the tag filter bar or modal
      if (e.target.closest('#tagFilterBar')) return;
      if (e.target.closest('#statusFilterPanel') || e.target.closest('#searchStatusBtn') || e.target.closest('#statusFilterFab')) return;
      closeLibFilterPanel();
    });
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && libFilterOpen) {
        e.stopPropagation();
        closeLibFilterPanel();
      }
    });
  }
  return fab || inlineBtn;
}

// Back-compat aliases — old calls route to the FABs
function buildLibraryFilterBarHTML() { return buildLibFilterPanelHTML(); }
function wireLibraryFilterBar(bar) { return wireLibFilterPanel(bar); }
function ensureLibraryFilterBar() {
  ensureLibFilterFab();
  return ensureStatusFilterFab();
}

export async function loadLibrary(status, tag, platform, ownedPlatform, extraOpts = null) {
  const grid = document.getElementById('gameGrid');
  if (!grid) return;

  // Invalidate any search request that is still resolving so it cannot put
  // stale query totals back into the library view.
  searchLoadVersion++;
  const loadVersion = ++libraryLoadVersion;

  // Handle legacy 4-arg calls where 4th arg is actually extraOpts object (platform+owned shift)
  if (ownedPlatform !== undefined && typeof ownedPlatform === 'object' && ownedPlatform !== null && !Array.isArray(ownedPlatform)) {
    extraOpts = ownedPlatform;
    ownedPlatform = undefined;
  }
  // Preserve existing filters when not explicitly passed (e.g., hash navigation
  // or tag-only updates). Explicit '' clears the filter; undefined preserves it
  // so that platform + status remain intersected (ps5 + completed = completed
  // ps5 games in library, not all ps5-available). Owned platform is separate.
  if (status === undefined) status = paginationState.statuses;
  if (tag === undefined) tag = paginationState.tagFilter;
  if (platform === undefined) platform = paginationState.platformFilter;
  if (ownedPlatform === undefined) ownedPlatform = paginationState.ownedPlatformFilter;
  // Normalize status input to array (supports single string, comma string, or array for multi-select)
  const normStatuses = normalizeStatuses(status);
  // Merge extraOpts (release/format/sort filters) into libraryFilters if provided,
  // otherwise keep current libraryFilters (persisted across pagination).
  if (extraOpts && typeof extraOpts === 'object') {
    if ('yearFrom' in extraOpts) libraryFilters.yearFrom = extraOpts.yearFrom || '';
    if ('yearTo' in extraOpts) libraryFilters.yearTo = extraOpts.yearTo || '';
    if ('releaseFrom' in extraOpts) libraryFilters.releaseFrom = extraOpts.releaseFrom || '';
    if ('releaseTo' in extraOpts) libraryFilters.releaseTo = extraOpts.releaseTo || '';
    if ('format' in extraOpts) libraryFilters.format = extraOpts.format || '';
    if ('sort' in extraOpts) libraryFilters.sort = extraOpts.sort || '';
  }
  // If format not provided via extraOpts but paginationState has it, keep it
  const effectiveFormat = (extraOpts && 'format' in extraOpts) ? libraryFilters.format : (paginationState.formatFilter || libraryFilters.format || '');
  // If sort not provided via extraOpts but paginationState has it, keep it
  const effectiveSort = (extraOpts && 'sort' in extraOpts) ? libraryFilters.sort : (paginationState.sort || libraryFilters.sort || '');

  // Reset pagination state
  paginationState = {
    currentStatus: normStatuses[0] || '',
    statuses: normStatuses,
    tagFilter: tag || '',
    platformFilter: platform || '',
    ownedPlatformFilter: ownedPlatform || '',
    formatFilter: effectiveFormat || '',
    sort: effectiveSort || '',
    offset: 0,
    // Held true across the fetch below so a scroll event landing in this
    // window can't trigger a concurrent loadMore() that appends the same
    // page twice (duplicated cards). renderPagedItems clears it once done.
    loading: true,
    hasMore: true,
    pageSize: PAGE_SIZE,
    mode: 'library',
    searchQuery: '',
    yearFrom: libraryFilters.yearFrom,
    yearTo: libraryFilters.yearTo,
    releaseFrom: libraryFilters.releaseFrom,
    releaseTo: libraryFilters.releaseTo,
  };
  const filteredView = hasActiveLibraryFilters();

  // Hide search results header and show FABs
  const searchHeader = document.getElementById('searchResultsHeader');
  if (searchHeader) searchHeader.remove();
  // Legacy single-status tab sync (first of multi)
  activateStatusTab(normStatuses[0] || '');
  // Sync status FAB (multi-select)
  syncStatusFilterPanel();
  updateTagFilterBar();
  ensureLibFilterFab();
  ensureStatusFilterFab();

  const statsStrip = document.getElementById('statsStrip');
  if (statsStrip) statsStrip.style.display = 'none';
  grid.innerHTML = '<div class="loading">Loading library...</div>';

  try {
    const { items, total, hasMore } = await library.list(normStatuses, PAGE_SIZE, 0, tag || '', platform || '', {
      yearFrom: libraryFilters.yearFrom || null,
      yearTo: libraryFilters.yearTo || null,
      releaseFrom: libraryFilters.releaseFrom || null,
      releaseTo: libraryFilters.releaseTo || null,
      ownedPlatform: ownedPlatform || null,
      format: paginationState.formatFilter || null,
      sort: paginationState.sort || null,
    });
    if (loadVersion !== libraryLoadVersion || paginationState.mode !== 'library') return;
    if (filteredView) updateLibraryFilterTotal(total);
    renderPagedItems(grid, items, true, hasMore);
    refreshTabCounts();
  } catch (err) {
    if (loadVersion !== libraryLoadVersion || paginationState.mode !== 'library') return;
    paginationState.loading = false;
    grid.innerHTML = `<div class="empty-state">Failed to load library: ${err.message}</div>`;
  }
}

// renderPagedItems renders library items into the grid, either replacing or appending.
// isFirstPage=true means clear and replace; false means append.
// hasMore (optional) overrides the "came up one short" heuristic using the
// server's exact X-Has-More header.
function renderPagedItems(grid, items, isFirstPage = true, hasMore = null) {
  removeLoadMoreRetry();
  if (!items || items.length === 0) {
    if (isFirstPage) {
      renderEmptyState(grid, paginationState.mode);
      paginationState.hasMore = false;
      paginationState.loading = false;
    }
    return;
  }

  if (isFirstPage) {
    grid.innerHTML = '';
  }

  let displayItems = items;
  if (paginationState.mode === 'search') {
    // Adapt search results to card format and index them
    displayItems = items.map(r => ({
      game_id: r.id,
      game_name: r.name,
      cover_url: r.cover_url,
      local_cover_path: r.local_cover_path,
      platforms: r.platforms || [],
      rating: 0,
      first_release_date: r.first_release_date || 0,
    }));
    indexSearchResults(items, isFirstPage);
  } else {
    indexItems(items, isFirstPage);
  }

  // Render and append cards
  const html = buildCardHTML(displayItems);
  grid.insertAdjacentHTML('beforeend', html);
  attachCardEvents(grid, displayItems, paginationState.mode === 'search' ? items : null);

  // Update pagination state
  paginationState.offset += items.length;
  paginationState.hasMore = hasMore === null
    ? items.length === paginationState.pageSize
    : hasMore;
  paginationState.loading = false;

  // Attach scroll listener on first page
  if (isFirstPage) {
    attachScrollListener();
  }
}

// renderLibraryItems is for the initial server-rendered page load.
// It sets up pagination state and attaches scroll listener.
export function renderLibraryItems(items, status = '') {
  const grid = document.getElementById('gameGrid');
  if (!grid) return;

  const norm = normalizeStatuses(status);
  paginationState = {
    currentStatus: norm[0] || '',
    statuses: norm,
    tagFilter: '',
    platformFilter: '',
    ownedPlatformFilter: '',
    formatFilter: libraryFilters.format,
    offset: 0,
    loading: false,
    hasMore: true,
    pageSize: PAGE_SIZE,
    mode: 'library',
    searchQuery: '',
    yearFrom: libraryFilters.yearFrom,
    yearTo: libraryFilters.yearTo,
    releaseFrom: libraryFilters.releaseFrom,
    releaseTo: libraryFilters.releaseTo,
  };
  ensureLibraryFilterBar();
  ensureStatusFilterFab();

  if (!items || items.length === 0) {
    renderEmptyState(grid, 'library', status);
    paginationState.hasMore = false;
    return;
  }

  grid.innerHTML = '';
  indexItems(items, true);
  const html = buildCardHTML(items);
  grid.insertAdjacentHTML('beforeend', html);
  attachCardEvents(grid, items);

  // Update pagination state as if this was the first page
  paginationState.offset = items.length;
  paginationState.hasMore = items.length === PAGE_SIZE;

  attachScrollListener();
}

async function loadMore() {
  if (paginationState.loading || !paginationState.hasMore) return;

  paginationState.loading = true;
  const searchVersion = paginationState.mode === 'search' ? searchLoadVersion : null;
  const libraryVersion = paginationState.mode === 'library' ? libraryLoadVersion : null;

  const grid = document.getElementById('gameGrid');
  if (!grid) return;

  try {
    if (paginationState.mode === 'search') {
      const { results, total } = await searchGamesFull(paginationState.searchQuery, {
        limit: paginationState.pageSize,
        offset: paginationState.offset,
        ...filterParams(),
      });
      if (searchVersion !== searchLoadVersion || paginationState.mode !== 'search') return;
      updateSearchTotal(total);
      renderPagedItems(grid, results, false); // false = append, not replace
    } else {
      const { items, hasMore } = await library.list(
        paginationState.statuses || [],
        paginationState.pageSize,
        paginationState.offset,
        paginationState.tagFilter,
        paginationState.platformFilter,
        {
          yearFrom: paginationState.yearFrom || libraryFilters.yearFrom || null,
          yearTo: paginationState.yearTo || libraryFilters.yearTo || null,
          releaseFrom: paginationState.releaseFrom || libraryFilters.releaseFrom || null,
          releaseTo: paginationState.releaseTo || libraryFilters.releaseTo || null,
          ownedPlatform: paginationState.ownedPlatformFilter || null,
          format: paginationState.formatFilter || libraryFilters.format || null,
          sort: paginationState.sort || null,
        }
      );
      if (libraryVersion !== libraryLoadVersion || paginationState.mode !== 'library') return;
      renderPagedItems(grid, items, false, hasMore);
    }
  } catch (err) {
    if (searchVersion !== null && (searchVersion !== searchLoadVersion || paginationState.mode !== 'search')) return;
    if (libraryVersion !== null && (libraryVersion !== libraryLoadVersion || paginationState.mode !== 'library')) return;
    paginationState.loading = false;
    // Scrolling appeared broken with no feedback; surface a visible retry.
    showLoadMoreRetry();
  }
}

// showLoadMoreRetry appends a retry button after a failed page load. The
// next successful render removes it.
function showLoadMoreRetry() {
  const grid = document.getElementById('gameGrid');
  if (!grid) return;
  let btn = document.getElementById('loadMoreRetry');
  if (!btn) {
    btn = document.createElement('button');
    btn.id = 'loadMoreRetry';
    btn.type = 'button';
    btn.className = 'load-more-retry';
    btn.addEventListener('click', () => loadMore());
    grid.insertAdjacentElement('afterend', btn);
  }
  btn.textContent = 'Failed to load more — click to retry';
}

function removeLoadMoreRetry() {
  const btn = document.getElementById('loadMoreRetry');
  if (btn) btn.remove();
}

// refreshTabCounts fetches per-status counts and renders them into the tab
// labels ("Backlog (43)"), plus the lifetime stats strip in library mode. With
// the FAB, the same counts are mirrored onto the floating panel's chips so the
// user sees tallies without opening the old inline row.
export async function refreshTabCounts() {
  const statusTabs = document.getElementById('statusTabs');
  const counts = await library.counts();
  if (!counts) return;
  if (statusTabs) {
    statusTabs.querySelectorAll('.tab').forEach(tab => {
      const status = tab.dataset.status || '';
      const n = counts[status || 'all'];
      const label = tab.dataset.label || (tab.dataset.label = tab.textContent.replace(/\s\(\d+\)$/, ''));
      tab.textContent = typeof n === 'number' ? `${label} (${n})` : label;
    });
  }
  // Mirror counts onto status FAB chips (create panel if needed so labels exist)
  const statusPanel = document.getElementById('statusFilterPanel');
  if (statusPanel && !statusPanel.innerHTML.trim()) {
    statusPanel.innerHTML = buildStatusFilterPanelHTML();
    wireStatusFilterPanel(statusPanel);
  }
  if (statusPanel) {
    statusPanel.querySelectorAll('.lib-filter-chip[data-status]').forEach(chip => {
      const status = chip.dataset.status || '';
      const n = counts[status || 'all'];
      const base = chip.dataset.label || (chip.dataset.label = chip.textContent.replace(/\s\(\d+\)$/, ''));
      chip.textContent = typeof n === 'number' ? `${base} (${n})` : base;
    });
  }
  if (paginationState.mode !== 'search' && !hasActiveLibraryFilters()) updateStatsStrip(counts);
}

// hasActiveLibraryFilters identifies filters that change membership. Sorting
// is intentionally excluded because it changes order, not the total count.
function hasActiveLibraryFilters() {
  const hasValue = value => String(value || '').trim() !== '';
  return currentStatuses().length > 0 ||
    hasValue(paginationState.tagFilter) ||
    hasValue(paginationState.platformFilter) ||
    hasValue(paginationState.ownedPlatformFilter) ||
    hasValue(paginationState.formatFilter || libraryFilters.format) ||
    hasValue(paginationState.yearFrom || libraryFilters.yearFrom) ||
    hasValue(paginationState.yearTo || libraryFilters.yearTo) ||
    hasValue(paginationState.releaseFrom || libraryFilters.releaseFrom) ||
    hasValue(paginationState.releaseTo || libraryFilters.releaseTo);
}

// updateLibraryFilterTotal renders an exact count for a filtered library view.
// Lifetime completion/playtime stats are intentionally omitted because they
// would describe the unfiltered library and be misleading here.
function updateLibraryFilterTotal(total) {
  const el = document.getElementById('statsStrip');
  if (!el) return;
  el.dataset.context = 'filtered-library';
  el.removeAttribute('role');
  el.removeAttribute('tabindex');
  el.removeAttribute('title');
  if (total == null) {
    el.style.display = 'none';
    return;
  }
  const count = Number(total);
  if (!Number.isFinite(count) || count < 0) {
    el.style.display = 'none';
    return;
  }
  el.textContent = `${count} ${count === 1 ? 'game' : 'games'} found`;
  el.style.display = '';
}

// updateStatsStrip renders the one-line summary ("12 games · 4 finished ·
// ~130h logged") under the tabs. Hidden entirely on an empty library.
function updateStatsStrip(counts) {
  const el = document.getElementById('statsStrip');
  if (!el) return;
  el.dataset.context = 'library';
  el.setAttribute('role', 'button');
  el.setAttribute('tabindex', '0');
  el.title = 'View detailed stats';
  if (!counts.all || counts.total_minutes == null || counts.total_minutes < 0) {
    el.style.display = 'none';
    return;
  }
  const bits = [`${counts.all} ${counts.all === 1 ? 'game' : 'games'}`];
  if ((counts.completed_count || 0) > 0) bits.push(`${counts.completed_count} finished`);
  const hours = Math.round((counts.total_minutes || 0) / 60);
  if (hours > 0) bits.push(`~${hours}h logged`);
  el.textContent = bits.join(' · ');
  el.style.display = '';
}

// renderEmptyState fills the grid with a friendly, contextual empty state.
// Library modes get a CTA that focuses the search box; a fully empty
// collection additionally gets popular-game suggestions with one-click add.
function renderEmptyState(grid, mode, status = '') {
  let title = 'Nothing here yet';
  let sub = 'Your library is empty — every collection starts somewhere.';
  if (paginationState.tagFilter) {
    title = 'No games match these tags';
    sub = 'Try removing a tag from the filter above.';
  } else if (mode === 'search') {
    title = 'No games found';
    sub = 'Check the spelling or try a shorter query.';
  } else if (status === 'wishlist') {
    sub = 'Search for a game above and wishlist it for later.';
  } else if (status === 'playing') {
    sub = 'Pick something from your backlog and hit play!';
  } else if (status === 'completed') {
    sub = 'Finish a game and it will show up here.';
  }
  grid.innerHTML = `
    <div class="empty-state">
      <div class="empty-title">${title}</div>
      <p>${sub}</p>
      ${mode !== 'search' && !paginationState.tagFilter
        ? '<button type="button" class="btn btn-primary btn-inline" id="emptySearchCta">Find a game</button>'
        : ''}
      <div class="suggest-wrap" id="suggestWrap" style="display:none">
        <div class="suggest-title">Popular right now — tap to add:</div>
        <div class="suggest-grid" id="suggestGrid"></div>
      </div>
    </div>`;
  document.getElementById('emptySearchCta')
    ?.addEventListener('click', () => document.getElementById('searchInput')?.focus());

  // Suggestions only make sense on an unfiltered library view. The request
  // resolves after the empty state is visible; a miss just leaves the wrap
  // hidden.
  if (mode === 'library' && !paginationState.tagFilter) loadSuggestions();
}

async function loadSuggestions() {
  const items = await library.suggestions(8);
  const wrap = document.getElementById('suggestWrap');
  const grid = document.getElementById('suggestGrid');
  if (!wrap || !grid || !items || items.length === 0) return;
  // The user may have added something while we were fetching.
  if (!document.getElementById('suggestWrap')) return;

  wrap.style.display = '';
  grid.innerHTML = items.map(g => `
    <button type="button" class="suggest-card" data-suggest-id="${g.id}" data-suggest-name="${escapeHTML(g.name)}">
      <img src="${getCoverURL(g)}" alt="${escapeHTML(g.name)}" loading="lazy" decoding="async" onerror="this.onerror=null;this.src='/covers/${g.id}.jpg'">
      <span class="suggest-name">${escapeHTML(g.name)}</span>
    </button>`).join('');

  grid.querySelectorAll('.suggest-card').forEach(card => {
    card.addEventListener('click', async () => {
      const id = Number(card.dataset.suggestId);
      const name = card.dataset.suggestName;
      card.disabled = true;
      try {
        await library.add(id, { status: 'backlog' });
        showToast(`Added ${name} to Backlog`);
        // Offer to set platform/format immediately; backlog stays the status
        // even if the sheet is closed without edits.
        try {
          const item = await library.get(id);
          // Refresh the grid/counts so the new backlog entry is visible
          // even if the sheet is dismissed without further changes (the
          // sheet's close would otherwise only refresh after an edit).
          loadLibrary(currentStatuses(), paginationState.tagFilter);
          openLibraryItemModal(item);
        } catch {
          // Fallback: if fetching the new item fails, at least reload the view
          await loadLibrary(currentStatuses(), paginationState.tagFilter);
        }
      } catch (err) {
        card.disabled = false;
        showToast(`Couldn't add ${name}: ${err.message}`, { type: 'error' });
      }
    });
  });
}

// showToast displays a transient confirmation message. opts:
//   type   'error' renders in the danger color
//   action { label, fn } renders an inline button (e.g. Undo) — the toast
//          stays until the action is clicked or the timeout elapses.
export function showToast(message, opts = {}) {
  let toast = document.getElementById('toast');
  if (!toast) {
    toast = document.createElement('div');
    toast.id = 'toast';
    toast.setAttribute('role', 'status');
    document.body.appendChild(toast);
  }
  clearTimeout(showToast._timer);
  toast.textContent = message;
  toast.classList.toggle('error', opts.type === 'error');

  const existingAction = toast.querySelector('.toast-action');
  if (existingAction) existingAction.remove();
  if (opts.action) {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'toast-action';
    btn.textContent = opts.action.label;
    btn.addEventListener('click', () => {
      clearTimeout(showToast._timer);
      toast.classList.remove('visible');
      opts.action.fn();
    });
    toast.appendChild(btn);
  }

  toast.classList.add('visible');
  showToast._timer = setTimeout(() => toast.classList.remove('visible'), opts.action ? 8000 : 2500);
}

function attachScrollListener() {
  if (scrollListenerAttached) return;
  scrollListenerAttached = true;

  window.addEventListener('scroll', () => {
    // Load more when user scrolls within 600px of the bottom
    const scrollPos = window.scrollY + window.innerHeight;
    const scrollThreshold = document.documentElement.scrollHeight - 600;
    if (scrollPos >= scrollThreshold) {
      loadMore();
    }
  });

  // Delegated click handler for tag chips on cards. Delegation survives the
  // cloneNode rebinding in attachCardEvents.
  const grid = document.getElementById('gameGrid');
  if (grid) {
    grid.addEventListener('click', (e) => {
      const chip = e.target.closest('.tag-chip');
      if (chip && chip.dataset.tag) {
        e.stopPropagation();
        filterByTag(chip.dataset.tag);
      }
    });
  }
}

function htmlToEl(html) {
  const tpl = document.createElement('template');
  tpl.innerHTML = html.trim();
  return tpl.content.firstChild;
}

function removeCardAnimated(card) {
  if (!card) return;
  card.classList.add('card-removing');
  setTimeout(() => card.remove(), 260);
}

function fmtHours(minutes) {
  const h = Math.round((minutes / 60) * 10) / 10;
  return h === 1 ? '1h' : `${h}h`;
}

function fmtDelta(minutes) {
  return minutes % 60 === 0 ? `${minutes / 60}h` : `${minutes}m`;
}

// filterByTag sets the tag filter and reloads the library.
// Called when a tag chip on a card is clicked.
// If a filter is already active, adds the tag with AND (space-separated).
export function filterByTag(tag) {
  const current = paginationState.tagFilter;
  const newFilter = current ? current + ' ' + formatTagForQuery(tag) : formatTagForQuery(tag);
  loadLibrary(currentStatuses(), newFilter);
}

// applyTagFilter replaces the active tag filter outright and reloads.
// Used by the game-form tag menu, where the exact combination (replace,
// AND, OR) is chosen explicitly rather than always appended.
export function applyTagFilter(filter) {
  loadLibrary(currentStatuses(), filter);
}

// clearTagFilter removes the tag filter and reloads (platform filter kept).
export function clearTagFilter() {
  loadLibrary(currentStatuses(), '', paginationState.platformFilter);
}

// clearAllFilters removes both tag and platform filters and reloads.
export function clearAllFilters() {
  loadLibrary(currentStatuses(), '', '');
}

// filterByPlatform sets the platform availability filter and reloads.
// Typing the same platform again clears it (toggle), mirroring tag chips.
export function filterByPlatform(platform) {
  const next = paginationState.platformFilter === platform ? '' : platform;
  loadLibrary(currentStatuses(), paginationState.tagFilter, next, paginationState.ownedPlatformFilter);
}

// filterByOwnedPlatform sets the owned-platform filter and reloads.
export function filterByOwnedPlatform(platform) {
  const next = paginationState.ownedPlatformFilter === platform ? '' : platform;
  loadLibrary(currentStatuses(), paginationState.tagFilter, paginationState.platformFilter, next);
}

// updateTagFilterBar shows or hides the "filtered by" bar, covering tag,
// platform, owned, format, status, sort and year filters. Status is multi-select — each selected status
// gets its own chip with × to remove that one.
function updateTagFilterBar() {
  const existing = document.getElementById('tagFilterBar');
  if (existing) existing.remove();

  const tag = paginationState.tagFilter;
  const platform = paginationState.platformFilter;
  const owned = paginationState.ownedPlatformFilter;
  const format = paginationState.formatFilter || libraryFilters.format || '';
  const statuses = currentStatuses();
  const sort = paginationState.sort || libraryFilters.sort || '';
  const yearFrom = libraryFilters.yearFrom || paginationState.yearFrom || '';
  const yearTo = libraryFilters.yearTo || paginationState.yearTo || '';
  const hasSort = !!String(sort || '').trim();
  const hasFormat = !!String(format || '').trim();
  const hasYear = !!String(yearFrom || '').trim() || !!String(yearTo || '').trim();
  if (!tag && !platform && !owned && !hasFormat && statuses.length === 0 && !hasSort && !hasYear) return;

  const { tags: tagList, op } = parseTagQuery(tag || '');
  let display = '';
  if (statuses.length > 0) {
    display += statuses.map(s => {
      const label = STATUS_LABELS[s] || s;
      const color = STATUS_CHIP_COLORS[s] || 'var(--accent)';
      return `<span class="tag-filter-chip" data-status="${escapeHTML(s)}" style="--chip-color:${color};border-color:${color}">${escapeHTML(label)}<button type="button" class="tag-filter-chip-x" aria-label="Remove ${escapeHTML(s)} filter">×</button></span>`;
    }).join(' ');
  }
  if (tag) {
    const joiner = op === 'or' ? ' OR ' : ' AND ';
    if (display) display += `<span style="color:var(--text-dim);font-size:0.8rem"> ${joiner} </span>`;
    display += tagList.map(t => `<span class="tag-filter-chip" data-tag="${escapeHTML(t)}">${escapeHTML(t)}<button type="button" class="tag-filter-chip-x" aria-label="Remove ${escapeHTML(t)}">×</button></span>`).join(joiner.includes('OR') ? ' OR ' : ' ');
  }
  if (platform) {
    if (display) display += ' ';
    display += `<span class="tag-filter-chip tag-filter-platform" data-platform="${escapeHTML(platform)}">@${escapeHTML(platform)}<button type="button" class="tag-filter-chip-x" aria-label="Remove platform filter">×</button></span>`;
  }
  if (owned) {
    if (display) display += ' ';
    display += `<span class="tag-filter-chip tag-filter-platform" data-owned-platform="${escapeHTML(owned)}">owned:${escapeHTML(owned)}<button type="button" class="tag-filter-chip-x" aria-label="Remove owned platform filter">×</button></span>`;
  }
  if (hasFormat) {
    const formatLabels = {
      digital: 'Digital',
      physical: 'Physical',
      both: 'Both formats',
      none: 'No format',
    };
    if (display) display += ' ';
    display += `<span class="tag-filter-chip" data-format="${escapeHTML(format)}">Format: ${escapeHTML(formatLabels[format] || format)}<button type="button" class="tag-filter-chip-x" aria-label="Remove format filter">×</button></span>`;
  }
  if (hasSort) {
    const sortLabels = {
      '': 'Recently updated',
      'added': 'Recently added',
      'name': 'Name A–Z',
      'platform': 'Owned on: A–Z',
      'platform_desc': 'Owned on: Z–A',
      'owned_platform': 'Owned on: A–Z',
      'owned_platform_desc': 'Owned on: Z–A',
      'available_platform': 'Available on: A–Z',
      'available_platform_desc': 'Available on: Z–A',
      'release_new': 'Newest first',
      'release_old': 'Oldest first',
      'rating': 'My rating: Highest first',
      'my_rating': 'My rating: Highest first',
      'my_rating_low': 'My rating: Lowest first',
      'critic_rating': 'Critic rating: Highest first',
      'critic_rating_low': 'Critic rating: Lowest first',
      'critic': 'Critic rating: Highest first',
      'aggregated_rating': 'Critic rating: Highest first',
    };
    const label = sortLabels[sort] || sort;
    if (display) display += ' ';
    display += `<span class="tag-filter-chip" data-sort="${escapeHTML(sort)}">Sort: ${escapeHTML(label)}<button type="button" class="tag-filter-chip-x" aria-label="Remove sort">×</button></span>`;
  }
  if (hasYear) {
    let yearLabel = '';
    if (yearFrom && yearTo) yearLabel = `${escapeHTML(yearFrom)}–${escapeHTML(yearTo)}`;
    else if (yearFrom) yearLabel = `≥${escapeHTML(yearFrom)}`;
    else if (yearTo) yearLabel = `≤${escapeHTML(yearTo)}`;
    if (display) display += ' ';
    display += `<span class="tag-filter-chip" data-year="1">Year: ${yearLabel}<button type="button" class="tag-filter-chip-x" aria-label="Remove year filter">×</button></span>`;
  }

  const bar = document.createElement('div');
  bar.id = 'tagFilterBar';
  bar.className = 'tag-filter-bar';
  bar.innerHTML = `
    <span class="tag-filter-label">Filtered: ${display}</span>
    <button class="tag-filter-clear" type="button" aria-label="Clear all filters">✕ Clear</button>
  `;
  bar.querySelector('.tag-filter-clear').addEventListener('click', () => {
    // Clear everything including status/sort/year: go to "All"
    libraryFilters.yearFrom = ''; libraryFilters.yearTo = ''; libraryFilters.releaseFrom = ''; libraryFilters.releaseTo = ''; libraryFilters.format = ''; libraryFilters.sort = '';
    paginationState.yearFrom = ''; paginationState.yearTo = ''; paginationState.releaseFrom = ''; paginationState.releaseTo = ''; paginationState.formatFilter = ''; paginationState.sort = '';
    setStatuses([]);
    history.replaceState(null, '', window.location.pathname + window.location.search);
    loadLibrary([], '', '', '', {sort: '', yearFrom: '', yearTo: '', releaseFrom: '', releaseTo: ''});
  });

  bar.querySelectorAll('.tag-filter-chip-x').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      const chip = btn.parentElement;
      if (chip.dataset.status !== undefined) {
        const rm = chip.dataset.status;
        const next = currentStatuses().filter(s => s !== rm);
        setStatuses(next);
        loadLibrary(next, paginationState.tagFilter, paginationState.platformFilter, paginationState.ownedPlatformFilter);
        syncStatusFilterPanel();
        return;
      }
      if (chip.dataset.platform !== undefined) {
        loadLibrary(currentStatuses(), paginationState.tagFilter, '', paginationState.ownedPlatformFilter);
        return;
      }
      if (chip.dataset.ownedPlatform !== undefined) {
        loadLibrary(currentStatuses(), paginationState.tagFilter, paginationState.platformFilter, '');
        return;
      }
      if (chip.dataset.format !== undefined) {
        libraryFilters.format = ''; paginationState.formatFilter = '';
        loadLibrary(currentStatuses(), paginationState.tagFilter, paginationState.platformFilter, paginationState.ownedPlatformFilter, {format: ''});
        return;
      }
      if (chip.dataset.sort !== undefined) {
        libraryFilters.sort = ''; paginationState.sort = '';
        loadLibrary(currentStatuses(), paginationState.tagFilter, paginationState.platformFilter, paginationState.ownedPlatformFilter, {sort: ''});
        return;
      }
      if (chip.dataset.year !== undefined) {
        libraryFilters.yearFrom = ''; libraryFilters.yearTo = ''; libraryFilters.releaseFrom = ''; libraryFilters.releaseTo = '';
        paginationState.yearFrom = ''; paginationState.yearTo = ''; paginationState.releaseFrom = ''; paginationState.releaseTo = '';
        loadLibrary(currentStatuses(), paginationState.tagFilter, paginationState.platformFilter, paginationState.ownedPlatformFilter, {yearFrom: '', yearTo: '', releaseFrom: '', releaseTo: ''});
        return;
      }
      const removeTag = chip.dataset.tag;
      const remaining = tagList.filter(t => t !== removeTag);
      const newFilter = remaining.map(formatTagForQuery).join(op === 'or' ? '|' : ' ');
      loadLibrary(currentStatuses(), newFilter, paginationState.platformFilter, paginationState.ownedPlatformFilter);
    });
  });

  // Insert after the search box (library header) — FAB is floating so this is the visible summary
  const after = document.getElementById('searchResultsHeader') || document.querySelector('.search-wrap');
  if (after) after.insertAdjacentElement('afterend', bar);
}

// formatPlatformName shortens IGDB platform names for compact display:
// "PC (Microsoft Windows)" → "PC", "Xbox Series X|S" unchanged.
export function formatPlatformName(name) {
  const raw = String(name || '').trim();
  const short = raw.replace(/\s*\([^)]*\)/g, '').trim();
  return short || raw;
}

function platformNames(platforms) {
  const names = (platforms || []).map(formatPlatformName).filter(Boolean);
  return [...new Set(names)].sort((a, b) => {
    const byName = a.toLowerCase().localeCompare(b.toLowerCase());
    return byName || a.localeCompare(b);
  });
}

function platformsLine(platforms, max = 3) {
  const names = platformNames(platforms);
  if (!names.length) return '';
  let line = names.slice(0, max).join(' · ');
  if (names.length > max) line += ` +${names.length - max}`;
  return line;
}

function buildCardHTML(items) {
  const isSearch = paginationState.mode === 'search';
  return items.map((item, index) => {
    // High priority for the first 8 cards
    const priority = index < 8 ? ' fetchpriority="high"' : '';
    const tagsHTML = (item.tags && item.tags.length)
      ? `<div class="card-tags">${item.tags.map(t => `<span class="tag-chip" data-tag="${escapeHTML(t)}">${escapeHTML(t)}</span>`).join('')}</div>`
      : '';
    const ownedStatus = isSearch ? ownedStatuses.get(Number(item.game_id)) : undefined;
    const ownedBadge = ownedStatus !== undefined
      ? `<div class="owned-badge status-${escapeHTML(ownedStatus)}">${escapeHTML(statusBadgeLabel(ownedStatus))}</div>`
      : '';

    let libraryOverlays = '';
    if (!isSearch) {
      // Unified status pill (top-left). Completed items show the year so the
      // old completion badge stays readable at a glance.
      const isDone = item.status === 'completed';
      const badgeText = isDone && item.completed_at
        ? `✓ ${formatYear(item.completed_at)}`
        : STATUS_LABELS[item.status] || item.status;
      libraryOverlays += `<div class="card-status status-${escapeHTML(item.status)}">${escapeHTML(badgeText)}</div>`;
    }

    // Ownership info occupies the tag-chip row when there are no tags.
    const owned = Array.isArray(item.owned_platforms) && item.owned_platforms.length
      ? item.owned_platforms
      : (item.platform ? [item.platform] : []);
    const ownedNames = platformNames(owned);
    const ownedLine = platformsLine(owned);
    const ownBits = [ownedLine, item.medium].filter(Boolean).join(' · ');
    const ownedTitle = [ownedNames.join(', '), item.medium].filter(Boolean).join(' · ');
    const ownMetaHTML = (!isSearch && ownBits)
      ? `<div class="card-platforms card-owned-platforms" title="${escapeHTML(ownedTitle)}">${escapeHTML(ownBits)}</div>`
      : '';

    // Available platforms (IGDB data) — shown for library and search cards
    // so it's clear what a game can be played on before/after logging.
    const availableNames = platformNames(item.platforms);
    const platLine = platformsLine(availableNames);
    const platformsHTML = platLine
      ? `<div class="card-platforms" title="${escapeHTML(availableNames.join(', '))}">${escapeHTML(platLine)}</div>`
      : '';

    // Release date — visible on search result cards so the timing of a
    // release (or upcoming TBA) is obvious without opening the modal.
    const releaseUnix = item.first_release_date || 0;
    const releaseHTML = isSearch
      ? `<div class="card-release release-${releaseStatus(releaseUnix)}">${escapeHTML(releaseLabel(releaseUnix))}</div>`
      : '';

    return `
    <div class="game-card" data-game-id="${item.game_id}">
      <img src="${getCoverURL(item)}" alt="${escapeHTML(item.game_name)}" loading="lazy" decoding="async"${priority}>
      ${libraryOverlays}
      <div class="card-title">${escapeHTML(item.game_name)}</div>
      ${releaseHTML}
      ${platformsHTML}
       ${tagsHTML}
       ${ownMetaHTML}
      ${ownedBadge}
      ${item.rating > 0 ? `<div class="rating-display">${Number(item.rating)}</div>` : ''}
    </div>
  `}).join('');
}

// attachCardEvents attaches the click handler to game cards. Clicking a card
// opens the routable edit modal (the same popup search results use) rather than
// flipping the card, which is hard to use on small mobile covers.
// If items are provided, only attach to cards for those items; otherwise attach to all cards.
// originalSearchResults is provided in search mode to map back to original result objects.
function attachCardEvents(grid, newItems = null, originalSearchResults = null) {
  let cardsToAttach;
  if (newItems) {
    // Attach only to cards for the newly added items
    cardsToAttach = Array.from(grid.querySelectorAll('.game-card')).filter(card =>
      newItems.some(item => card.dataset.gameId == item.game_id)
    );
  } else {
    // Attach to all cards
    cardsToAttach = Array.from(grid.querySelectorAll('.game-card'));
  }

  cardsToAttach.forEach(card => {
    // Remove existing listeners by cloning (simplest approach)
    const newCard = card.cloneNode(true);
    card.parentNode.replaceChild(newCard, card);
    card = newCard;

    bindCoverFallback(card);

    card.addEventListener('click', (e) => {
      // Tag chips are handled by the delegated grid listener; don't also open
      // the edit modal.
      if (e.target.closest('.tag-chip')) return;
      const gameId = card.dataset.gameId;

      if (paginationState.mode === 'search') {
        // In search mode, use original search result
        const result = searchResultsById.get(gameId);
        if (!result) return;
        if (ownedStatuses.has(Number(gameId))) {
          // Already owned — open the edit form (never the destructive Add form).
          openLibraryItemById(result.id);
        } else {
          addGameToLibrary(result);
        }
      } else {
        // In library mode, use library item
        const item = itemsById.get(gameId);
        if (item) openLibraryItemModal(item);
      }
    });
  });
}

// bindCoverFallback swaps a broken remote cover image for the local
// /covers/<id>.jpg route, which serves the cached file or the placeholder
// SVG. Prevents dead remote URLs from rendering as broken images.
function bindCoverFallback(card) {
  const img = card.querySelector('img');
  const gameId = card.dataset.gameId;
  if (!img || !gameId) return;
  img.addEventListener('error', () => {
    if (img.dataset.coverFallback) return;
    img.dataset.coverFallback = '1';
    img.src = `/covers/${gameId}.jpg`;
  });
}

// openLibraryItemById fetches a single library item and opens its edit modal.
async function openLibraryItemById(gameID) {
  try {
    const item = await library.get(gameID);
    openLibraryItemModal(item);
  } catch (err) {
    console.error('Failed to fetch library item:', err.message);
  }
}

// openGameModal opens the routable game popup for a #game/<id> deep link. If
// the game is in the library (rendered or not) it opens the edit form;
// otherwise it fetches metadata and opens the add-to-library form.
export async function openGameModal(gameID) {
  const item = itemsById.get(String(gameID));
  if (item) {
    openLibraryItemModal(item);
    return;
  }
  // The game may be owned but not on the currently loaded page.
  try {
    const existing = await library.get(gameID);
    openLibraryItemModal(existing);
    return;
  } catch {
    // Not in library — fall through to the add flow.
  }
  try {
    const game = await getGame(gameID);
    await addGameToLibrary(game);
  } catch (err) {
    console.error('Failed to fetch game:', err.message);
  }
}

// addGameToLibrary opens the Add form for a game. If the game turns out to be
// already in the library, the EDIT form opens instead — saving from the Add
// form upserts and would have silently reset status/rating/tags/notes.
export async function addGameToLibrary(game) {
  if (!game || !game.id) return;
  try {
    const item = await library.get(game.id);
    openLibraryItemModal(item);
    return;
  } catch {
    // Not in library — proceed with the add form.
  }
  openAddForm(game);
}

function openAddForm(game) {
  openGameForm({
    id: game.id,
    name: game.name,
    cover: getCoverURL(game),
    firstReleaseDate: game.first_release_date || 0,
    platforms: game.platforms || [],
    inLibrary: false,
  });
}

// openLibraryItemModal opens the same routable popup as a search result, but
// pre-filled with a library item's current status/rating/playtime/notes and
// offering Save/Remove. This replaces the old card-flip interaction, which was
// awkward on small mobile covers.
export function openLibraryItemModal(item) {
  if (!item || !item.game_id) return;
  openGameForm({
    id: item.game_id,
    name: item.game_name,
    cover: getCoverURL(item),
    firstReleaseDate: item.first_release_date || 0,
    status: item.status,
    rating: item.rating || 0,
    playtime: item.playtime_minutes || 0,
    tags: item.tags || [],
    notes: item.notes || '',
    startedAt: item.started_at || null,
    completedAt: item.completed_at || null,
    platformsOwned: item.owned_platforms || (item.platform ? [item.platform] : []),
    medium: item.medium || '',
    platforms: item.platforms || [],
    inLibrary: true,
  });
}

// openGameForm renders the routable game popup (#game/<id>). In "add" mode it
// shows a blank form with an "Add to Library" action; in "edit" mode
// (inLibrary=true) it is pre-filled and offers Save and Remove. Both actions
// POST to library.add, which upserts.
function openGameForm({ id, name, cover, year = '', firstReleaseDate = 0, status = '',
                        rating = 0, playtime = 0, tags = [], notes = '',
                        startedAt = null, completedAt = null,
                        platformsOwned = [], medium = '', platforms = [],
                        inLibrary = false }) {
  // Replace any existing modal (e.g. user clicks a second result).
  const existing = document.getElementById('addGameModal');
  if (existing) existing.remove();

  const title = inLibrary ? 'Edit Library Entry' : 'Add to Library';
  // Release date display — prefer the unix timestamp; fall back to the legacy
  // year number so older call paths still render a label. TBA is always shown
  // so an unknown date is explicit rather than silently blank.
  let releaseUnix = Number(firstReleaseDate) || 0;
  if (!releaseUnix && year) {
    const y = Number(year);
    if (y >= 1900 && y <= 2100) releaseUnix = Date.UTC(y, 0, 1) / 1000;
  }
  const releaseText = modalReleaseLabel(releaseUnix);
  const releaseCls = releaseStatus(releaseUnix);
  const hours = Math.round((playtime / 60) * 100) / 100;

  // Hours dropdown: quarter-hour steps (the old stepper's granularity) from 0
  // up to at least 200h — the cap stretches if a stored value is bigger, and
  // off-grid stored values (e.g. 4.13) are spliced in so they stay visible
  // and saveable.
  const fmtHours = (h) => h.toFixed(2).replace(/\.?0+$/, '');
  const hourVals = [];
  for (let q = 0; q <= Math.max(200, hours) * 4; q++) hourVals.push(q / 4);
  if (!hourVals.includes(hours)) {
    hourVals.push(hours);
    hourVals.sort((a, b) => a - b);
  }
  const hoursOptionsHTML = hourVals.map(h =>
    `<option value="${fmtHours(h)}"${h === hours ? ' selected' : ''}>${fmtHours(h)}</option>`).join('');

  // Dates are server-managed (auto-set on completion) but correctable: the
  // finish date is editable and only sent when changed, so an untouched save
  // never disturbs it. Empty + sent = explicit clear.
  const toInputValue = (iso) => (iso ? String(iso).slice(0, 10) : '');
  const initialDates = {
    completed: toInputValue(completedAt),
  };
  const pendingDateChanges = { completed: undefined };
  const datesHTML = inLibrary ? `
        <div class="modal-date-row">
          <span class="modal-date-label">Completed</span>
          <input type="date" class="modal-date-input" data-datekey="completed" value="${escapeHTML(initialDates.completed)}"
                 title="Fills in when you mark it Completed">
        </div>` : '';

  // Ownership is chosen directly on the available-platform chips, which are
  // MULTI-select: a highlighted chip means you own it there, and a game can
  // be owned on several. Stored values that match no IGDB chip still show as
  // their own selected chips so existing data stays visible and clearable.
  const norm = (s) => s.toLowerCase();
  // Keyed by lowercase name so chip matching is case-insensitive; the value
  // preserves the stored/original casing for saving.
  let ownedMap = new Map();
  (platformsOwned || []).map(p => String(p).trim()).filter(Boolean)
    .forEach(p => ownedMap.set(norm(p), p));
  const availPlatforms = (platforms || []).map(p => String(p).trim()).filter(Boolean);
  const extraOwned = [...ownedMap.keys()].filter(k => !availPlatforms.some(a => norm(a) === k));
  const chipHTML = (p) => `<button type="button" class="plat-chip${ownedMap.has(norm(p)) ? ' selected' : ''}" data-full="${escapeHTML(p)}" title="${escapeHTML(p)}">${escapeHTML(formatPlatformName(p))}</button>`;
  const availChipsHTML = (availPlatforms.length || extraOwned.length) ? `
        <div class="modal-avail">
          <span class="modal-avail-label">Available on <span class="modal-avail-hint">(highlighted = owned)</span></span>
          <div class="modal-avail-chips">
            ${availPlatforms.map(chipHTML).join('')}
            ${extraOwned.map(k => chipHTML(ownedMap.get(k))).join('')}
          </div>
        </div>` : '';

  const modal = document.createElement('div');
  modal.id = 'addGameModal';
  modal.className = 'modal-overlay';
  modal.setAttribute('role', 'dialog');
  modal.setAttribute('aria-modal', 'true');
  modal.setAttribute('aria-labelledby', 'addGameModalTitle');
  modal.innerHTML = `
    <div class="modal-card">
      <div class="sheet-handle" aria-hidden="true"><span class="sheet-grip"></span></div>
      <div class="modal-header">
        <h2 id="addGameModalTitle">${title}</h2>
        <button class="modal-close" type="button" aria-label="Close">&times;</button>
      </div>
      <div class="modal-body">
        <div class="modal-game-info">
          <img src="${cover}" alt="${escapeHTML(name)}" decoding="async">
          <div class="modal-game-meta">
            <h3>${escapeHTML(name)}</h3>
            <div class="modal-release release-${releaseCls}">${escapeHTML(releaseText)}</div>
          </div>
          <div class="modal-rating">
            <div class="rating-dial">
              <svg viewBox="0 0 120 120" aria-hidden="true">
                <circle class="dial-track" cx="60" cy="60" r="52"></circle>
                <circle class="dial-fill" cx="60" cy="60" r="52"></circle>
              </svg>
              <select class="rating-input" aria-label="Rating, 0 to 100">
                ${Array.from({ length: 101 }, (_, i) =>
                  `<option value="${i}"${i === (Number(rating) || 0) ? ' selected' : ''}>${i === 0 ? 'Unrated' : i}</option>`).join('')}
              </select>
            </div>
            <div class="rating-actions">
              <button type="button" class="mini-btn" data-rating="reset"
                      aria-label="Restore original rating" hidden>&#8634;</button>
            </div>
          </div>
        </div>
        ${datesHTML}
        <div class="modal-pair">
          <div class="modal-field">
            <span class="field-label">Status</span>
            <select class="modal-select" data-role="status" aria-label="Status">
              <option value=""${!status ? ' selected' : ''} disabled>— Select status —</option>
              ${VALID_STATUSES.map(s => `<option value="${s}"${s === status ? ' selected' : ''}>${STATUS_LABELS[s]}</option>`).join('')}
            </select>
          </div>
          <div class="modal-field">
            <span class="field-label">Hours</span>
            <select class="modal-select" data-role="hours" aria-label="Hours played">
              ${hoursOptionsHTML}
            </select>
          </div>
        </div>
        <div class="modal-field">
          <span class="field-label">Tags</span>
          <div class="modal-tags-wrap">
            <div class="modal-tags-chips">${tags.map(t => `<span class="tag-chip tag-chip-removable" data-tag="${escapeHTML(t)}">${escapeHTML(t)}<button type="button" class="tag-chip-x" aria-label="Remove ${escapeHTML(t)}">&times;</button></span>`).join('')}</div>
            <input type="text" class="modal-tags-input" placeholder="Add tag..." list="tagSuggestions">
            <datalist id="tagSuggestions"></datalist>
          </div>
        </div>
        <div class="modal-field">
          <span class="field-label">Notes</span>
          <textarea class="modal-notes notes-autogrow" placeholder="Notes...">${escapeHTML(notes)}</textarea>
        </div>
        <div class="modal-ownership">
          ${availChipsHTML}
          <div class="modal-field">
            <span class="field-label">Format</span>
            <div class="chip-row" data-role="medium">
              <button type="button" class="opt-chip${medium === 'physical' ? ' selected' : ''}" data-value="physical">Physical</button>
              <button type="button" class="opt-chip${medium === 'digital' ? ' selected' : ''}" data-value="digital">Digital</button>
            </div>
          </div>
        </div>
      </div>
      <div class="modal-footer">
        <span class="autosave-status" aria-live="polite"></span>
        <button class="btn btn-secondary modal-cancel" type="button">Close</button>
        ${inLibrary ? '<button class="btn modal-remove" type="button">Remove</button>' : ''}
      </div>
    </div>`;
  document.body.appendChild(modal);
  document.body.classList.add('modal-open');

  const prevHash = window.location.hash;
  const prevHashWasGame = prevHash.startsWith('#game/');
  setGameHash(id);

  // --- auto-save -------------------------------------------------------------
  // Every change saves itself; there is no submit button. Rapid edits
  // (slider drags, typing) coalesce through a short debounce, and closing
  // the form flushes anything still pending. The upsert endpoint means the
  // first change on an un-owned game creates the library entry.
  let saveTimer = null;
  let unsavedChanges = false;
  let savedAtLeastOnce = false;
  let createdYet = inLibrary;
  let removed = false;
  let lastSnapshot = null;
  let flashTimer = null;

  // Chip rows are single-select groups of buttons; Format may be cleared by
  // re-tapping the selected chip, Status always keeps a value.
  const setupChipRow = (row, initial, allowClear) => {
    let value = initial;
    row.addEventListener('click', (e) => {
      const chip = e.target.closest('.opt-chip');
      if (!chip || !row.contains(chip)) return;
      if (chip.dataset.value === value && allowClear) {
        value = '';
        chip.classList.remove('selected');
      } else {
        value = chip.dataset.value;
        row.querySelectorAll('.opt-chip').forEach(c =>
          c.classList.toggle('selected', c === chip));
      }
      scheduleSave();
    });
    return () => value;
  };
  // Status is a native select; its text takes the list-badge color so the
  // form matches the pills on the cards at a glance.
  const STATUS_COLORS = {
    wishlist: '#2196f3',
    backlog: '#ff9800',
    playing: '#4caf50',
    completed: '#9c27b0',
    abandoned: '#f44336',
  };
  const statusSel = modal.querySelector('[data-role="status"]');
  const renderStatus = () => {
    statusSel.style.color = STATUS_COLORS[statusSel.value] || '';
  };
  renderStatus();
  statusSel.addEventListener('change', () => {
    renderStatus();
    scheduleSave();
  });

  const hoursSel = modal.querySelector('[data-role="hours"]');
  hoursSel.addEventListener('change', () => scheduleSave());

  const getMedium = setupChipRow(modal.querySelector('[data-role="medium"]'), medium || '', true);

  const collectPayload = () => {
    const newHours = parseFloat(hoursSel.value) || 0;
    const payload = {
      status: statusSel.value,
      rating: ratingValue,
      playtime_minutes: Math.max(0, Math.round(newHours * 60)),
      tags: Array.from(modal.querySelectorAll('.modal-tags-chips .tag-chip-removable'))
        .map(c => c.firstChild.textContent.trim()),
      notes: modal.querySelector('.modal-notes').value,
      platforms: [...ownedMap.values()],
      platform: [...ownedMap.values()][0] || '',
      medium: getMedium(),
    };
    // Explicit date changes only — omitting the fields preserves existing
    // values (and keeps server-side auto-tracking working).
    if (inLibrary && pendingDateChanges.completed !== undefined) payload.completed_at = pendingDateChanges.completed;
    return payload;
  };

  const flashSaveState = (text, sticky = false) => {
    const el = modal.querySelector('.autosave-status');
    if (!el) return;
    el.textContent = text;
    el.classList.add('visible');
    clearTimeout(flashTimer);
    if (!sticky) flashTimer = setTimeout(() => el.classList.remove('visible'), 1600);
  };

  const runSave = async () => {
    clearTimeout(saveTimer);
    saveTimer = null;
    if (removed) return;
    const payload = collectPayload();
    if (!payload.status) {
      const el = modal.querySelector('.autosave-status');
      if (el) el.classList.remove('visible');
      flashSaveState('Choose a status');
      unsavedChanges = false;
      return;
    }
    const snapshot = JSON.stringify(payload);
    if (snapshot === lastSnapshot) {
      unsavedChanges = false;
      return;
    }
    flashSaveState('Saving…', true);
    try {
      await library.add(id, payload);
      lastSnapshot = snapshot;
      unsavedChanges = false;
      savedAtLeastOnce = true;
      if (!createdYet) {
        createdYet = true;
        showToast(`Added ${name} to ${STATUS_LABELS[payload.status] || 'library'}`);
      }
      flashSaveState('Saved ✓');
    } catch (err) {
      // Clear the sticky indicator; unsavedChanges stays true so closing
      // retries once.
      const el = modal.querySelector('.autosave-status');
      if (el) el.classList.remove('visible');
      showToast(`Failed to auto-save ${name}: ${err.message}`, { type: 'error' });
    }
  };

  const scheduleSave = () => {
    if (removed) return;
    unsavedChanges = true;
    clearTimeout(saveTimer);
    saveTimer = setTimeout(runSave, 700);
  };

  // --- rating ----------------------------------------------------------------
  // The number in the center of the dial IS the control: a transparent,
  // borderless native <select> filling the circle. Tapping it opens the
  // standard picker (iPhone picker wheel, desktop dropdown with keyboard
  // type-ahead); the arc around it mirrors the choice (length + hue,
  // red → amber → green). 0 ("Unrated") shows an empty gray ring.
  const initialRating = Number(rating) || 0;
  let ratingValue = initialRating;
  const ratingSel = modal.querySelector('.rating-input');
  const dialFill = modal.querySelector('.dial-fill');
  const DIAL_CIRC = 2 * Math.PI * 52;

  // Hue 0 (red) at 0 → hue 120 (green) at 100; gray means "unrated".
  const ratingColor = () =>
    ratingValue > 0 ? `hsl(${Math.round(ratingValue * 1.2)}, 70%, 52%)` : '';

  // One-tap way back: restore appears only once the value has drifted from
  // what the form opened with. Clearing is deliberate by design — pick
  // "Unrated" (the first entry) in the picker; there is no clear button to
  // hit by accident.
  const resetBtn = modal.querySelector('[data-rating="reset"]');
  const updateRatingHelpers = () => {
    resetBtn.hidden = ratingValue === initialRating;
  };

  const renderRating = () => {
    ratingSel.value = String(ratingValue);
    const col = ratingColor();
    if (ratingValue > 0) {
      dialFill.style.display = '';
      dialFill.style.strokeDasharray = `${DIAL_CIRC * ratingValue / 100} ${DIAL_CIRC}`;
      dialFill.style.stroke = col;
    } else {
      // Hidden rather than zero-length: a round linecap would still paint a dot.
      dialFill.style.display = 'none';
    }
    ratingSel.style.color = col;
    // "Unrated" is longer than the digits; shrink it to fit the circle.
    ratingSel.classList.toggle('unrated', ratingValue === 0);
    updateRatingHelpers();
  };
  renderRating();

  const applyRating = (next) => {
    const v = Math.max(0, Math.min(100, Math.round(Number(next))));
    if (!Number.isFinite(v) || v === ratingValue) return;
    ratingValue = v;
    renderRating();
    scheduleSave();
  };

  ratingSel.addEventListener('change', () => applyRating(ratingSel.value));
  resetBtn.addEventListener('click', () => applyRating(initialRating));

  modal.querySelector('.modal-notes').addEventListener('input', scheduleSave);

  // Auto-growing notes: expands with content (up to ~40% of the viewport)
  // instead of a fixed two-line box with an inner scrollbar.
  const notesEl = modal.querySelector('.modal-notes');
  const growNotes = () => {
    notesEl.style.height = 'auto';
    notesEl.style.height = `${Math.min(notesEl.scrollHeight + 2, Math.round(window.innerHeight * 0.4))}px`;
  };
  notesEl.addEventListener('input', growNotes);
  growNotes();

  // Tap a platform chip to toggle owning the game on it — multi-select, so
  // a game can be owned on several platforms at once.
  modal.querySelectorAll('.plat-chip').forEach(chip => {
    chip.addEventListener('click', () => {
      const key = norm(chip.dataset.full);
      const wasSelected = ownedMap.has(key);
      if (wasSelected) {
        ownedMap.delete(key);
      } else {
        ownedMap.set(key, chip.dataset.full);
      }
      // Toggle selected class instantly; use the new state so the color
      // updates even while the pointer is still hovering (hover previously
      // masked the change because both states used the same accent bg).
      chip.classList.toggle('selected', !wasSelected);
      scheduleSave();
    });
  });

  // Date edits are tracked per-field; only changed values are sent (empty =
  // explicit clear, a value sets it — both match the API's semantics).
  modal.querySelectorAll('.modal-date-input').forEach(inp => {
    inp.addEventListener('change', () => {
      const key = inp.dataset.datekey;
      pendingDateChanges[key] = inp.value || '';
      inp.classList.toggle('dirty', pendingDateChanges[key] !== initialDates[key]);
      scheduleSave();
    });
  });

  // Focus trap: keep Tab cycling inside the modal instead of escaping to
  // the page behind it.
  modal.addEventListener('keydown', (e) => {
    if (e.key !== 'Tab') return;
    const focusables = Array.from(
      modal.querySelectorAll('button, input, select, textarea')
    ).filter(el => !el.disabled && el.offsetParent !== null);
    if (focusables.length === 0) return;
    const first = focusables[0];
    const last = focusables[focusables.length - 1];
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  });

  // Swipe-down-to-close on the mobile bottom sheet: drag the grip under the
  // title bar; past ~120px the sheet dismisses, otherwise it springs back.
  const sheetCard = modal.querySelector('.modal-card');
  const sheetHandle = modal.querySelector('.sheet-handle');
  let draggingSheet = false;
  let dragStartY = 0;
  sheetHandle.addEventListener('pointerdown', (e) => {
    draggingSheet = true;
    dragStartY = e.clientY;
    sheetCard.style.transition = 'none';
    try { sheetHandle.setPointerCapture(e.pointerId); } catch { /* already gone */ }
  });
  sheetHandle.addEventListener('pointermove', (e) => {
    if (!draggingSheet) return;
    sheetCard.style.transform = `translateY(${Math.max(0, e.clientY - dragStartY)}px)`;
  });
  const endSheetDrag = (e) => {
    if (!draggingSheet) return;
    draggingSheet = false;
    sheetCard.style.transition = '';
    const dy = Math.max(0, (e.clientY || dragStartY) - dragStartY);
    if (dy > 120) {
      close();
      return;
    }
    sheetCard.style.transform = '';
  };
  sheetHandle.addEventListener('pointerup', endSheetDrag);
  sheetHandle.addEventListener('pointercancel', endSheetDrag);

  // Tag chip input
  const chipsContainer = modal.querySelector('.modal-tags-chips');
  const tagInput = modal.querySelector('.modal-tags-input');

  // Tag autocomplete: the user's whole tag vocabulary feeds a native datalist,
  // so typing suggests existing tags and taps a familiar mobile picker UI.
  autocompleteTags('', 100).then(list => {
    const dl = modal.querySelector('#tagSuggestions');
    if (dl && Array.isArray(list)) {
      dl.innerHTML = list.map(t => `<option value="${escapeHTML(t)}"></option>`).join('');
    }
  }).catch(() => { /* suggestions are cosmetic */ });

  function addChip(text) {
    const chip = document.createElement('span');
    chip.className = 'tag-chip tag-chip-removable';
    chip.dataset.tag = text;
    chip.innerHTML = `${escapeHTML(text)}<button type="button" class="tag-chip-x" aria-label="Remove ${escapeHTML(text)}">&times;</button>`;
    chip.querySelector('.tag-chip-x').addEventListener('click', () => {
      chip.remove();
      scheduleSave();
    });
    chipsContainer.appendChild(chip);
    scheduleSave();
  }

  chipsContainer.querySelectorAll('.tag-chip-x').forEach(btn => {
    btn.addEventListener('click', () => {
      btn.parentElement.remove();
      scheduleSave();
    });
  });

  tagInput.addEventListener('keydown', (e) => {
    const val = tagInput.value.trim();
    if (e.key === 'Enter' || e.key === ',') {
      e.preventDefault();
      if (val) { addChip(val); tagInput.value = ''; }
    } else if (e.key === 'Backspace' && !val) {
      const chips = chipsContainer.querySelectorAll('.tag-chip-removable');
      if (chips.length) {
        chips[chips.length - 1].remove();
        scheduleSave();
      }
    }
  });

  // On blur, convert any pending text to a chip
  tagInput.addEventListener('blur', () => {
    const val = tagInput.value.trim();
    if (val) { addChip(val); tagInput.value = ''; }
  });

  // Clicking a tag chip's body (not its ×) offers to show every library game
  // carrying that tag: a small popover with one action. Choosing it closes the
  // form and applies the library tag filter, so unsaved edits are never lost
  // by an accidental chip click.
  let tagMenu = null;

  function dismissTagMenu() {
    if (!tagMenu) return;
    tagMenu.remove();
    tagMenu = null;
    document.removeEventListener('click', onDocClickTagMenu, true);
    document.removeEventListener('keydown', onTagMenuEsc, true);
    window.removeEventListener('resize', dismissTagMenu);
    window.removeEventListener('scroll', dismissTagMenu, true);
  }

  // Capture phase so the menu dismisses on any outside click before other
  // handlers (e.g. the modal-overlay backdrop close) react to it.
  function onDocClickTagMenu(e) {
    if (tagMenu && !tagMenu.contains(e.target)) dismissTagMenu();
  }

  // Escape dismisses just the menu — stopPropagation keeps escHandler from
  // closing the whole form at the same time.
  function onTagMenuEsc(e) {
    if (e.key === 'Escape') {
      e.stopPropagation();
      dismissTagMenu();
    }
  }

  function showTagMenu(chip) {
    dismissTagMenu();
    const tag = chip.dataset.tag;
    if (!tag) return;

    // Build the menu actions relative to any active tag filter. Filters are
    // rebuilt from parsed tags, never raw-concatenated: parseTagQuery folds
    // any mix into a single op ('or' wins) and treats spaces after a pipe as
    // literal tag characters, so naive concatenation would corrupt
    // multi-word tags like "Switch 2".
    const { tags: currentTags, op } = parseTagQuery(paginationState.tagFilter);
    const fmt = formatTagForQuery;
    // AND/OR on a tag already in the filter would only duplicate it.
    const dup = currentTags.some(t => t.toLowerCase() === tag.toLowerCase());
    const actions = [];
    if (currentTags.length === 0) {
      actions.push({ label: `Show all games tagged "${tag}"`, filter: fmt(tag) });
    } else {
      actions.push({ label: `Show only games tagged "${tag}"`, filter: fmt(tag) });
      if (!dup) {
        // Grouped (a|b) AND c isn't expressible in the query grammar (any
        // pipe forces OR), so AND is offered only on pure-AND filters.
        if (op !== 'or') {
          actions.push({ label: `Also require "${tag}" (AND)`, filter: [...currentTags, tag].map(fmt).join(' ') });
        }
        actions.push({ label: `Also include "${tag}" (OR)`, filter: [...currentTags, tag].map(fmt).join('|') });
      }
    }

    tagMenu = document.createElement('div');
    tagMenu.className = 'tag-chip-menu';
    tagMenu.setAttribute('role', 'menu');
    tagMenu.innerHTML = actions.map(a =>
      `<button type="button" class="tag-chip-menu-item" role="menuitem">${escapeHTML(a.label)}</button>`
    ).join('');
    tagMenu.querySelectorAll('.tag-chip-menu-item').forEach((btn, i) => {
      btn.addEventListener('click', () => {
        dismissTagMenu();
        close();
        applyTagFilter(actions[i].filter);
      });
    });
    document.body.appendChild(tagMenu);
    // Fixed positioning from the chip's viewport rect; clamp so wide tags
    // don't overflow the right edge.
    const r = chip.getBoundingClientRect();
    const left = Math.max(8, Math.min(r.left, window.innerWidth - tagMenu.offsetWidth - 8));
    tagMenu.style.left = `${Math.round(left)}px`;
    tagMenu.style.top = `${Math.round(Math.min(r.bottom + 6, window.innerHeight - tagMenu.offsetHeight - 8))}px`;
    setTimeout(() => {
      document.addEventListener('click', onDocClickTagMenu, true);
      document.addEventListener('keydown', onTagMenuEsc, true);
      window.addEventListener('resize', dismissTagMenu);
      window.addEventListener('scroll', dismissTagMenu, true);
    }, 0);
  }

  chipsContainer.addEventListener('click', (e) => {
    if (e.target.closest('.tag-chip-x')) return;
    const chip = e.target.closest('.tag-chip-removable');
    if (chip && chipsContainer.contains(chip)) showTagMenu(chip);
  });

  const close = async () => {
    dismissTagMenu();
    // Flush any debounced edit before tearing down — closing must never
    // lose a change. (After removal, runSave no-ops via the removed flag.)
    if (unsavedChanges || saveTimer) await runSave();
    const viewMode = paginationState.mode;
    const viewSearchQuery = paginationState.searchQuery;
    const shouldRefresh = savedAtLeastOnce;
    modal.remove();
    document.body.classList.remove('modal-open');
    document.removeEventListener('keydown', escHandler);
    if (window.location.hash.startsWith('#game/')) {
      if (prevHashWasGame || !prevHash) {
        history.replaceState(null, '', window.location.pathname + window.location.search);
      } else {
        history.replaceState(null, '', window.location.pathname + window.location.search + prevHash);
      }
    }
    // Reflect auto-saved edits in the view once, on the way out. Search-mode
    // refreshes are important when the active filter is Wishlist or Backlog.
    if (shouldRefresh) {
      if (viewMode === 'search' && viewSearchQuery) {
        loadSearchResults(viewSearchQuery);
      } else if (viewMode === 'library') {
        loadLibrary(currentStatuses(), paginationState.tagFilter, paginationState.platformFilter);
      }
    }
  };
  modal.querySelector('.modal-close').addEventListener('click', close);
  modal.querySelector('.modal-cancel').addEventListener('click', close);
  modal.addEventListener('click', (e) => {
    if (e.target === modal) close();
  });
  const escHandler = (e) => { if (e.key === 'Escape') close(); };
  document.addEventListener('keydown', escHandler);

  const removeBtn = modal.querySelector('.modal-remove');
  if (removeBtn) {
    removeBtn.addEventListener('click', async () => {
      const viewMode = paginationState.mode;
      const viewSearchQuery = paginationState.searchQuery;
      // No confirm() — removal is undoable via the toast instead, which is
      // both faster and safer than a modal nag. Any pending auto-save is
      // dropped: the undo snapshot restores the ORIGINAL values, so a queued
      // save must not resurrect the item with new ones.
      removed = true;
      clearTimeout(saveTimer);
      saveTimer = null;
      unsavedChanges = false;
      const snapshot = {
        status,
        rating,
        playtime_minutes: playtime,
        tags,
        notes,
        platforms: platformsOwned,
        platform: platformsOwned[0] || '',
        medium,
      };
      if (startedAt) snapshot.started_at = startedAt;
      if (completedAt) snapshot.completed_at = completedAt;

      removeBtn.disabled = true;
      removeBtn.textContent = 'Removing...';
      try {
        await library.remove(id);
        // The handler below refreshes the grid itself; stop close() from
        // double-reloading.
        savedAtLeastOnce = false;
        close();
        if (viewMode === 'search' && viewSearchQuery) {
          await loadSearchResults(viewSearchQuery);
        } else {
          await loadLibrary(currentStatuses(), paginationState.tagFilter, paginationState.platformFilter);
        }
        showToast(`Removed ${name}`, {
          action: {
            label: 'Undo',
            fn: async () => {
              try {
                await library.add(id, snapshot);
                showToast(`Restored ${name}`);
              } catch (err) {
                showToast(`Couldn't restore ${name}: ${err.message}`, { type: 'error' });
              }
              if (viewMode === 'search' && viewSearchQuery) {
                await loadSearchResults(viewSearchQuery);
              } else {
                await loadLibrary(currentStatuses(), paginationState.tagFilter, paginationState.platformFilter);
              }
            },
          },
        });
      } catch (err) {
        removeBtn.disabled = false;
        removeBtn.textContent = 'Remove';
        showToast(`Failed to remove game: ${err.message}`, { type: 'error' });
      } finally {
        document.removeEventListener('keydown', escHandler);
      }
    });
  }

  modal.querySelector('.modal-close').focus();
}

// escapeHTML escapes all five HTML-sensitive characters. The previous
// implementation (div.textContent = s; return div.innerHTML) left quotes
// untouched, which allowed attribute injection in data-tag="..."/alt="..."
// contexts — a verified stored-XSS vector.
export function escapeHTML(str) {
  return String(str ?? '').replace(/[&<>"']/g, c => ({
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&#39;',
  }[c]));
}
