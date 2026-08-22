import { library, getCoverURL, getGame, searchGamesFull, parseTagQuery, formatTagForQuery } from './api.js';
import { formatDate, formatYear } from './dates.js';

const VALID_STATUSES = ['wishlist', 'backlog', 'playing', 'completed', 'abandoned'];

// Human labels for statuses (the raw values are lowercase internal codes).
const STATUS_LABELS = {
  wishlist: 'Wishlist',
  backlog: 'Backlog',
  playing: 'Playing',
  completed: 'Completed',
  abandoned: 'Abandoned',
};

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

// Pagination state
let paginationState = {
  currentStatus: '',
  tagFilter: '',
  platformFilter: '',
  offset: 0,
  loading: false,
  hasMore: true,
  pageSize: PAGE_SIZE,
  mode: 'library', // 'library' or 'search'
  searchQuery: '',
};

let scrollListenerAttached = false;

// Active sort/filters for search mode (SEARCH_IMPROVEMENTS.md §5). Scoped to
// one query: switching searches resets them.
const searchFilters = { sort: '', yearFrom: '', yearTo: '', minRating: '' };
let searchFiltersQuery = '';

function resetSearchFilters(query) {
  if (searchFiltersQuery === query) return;
  searchFiltersQuery = query;
  searchFilters.sort = '';
  searchFilters.yearFrom = '';
  searchFilters.yearTo = '';
  searchFilters.minRating = '';
}

// itemsById indexes the currently rendered library items by game_id so that a
// card click (or a #game/<id> deep link) can open the edit modal with the
// item's existing status/rating/playtime/notes without an extra API round-trip.
const itemsById = new Map();

// searchResultsById maps result id to result object for search mode
const searchResultsById = new Map();

// ownedIds holds game IDs confirmed (via /api/library/check) to be in the
// user's library while in search mode, so cards can be badged and clicks can
// open the edit form instead of the destructive add form.
const ownedIds = new Set();

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
// page bootstrap.
export function activateStatusTab(status) {
  const statusTabs = document.getElementById('statusTabs');
  if (!statusTabs) return;
  statusTabs.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
  const tab = statusTabs.querySelector(`.tab[data-status="${status || ''}"]`);
  if (tab) {
    tab.classList.add('active');
  } else {
    const allTab = statusTabs.querySelector('.tab[data-status=""]');
    if (allTab) allTab.classList.add('active');
  }
}

export async function loadSearchResults(query) {
  const grid = document.getElementById('gameGrid');
  if (!grid) return;

  resetSearchFilters(query);

  // Reset pagination state to search mode
  paginationState = {
    currentStatus: '',
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

  // Hide status tabs
  const statusTabs = document.getElementById('statusTabs');
  if (statusTabs) statusTabs.style.display = 'none';
  hideHero();

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

  const container = document.querySelector('.container');
  const searchWrap = document.querySelector('.search-wrap');
  container.insertBefore(header, searchWrap.nextSibling);

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
    updateSearchTotal(total);
    // Learn which results are already in the library so cards get a badge
    // and clicks open the edit form instead of a destructive "Add" form.
    ownedIds.clear();
    const ids = results.map(r => r.id);
    for (const id of await library.check(ids)) {
      ownedIds.add(Number(id));
    }
    renderPagedItems(grid, results, true);
  } catch (err) {
    paginationState.loading = false;
    grid.innerHTML = `<div class="empty-state">Failed to load results: ${err.message}</div>`;
  }
}

// filterParams maps searchFilters onto the API's query params.
function filterParams() {
  const p = {};
  if (searchFilters.sort) p.sort = searchFilters.sort;
  if (searchFilters.yearFrom) p.yearFrom = Number(searchFilters.yearFrom);
  if (searchFilters.yearTo) p.yearTo = Number(searchFilters.yearTo);
  if (searchFilters.minRating) p.minRating = Number(searchFilters.minRating);
  return p;
}

function updateSearchTotal(total) {
  const el = document.getElementById('searchTotal');
  if (!el || !Number.isFinite(total)) return;
  el.textContent = total === 1 ? ' · 1 game' : ` · ${total} games`;
}

// buildSearchFilterBarHTML renders the collapsible sort/filter controls.
function buildSearchFilterBarHTML() {
  return `
    <details class="search-filterbar">
      <summary>Sort &amp; filter</summary>
      <div class="sf-controls">
        <label>Sort
          <select id="sfSort">
            <option value="">Relevance</option>
            <option value="release_new">Newest</option>
            <option value="release_old">Oldest</option>
            <option value="rating">Rating</option>
            <option value="popularity">Popularity</option>
            <option value="name">Name A–Z</option>
          </select>
        </label>
        <label>Year from
          <input id="sfYearFrom" type="number" min="1900" max="2100" inputmode="numeric" placeholder="1994">
        </label>
        <label>to
          <input id="sfYearTo" type="number" min="1900" max="2100" inputmode="numeric" placeholder="2024">
        </label>
        <label>Min rating
          <select id="sfMinRating">
            <option value="">Any</option>
            <option value="60">60+</option>
            <option value="75">75+</option>
            <option value="85">85+</option>
            <option value="95">95+</option>
          </select>
        </label>
        <button id="sfApply" class="btn btn-primary btn-inline" type="button">Apply</button>
        <button id="sfClear" class="btn btn-secondary btn-inline" type="button">Clear</button>
      </div>
    </details>`;
}

// wireSearchFilterBar restores control values and applies changes. Applying
// re-runs the whole search (page 1, fresh totals); Clear resets to defaults.
function wireSearchFilterBar(header, query) {
  const sortSel = header.querySelector('#sfSort');
  const yearFrom = header.querySelector('#sfYearFrom');
  const yearTo = header.querySelector('#sfYearTo');
  const minRating = header.querySelector('#sfMinRating');
  if (!sortSel) return;

  sortSel.value = searchFilters.sort;
  yearFrom.value = searchFilters.yearFrom;
  yearTo.value = searchFilters.yearTo;
  minRating.value = searchFilters.minRating;

  const apply = () => {
    const clampYear = (v) => {
      const n = parseInt(v, 10);
      return Number.isFinite(n) && n >= 1900 && n <= 2100 ? String(n) : '';
    };
    searchFilters.sort = sortSel.value;
    searchFilters.yearFrom = clampYear(yearFrom.value);
    searchFilters.yearTo = clampYear(yearTo.value);
    searchFilters.minRating = minRating.value;
    loadSearchResults(query);
  };
  header.querySelector('#sfApply').addEventListener('click', apply);
  header.querySelector('#sfClear').addEventListener('click', () => {
    sortSel.value = '';
    yearFrom.value = '';
    yearTo.value = '';
    minRating.value = '';
    apply();
  });
}

export async function loadLibrary(status, tag = '', platform = '') {
  const grid = document.getElementById('gameGrid');
  if (!grid) return;

  // Reset pagination state
  paginationState = {
    currentStatus: status || '',
    tagFilter: tag || '',
    platformFilter: platform || '',
    offset: 0,
    // Held true across the fetch below so a scroll event landing in this
    // window can't trigger a concurrent loadMore() that appends the same
    // page twice (duplicated cards). renderPagedItems clears it once done.
    loading: true,
    hasMore: true,
    pageSize: PAGE_SIZE,
    mode: 'library',
    searchQuery: '',
  };

  // Hide search results header and show tabs
  const searchHeader = document.getElementById('searchResultsHeader');
  if (searchHeader) searchHeader.remove();
  const statusTabs = document.getElementById('statusTabs');
  if (statusTabs) statusTabs.style.display = '';
  activateStatusTab(status || '');
  updateTagFilterBar();
  loadHero();

  grid.innerHTML = '<div class="loading">Loading library...</div>';

  try {
    const { items, total, hasMore } = await library.list(status || '', PAGE_SIZE, 0, tag || '', platform || '');
    renderPagedItems(grid, items, true, hasMore);
    refreshTabCounts();
  } catch (err) {
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

  paginationState = {
    currentStatus: status || '',
    tagFilter: '',
    platformFilter: '',
    offset: 0,
    loading: false,
    hasMore: true,
    pageSize: PAGE_SIZE,
    mode: 'library',
    searchQuery: '',
  };

  loadHero();

  if (!items || items.length === 0) {
    renderEmptyState(grid, 'library', status);
    paginationState.hasMore = false;
    loadHero();
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

  const grid = document.getElementById('gameGrid');
  if (!grid) return;

  try {
    if (paginationState.mode === 'search') {
      const { results } = await searchGamesFull(paginationState.searchQuery, {
        limit: paginationState.pageSize,
        offset: paginationState.offset,
        ...filterParams(),
      });
      renderPagedItems(grid, results, false); // false = append, not replace
    } else {
      const { items, hasMore } = await library.list(
        paginationState.currentStatus,
        paginationState.pageSize,
        paginationState.offset,
        paginationState.tagFilter,
        paginationState.platformFilter
      );
      renderPagedItems(grid, items, false, hasMore);
    }
  } catch (err) {
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
// labels ("Backlog (43)"), plus the lifetime stats strip.
export async function refreshTabCounts() {
  const statusTabs = document.getElementById('statusTabs');
  if (!statusTabs) return;
  const counts = await library.counts();
  if (!counts) return;
  statusTabs.querySelectorAll('.tab').forEach(tab => {
    const status = tab.dataset.status || '';
    const n = counts[status || 'all'];
    const label = tab.dataset.label || (tab.dataset.label = tab.textContent.replace(/\s\(\d+\)$/, ''));
    tab.textContent = typeof n === 'number' ? `${label} (${n})` : label;
  });
  updateStatsStrip(counts);
}

// updateStatsStrip renders the one-line summary ("12 games · 4 finished ·
// ~130h logged") under the tabs. Hidden entirely on an empty library.
function updateStatsStrip(counts) {
  const el = document.getElementById('statsStrip');
  if (!el) return;
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
      <img src="${getCoverURL(g)}" alt="${escapeHTML(g.name)}" loading="lazy" decoding="async">
      <span class="suggest-name">${escapeHTML(g.name)}</span>
      <span class="suggest-add">+</span>
    </button>`).join('');

  grid.querySelectorAll('.suggest-card').forEach(card => {
    card.addEventListener('click', async () => {
      const id = Number(card.dataset.suggestId);
      const name = card.dataset.suggestName;
      const activeTab = document.querySelector('.tab.active');
      card.disabled = true;
      try {
        await library.add(id, { status: 'backlog' });
        showToast(`Added ${name} to Backlog`);
        // Reload the view: if this was the first game the empty state gives
        // way to the real grid; otherwise fresh suggestions exclude it.
        await loadLibrary(activeTab?.dataset?.status || '', paginationState.tagFilter);
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

  // Delegated click handlers for interactive bits on cards. Delegation (not
  // per-card listeners) survives the cloneNode rebinding in attachCardEvents.
  const grid = document.getElementById('gameGrid');
  if (grid) {
    grid.addEventListener('click', (e) => {
      const qaBtn = e.target.closest('.qa-btn');
      if (qaBtn) {
        e.stopPropagation();
        e.preventDefault();
        const card = qaBtn.closest('.game-card');
        if (card && qaBtn.dataset.qa) {
          quickSetStatus(card.dataset.gameId, qaBtn.dataset.qa, { card });
        }
        return;
      }
      const chip = e.target.closest('.tag-chip');
      if (chip && chip.dataset.tag) {
        e.stopPropagation();
        filterByTag(chip.dataset.tag);
      }
    });
  }
}

// quickSetStatus patches a library item's status in place: optimistic badge
// update, PATCH on success of which the item index is refreshed; on failure
// the badge reverts. When the active tab filter no longer matches the new
// status the card animates out.
async function quickSetStatus(gameId, qaAction, { card = null } = {}) {
  const key = String(gameId);
  const item = itemsById.get(key);
  const newStatus = qaAction === '__bought' ? 'backlog' : qaAction;
  if (!item || !VALID_STATUSES.includes(newStatus)) return;

  card = card || document.querySelector(`.game-card[data-game-id="${key}"]`);
  const oldStatus = item.status;
  const oldBadgeHTML = card ? card.querySelector('.card-status')?.outerHTML : '';
  const oldQaHTML = card ? card.querySelector('.qa-bar')?.outerHTML : '';

  // Optimistic update
  applyStatusToCard(card, newStatus, null);
  item.status = newStatus;

  try {
    const updated = await library.patch(gameId, { status: newStatus });
    itemsById.set(key, updated);
    applyStatusToCard(card, updated.status, updated.completed_at);
    refreshTabCounts();
    loadHero();
    if (qaAction === '__bought') {
      showToast(`Bought ${updated.game_name} — moved to Backlog`);
    } else {
      showToast(newStatus === 'completed' ? `Finished ${updated.game_name} ✓` : `Marked ${updated.game_name} as ${STATUS_LABELS[updated.status]}`);
    }

    // If the active tab no longer contains this game, animate it out.
    const activeTab = document.querySelector('.tab.active');
    const activeStatus = activeTab?.dataset?.status || '';
    if (activeStatus && activeStatus !== updated.status && paginationState.mode === 'library' && !paginationState.tagFilter) {
      removeCardAnimated(card);
      itemsById.delete(key);
    }
  } catch (err) {
    item.status = oldStatus;
    if (card) {
      if (oldBadgeHTML) card.querySelector('.card-status')?.replaceWith(htmlToEl(oldBadgeHTML));
      else applyStatusToCard(card, oldStatus, item.completed_at);
      const qaWrap = card.querySelector('.qa-bar');
      if (qaWrap) qaWrap.remove();
      if (oldQaHTML) card.appendChild(htmlToEl(oldQaHTML));
    }
    showToast(`Couldn't update ${item.game_name}: ${err.message}`, { type: 'error' });
  }
}

// applyStatusToCard re-renders a card's status pill and quick-action bar for
// the given status. completedAt (when known) feeds the "✓ YYYY" text.
function applyStatusToCard(card, status, completedAt) {
  if (!card) return;
  const isDone = status === 'completed';
  const badgeText = isDone && completedAt
    ? `✓ ${formatYear(completedAt)}`
    : STATUS_LABELS[status] || status;

  let badge = card.querySelector('.card-status');
  if (!badge) {
    badge = document.createElement('div');
    card.insertBefore(badge, card.querySelector('.card-title'));
  }
  badge.className = `card-status status-${status}`;
  badge.textContent = badgeText;
  badge.title = '';

  let qaBar = card.querySelector('.qa-bar');
  const actions = quickActionsFor(status);
  if (!qaBar) {
    qaBar = document.createElement('div');
    qaBar.className = 'qa-bar';
    card.appendChild(qaBar);
  }
  qaBar.innerHTML = actions.join('');
  qaBar.style.display = actions.length ? '' : 'none';
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

// --- "Playing now" hero strip -------------------------------------------
// The most valuable surface of a progress tracker: what you're playing right
// now, with one-tap time logging (+30m/+1h/+2h) and a Finish button. Backed
// entirely by PATCH merge updates, so nothing else about an item can be lost.

const heroItems = new Map();

function fmtHours(minutes) {
  const h = Math.round((minutes / 60) * 10) / 10;
  return h === 1 ? '1h' : `${h}h`;
}

function fmtDelta(minutes) {
  return minutes % 60 === 0 ? `${minutes / 60}h` : `${minutes}m`;
}

// ensureHeroContainer creates/returns the hero section positioned between
// the search box and the status tabs.
function ensureHeroContainer() {
  let hero = document.getElementById('playingHero');
  if (hero) return hero;
  hero = document.createElement('section');
  hero.id = 'playingHero';
  hero.className = 'playing-hero';
  hero.setAttribute('aria-label', 'Currently playing');
  const searchWrap = document.querySelector('.search-wrap');
  const container = document.querySelector('.container');
  if (searchWrap) searchWrap.insertAdjacentElement('afterend', hero);
  else if (container) container.prepend(hero);
  return hero;
}

export async function loadHero() {
  const hero = ensureHeroContainer();
  let items;
  try {
    ({ items } = await library.list('playing', 12, 0));
  } catch {
    hero.style.display = 'none';
    return;
  }
  heroItems.clear();
  for (const it of items || []) heroItems.set(String(it.game_id), it);

  if (!items || items.length === 0 || paginationState.mode === 'search') {
    hero.style.display = 'none';
    return;
  }

  hero.innerHTML = `
    <div class="hero-title">Playing now</div>
    <div class="hero-row">
      ${items.map(it => heroCardHTML(it)).join('')}
    </div>`;
  hero.style.display = '';
}

function heroCardHTML(item) {
  const bits = [];
  if (item.playtime_minutes > 0) bits.push(fmtHours(item.playtime_minutes));
  if (item.started_at) {
    const days = Math.max(1, Math.floor((Date.now() - new Date(item.started_at).getTime()) / 86400000) + 1);
    bits.push(`day ${days}`);
  }
  return `
    <div class="hero-card" data-game-id="${item.game_id}">
      <img src="${getCoverURL(item)}" alt="${escapeHTML(item.game_name)}" loading="lazy" decoding="async">
      <div class="hero-info">
        <div class="hero-name">${escapeHTML(item.game_name)}</div>
        <div class="hero-sub">${escapeHTML(bits.join(' · '))}</div>
      </div>
      <div class="hero-actions">
        <button type="button" class="hero-btn" data-hero-time="30" title="Log 30 minutes">+30m</button>
        <button type="button" class="hero-btn" data-hero-time="60" title="Log 1 hour">+1h</button>
        <button type="button" class="hero-btn" data-hero-time="120" title="Log 2 hours">+2h</button>
        <button type="button" class="hero-btn hero-finish" data-hero-finish title="Mark as finished">✓ Finished</button>
      </div>
    </div>`;
}

// initHeroActions attaches the delegated handlers once.
let heroInitialized = false;

export function initHeroActions() {
  if (heroInitialized) return;
  heroInitialized = true;
  const hero = ensureHeroContainer();
  hero.addEventListener('click', async (e) => {
    const timeBtn = e.target.closest('[data-hero-time]');
    const finishBtn = e.target.closest('[data-hero-finish]');
    if (!timeBtn && !finishBtn) return;
    const cardEl = (timeBtn || finishBtn).closest('.hero-card');
    const gameId = Number(cardEl?.dataset.gameId);
    if (!gameId) return;

    if (timeBtn) {
      const minutes = parseInt(timeBtn.dataset.heroTime, 10) || 0;
      timeBtn.disabled = true;
      try {
        const updated = await library.patch(gameId, { playtime_delta_minutes: minutes });
        heroItems.set(String(updated.game_id), updated);
        const sub = cardEl.querySelector('.hero-sub');
        const item = updated;
        const bits = [];
        if (item.playtime_minutes > 0) bits.push(fmtHours(item.playtime_minutes));
        if (item.started_at) {
          const days = Math.max(1, Math.floor((Date.now() - new Date(item.started_at).getTime()) / 86400000) + 1);
          bits.push(`day ${days}`);
        }
        if (sub) sub.textContent = bits.join(' · ');
        const gridItem = itemsById.get(String(gameId));
        if (gridItem) itemsById.set(String(gameId), { ...gridItem, playtime_minutes: item.playtime_minutes });
        showToast(`+${fmtDelta(minutes)} logged for ${updated.game_name}`);
      } catch (err) {
        showToast(`Couldn't log time: ${err.message}`, { type: 'error' });
      } finally {
        timeBtn.disabled = false;
      }
      return;
    }

    // Finish: same engine as the card quick actions (updates grid card,
    // tab counts, and this strip).
    quickSetStatus(gameId, 'completed');
    cardEl.classList.add('card-removing');
    setTimeout(() => { cardEl.remove(); maybeHideHero(); }, 260);
  });
}

function maybeHideHero() {
  const hero = document.getElementById('playingHero');
  if (!hero) return;
  if (hero.querySelectorAll('.hero-card').length === 0) {
    hero.style.display = 'none';
    loadHero();
  }
}

// hideHero hides the strip while browsing search results.
export function hideHero() {
  const hero = document.getElementById('playingHero');
  if (hero) hero.style.display = 'none';
}

// filterByTag sets the tag filter and reloads the library.
// Called when a tag chip on a card is clicked.
// If a filter is already active, adds the tag with AND (space-separated).
export function filterByTag(tag) {
  const activeTab = document.querySelector('.tab.active');
  const current = paginationState.tagFilter;
  const newFilter = current ? current + ' ' + formatTagForQuery(tag) : formatTagForQuery(tag);
  loadLibrary(activeTab?.dataset?.status || '', newFilter);
}

// applyTagFilter replaces the active tag filter outright and reloads.
// Used by the game-form tag menu, where the exact combination (replace,
// AND, OR) is chosen explicitly rather than always appended.
export function applyTagFilter(filter) {
  const activeTab = document.querySelector('.tab.active');
  loadLibrary(activeTab?.dataset?.status || '', filter);
}

// clearTagFilter removes the tag filter and reloads (platform filter kept).
export function clearTagFilter() {
  const activeTab = document.querySelector('.tab.active');
  loadLibrary(activeTab?.dataset?.status || '', '', paginationState.platformFilter);
}

// clearAllFilters removes both tag and platform filters and reloads.
export function clearAllFilters() {
  const activeTab = document.querySelector('.tab.active');
  loadLibrary(activeTab?.dataset?.status || '', '', '');
}

// filterByPlatform sets the platform availability filter and reloads.
// Typing the same platform again clears it (toggle), mirroring tag chips.
export function filterByPlatform(platform) {
  const activeTab = document.querySelector('.tab.active');
  const next = paginationState.platformFilter === platform ? '' : platform;
  loadLibrary(activeTab?.dataset?.status || '', paginationState.tagFilter, next);
}

// updateTagFilterBar shows or hides the "filtered by" bar, covering both the
// tag filter ($ chips) and the platform availability filter (@ chip).
function updateTagFilterBar() {
  const existing = document.getElementById('tagFilterBar');
  if (existing) existing.remove();

  const tag = paginationState.tagFilter;
  const platform = paginationState.platformFilter;
  if (!tag && !platform) return;

  const { tags: tagList, op } = parseTagQuery(tag || '');
  let display = '';
  if (tag) {
    const joiner = op === 'or' ? ' OR ' : ' AND ';
    display += tagList.map(t => `<span class="tag-filter-chip" data-tag="${escapeHTML(t)}">${escapeHTML(t)}<button type="button" class="tag-filter-chip-x" aria-label="Remove ${escapeHTML(t)}">×</button></span>`).join(joiner);
  }
  if (platform) {
    display += `<span class="tag-filter-chip tag-filter-platform" data-platform="${escapeHTML(platform)}">@${escapeHTML(platform)}<button type="button" class="tag-filter-chip-x" aria-label="Remove platform filter">×</button></span>`;
  }

  const container = document.querySelector('.container');
  const statusTabs = document.getElementById('statusTabs');
  const bar = document.createElement('div');
  bar.id = 'tagFilterBar';
  bar.className = 'tag-filter-bar';
  bar.innerHTML = `
    <span class="tag-filter-label">${display}</span>
    <button class="tag-filter-clear" type="button" aria-label="Clear filters">✕</button>
  `;
  bar.querySelector('.tag-filter-clear').addEventListener('click', clearAllFilters);

  // Clicking an individual chip's × removes just that filter: a tag chip
  // drops that tag (keeping others and the platform), the platform chip
  // toggles the platform off.
  bar.querySelectorAll('.tag-filter-chip-x').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      const chip = btn.parentElement;
      const activeTab = document.querySelector('.tab.active');
      const status = activeTab?.dataset?.status || '';
      if (chip.dataset.platform !== undefined) {
        loadLibrary(status, paginationState.tagFilter, '');
        return;
      }
      const removeTag = chip.dataset.tag;
      const remaining = tagList.filter(t => t !== removeTag);
      const newFilter = remaining.map(formatTagForQuery).join(op === 'or' ? '|' : ' ');
      loadLibrary(status, newFilter, paginationState.platformFilter);
    });
  });

  // Insert after status tabs (or after search header, or after search wrap)
  const after = statusTabs && statusTabs.style.display !== 'none' ? statusTabs : document.getElementById('searchResultsHeader') || document.querySelector('.search-wrap');
  after.insertAdjacentElement('afterend', bar);
}

// formatPlatformName shortens IGDB platform names for compact display:
// "PC (Microsoft Windows)" → "PC", "Xbox Series X|S" unchanged.
export function formatPlatformName(name) {
  const raw = String(name || '').trim();
  const short = raw.replace(/\s*\([^)]*\)/g, '').trim();
  return short || raw;
}

function platformsLine(platforms, max = 3) {
  const names = (platforms || []).map(formatPlatformName).filter(Boolean);
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
    const ownedBadge = isSearch && ownedIds.has(Number(item.game_id))
      ? '<div class="owned-badge">In library ✓</div>'
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

      // Contextual quick actions (hover-reveal; always visible on touch).
      const actions = quickActionsFor(item.status);
      if (actions.length) {
        libraryOverlays += `<div class="qa-bar">${actions.join('')}</div>`;
      }
    }

    // Ownership info occupies the tag-chip row when there are no tags.
    const ownBits = [item.platform, item.medium].filter(Boolean).join(' · ');
    const ownMetaHTML = (!isSearch && !tagsHTML && ownBits)
      ? `<div class="card-tags"><span class="tag-chip own-meta-chip">${escapeHTML(ownBits)}</span></div>`
      : '';

    // Available platforms (IGDB data) — shown for library and search cards
    // so it's clear what a game can be played on before/after logging.
    const platLine = platformsLine(item.platforms);
    const platformsHTML = platLine
      ? `<div class="card-platforms" title="${escapeHTML((item.platforms || []).join(', '))}">${escapeHTML(platLine)}</div>`
      : '';

    return `
    <div class="game-card" data-game-id="${item.game_id}">
      <img src="${getCoverURL(item)}" alt="${escapeHTML(item.game_name)}" loading="lazy" decoding="async"${priority}>
      ${libraryOverlays}
      <div class="card-title">${escapeHTML(item.game_name)}</div>
      ${platformsHTML}
      ${tagsHTML || ownMetaHTML}
      ${ownedBadge}
      ${item.rating > 0 ? `<div class="rating-display">${Number(item.rating)}</div>` : ''}
    </div>
  `}).join('');
}

// quickActionsFor builds the contextual quick-action buttons for a status.
// The special value "__bought" moves a wishlist game into the backlog.
function quickActionsFor(status) {
  const actions = [];
  if (status === 'wishlist') {
    actions.push('<button type="button" class="qa-btn qa-bought" data-qa="__bought" title="Bought it!" aria-label="Mark as bought">$</button>');
  }
  if (status !== 'playing' && status !== 'wishlist') {
    actions.push('<button type="button" class="qa-btn qa-play" data-qa="playing" title="Mark as playing" aria-label="Mark as playing">▶</button>');
  } else if (status === 'wishlist') {
    actions.push('<button type="button" class="qa-btn qa-play" data-qa="playing" title="Started playing" aria-label="Start playing">▶</button>');
  }
  if (status !== 'completed') {
    actions.push('<button type="button" class="qa-btn qa-done" data-qa="completed" title="Mark as finished" aria-label="Mark as finished">✓</button>');
  }
  return actions;
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
      // Quick-action buttons and tag chips are handled by the delegated grid
      // listener; don't also open the edit modal.
      if (e.target.closest('.qa-btn') || e.target.closest('.tag-chip')) return;
      const gameId = card.dataset.gameId;

      if (paginationState.mode === 'search') {
        // In search mode, use original search result
        const result = searchResultsById.get(gameId);
        if (!result) return;
        if (ownedIds.has(Number(gameId))) {
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
    year: game.first_release_date
      ? new Date(game.first_release_date * 1000).getFullYear()
      : '',
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
    year: item.first_release_date
      ? new Date(item.first_release_date * 1000).getFullYear()
      : '',
    status: item.status,
    rating: item.rating || 0,
    playtime: item.playtime_minutes || 0,
    tags: item.tags || [],
    notes: item.notes || '',
    createdAt: item.created_at || '',
    startedAt: item.started_at || null,
    completedAt: item.completed_at || null,
    platform: item.platform || '',
    medium: item.medium || '',
    platforms: item.platforms || [],
    inLibrary: true,
  });
}

// openGameForm renders the routable game popup (#game/<id>). In "add" mode it
// shows a blank form with an "Add to Library" action; in "edit" mode
// (inLibrary=true) it is pre-filled and offers Save and Remove. Both actions
// POST to library.add, which upserts.
function openGameForm({ id, name, cover, year = '', status = 'backlog',
                        rating = 0, playtime = 0, tags = [], notes = '',
                        createdAt = '', startedAt = null, completedAt = null,
                        platform = '', medium = '', platforms = [],
                        inLibrary = false }) {
  // Replace any existing modal (e.g. user clicks a second result).
  const existing = document.getElementById('addGameModal');
  if (existing) existing.remove();

  const title = inLibrary ? 'Edit Library Entry' : 'Add to Library';
  const hours = Math.round((playtime / 60) * 100) / 100;

  // Dates are server-managed (auto-set on entering playing/completed) but
  // correctable: started/completed are editable and only sent when changed,
  // so an untouched save never disturbs them. Empty + sent = explicit clear.
  const toInputValue = (iso) => (iso ? String(iso).slice(0, 10) : '');
  const initialDates = {
    started: toInputValue(startedAt),
    completed: toInputValue(completedAt),
  };
  const pendingDateChanges = { started: undefined, completed: undefined };
  const dateRow = (label, value, key, editable) => editable ? `
    <div class="modal-date-row">
      <span class="modal-date-label">${label}</span>
      <input type="date" class="modal-date-input" data-datekey="${key}" value="${escapeHTML(value)}">
    </div>` : `
    <div class="modal-date-row">
      <span class="modal-date-label">${label}</span>
      <span class="modal-date-value">${value ? escapeHTML(formatDate(value)) : '—'}</span>
    </div>`;
  const datesHTML = inLibrary ? `
        <div class="modal-field modal-dates">
          ${dateRow('Added', createdAt, 'added', false)}
          ${dateRow('Started', initialDates.started, 'started', true)}
          ${dateRow('Completed', initialDates.completed, 'completed', true)}
          <div class="modal-dates-hint">Start/finish dates fill in automatically when you change status.</div>
        </div>` : '';

  // Ownership is chosen directly on the available-platform chips: the
  // highlighted chip IS the "Owned on" value (tap again to clear). A stored
  // free-text platform that matches no IGDB chip still shows as its own
  // selectable chip so existing data stays visible and clearable.
  let selectedPlatform = String(platform || '').trim();
  const norm = (s) => s.toLowerCase();
  const availPlatforms = (platforms || []).map(p => String(p).trim()).filter(Boolean);
  const chipHTML = (p) => `<button type="button" class="plat-chip${norm(selectedPlatform) === norm(p) ? ' selected' : ''}" data-full="${escapeHTML(p)}" title="${escapeHTML(p)}">${escapeHTML(formatPlatformName(p))}</button>`;
  const extraOwned = selectedPlatform && !availPlatforms.some(p => norm(p) === norm(selectedPlatform));
  const availChipsHTML = (availPlatforms.length || extraOwned) ? `
        <div class="modal-avail">
          <span class="modal-avail-label">Available on <span class="modal-avail-hint">(highlighted = owned)</span></span>
          <div class="modal-avail-chips">
            ${availPlatforms.slice(0, 8).map(chipHTML).join('')}
            ${availPlatforms.length > 8 ? `<span class="plat-more" title="${escapeHTML(availPlatforms.join(', '))}">+${availPlatforms.length - 8} more</span>` : ''}
            ${extraOwned ? chipHTML(selectedPlatform) : ''}
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
      <div class="modal-header">
        <h2 id="addGameModalTitle">${title}</h2>
        <button class="modal-close" type="button" aria-label="Close">&times;</button>
      </div>
      <div class="modal-body">
        <div class="modal-game-info">
          <img src="${cover}" alt="${escapeHTML(name)}" decoding="async">
          <div class="modal-game-meta">
            <h3>${escapeHTML(name)}</h3>
            ${year ? `<div class="modal-year">${Number(year)}</div>` : ''}
          </div>
        </div>
        ${datesHTML}
        <label class="modal-field">Status
          <select class="modal-status">
            ${VALID_STATUSES.map(s => `
              <option value="${s}"${s === status ? ' selected' : ''}>${STATUS_LABELS[s]}</option>
            `).join('')}
          </select>
        </label>
        <label class="modal-field">Rating: <span class="modal-rating-val">${Number(rating)}</span>
          <input type="range" min="0" max="100" value="${Number(rating)}" class="modal-rating">
        </label>
        <label class="modal-field">Hours:
          <input type="number" min="0" step="0.25" value="${hours}" class="modal-playtime" inputmode="decimal">
        </label>
        <div class="modal-field">Tags
          <div class="modal-tags-wrap">
            <div class="modal-tags-chips">${tags.map(t => `<span class="tag-chip tag-chip-removable" data-tag="${escapeHTML(t)}">${escapeHTML(t)}<button type="button" class="tag-chip-x" aria-label="Remove ${escapeHTML(t)}">&times;</button></span>`).join('')}</div>
            <input type="text" class="modal-tags-input" placeholder="Add tag...">
          </div>
        </div>
        <label class="modal-field">Notes
          <textarea class="modal-notes" placeholder="Notes...">${escapeHTML(notes)}</textarea>
        </label>
        <div class="modal-ownership">
          ${availChipsHTML}
          <label class="modal-field">Format
            <select class="modal-medium">
              <option value=""${!medium ? ' selected' : ''}>—</option>
              <option value="physical"${medium === 'physical' ? ' selected' : ''}>Physical</option>
              <option value="digital"${medium === 'digital' ? ' selected' : ''}>Digital</option>
            </select>
          </label>
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

  const collectPayload = () => {
    const newHours = parseFloat(modal.querySelector('.modal-playtime').value) || 0;
    const payload = {
      status: modal.querySelector('.modal-status').value,
      rating: parseInt(modal.querySelector('.modal-rating').value) || 0,
      playtime_minutes: Math.max(0, Math.round(newHours * 60)),
      tags: Array.from(modal.querySelectorAll('.modal-tags-chips .tag-chip-removable'))
        .map(c => c.firstChild.textContent.trim()),
      notes: modal.querySelector('.modal-notes').value,
      platform: selectedPlatform,
      medium: modal.querySelector('.modal-medium').value,
    };
    // Explicit date changes only — omitting the fields preserves existing
    // values (and keeps server-side auto-tracking working).
    if (inLibrary && pendingDateChanges.started !== undefined) payload.started_at = pendingDateChanges.started;
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

  // Live-update the rating preview label. Playtime is entered directly in
  // hours (converted to minutes on save) — no more "type 2, see 0.0".
  modal.querySelector('.modal-rating').addEventListener('input', (e) => {
    modal.querySelector('.modal-rating-val').textContent = e.target.value;
    scheduleSave();
  });
  modal.querySelector('.modal-status').addEventListener('change', scheduleSave);
  modal.querySelector('.modal-medium').addEventListener('change', scheduleSave);
  modal.querySelector('.modal-playtime').addEventListener('input', scheduleSave);
  modal.querySelector('.modal-notes').addEventListener('input', scheduleSave);

  // Tap a platform chip to select it as the owned platform; tap again to
  // clear. Single-select — the highlighted chip is what gets saved.
  modal.querySelectorAll('.plat-chip').forEach(chip => {
    chip.addEventListener('click', () => {
      if (norm(selectedPlatform) === norm(chip.dataset.full)) {
        selectedPlatform = '';
        chip.classList.remove('selected');
      } else {
        selectedPlatform = chip.dataset.full;
        modal.querySelectorAll('.plat-chip.selected').forEach(c => c.classList.remove('selected'));
        chip.classList.add('selected');
      }
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

  // Tag chip input
  const chipsContainer = modal.querySelector('.modal-tags-chips');
  const tagInput = modal.querySelector('.modal-tags-input');

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
    const shouldRefresh = savedAtLeastOnce && paginationState.mode !== 'search';
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
    // Reflect auto-saved edits in the grid/hero once, on the way out.
    if (shouldRefresh) {
      const activeTab = document.querySelector('.tab.active');
      loadLibrary(activeTab?.dataset?.status || '', paginationState.tagFilter, paginationState.platformFilter);
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
        platform,
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
        const activeTab = document.querySelector('.tab.active');
        await loadLibrary(activeTab?.dataset?.status || '', paginationState.tagFilter, paginationState.platformFilter);
        loadHero();
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
              const tab = document.querySelector('.tab.active');
              await loadLibrary(tab?.dataset?.status || '', paginationState.tagFilter, paginationState.platformFilter);
              loadHero();
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
// contexts — a verified stored-XSS vector (FINDINGS §1.5).
export function escapeHTML(str) {
  return String(str ?? '').replace(/[&<>"']/g, c => ({
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&#39;',
  }[c]));
}
