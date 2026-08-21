import { library, getCoverURL, getGame, searchGamesFull } from './api.js';
import { formatDate, formatYear } from './dates.js';

const VALID_STATUSES = ['wishlist', 'backlog', 'playing', 'completed', 'abandoned'];

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
  offset: 0,
  loading: false,
  hasMore: true,
  pageSize: PAGE_SIZE,
  mode: 'library', // 'library' or 'search'
  searchQuery: '',
};

let scrollListenerAttached = false;

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

  // Reset pagination state to search mode
  paginationState = {
    currentStatus: '',
    tagFilter: '',
    offset: 0,
    loading: false,
    hasMore: true,
    pageSize: SEARCH_PAGE_SIZE,
    mode: 'search',
    searchQuery: query,
  };

  // Hide status tabs
  const statusTabs = document.getElementById('statusTabs');
  if (statusTabs) statusTabs.style.display = 'none';

  // Create and show results header
  const existingHeader = document.getElementById('searchResultsHeader');
  if (existingHeader) existingHeader.remove();

  const header = document.createElement('div');
  header.id = 'searchResultsHeader';
  header.className = 'search-results-header';
  header.innerHTML = `
    <div class="search-results-header-content">
      <div class="search-results-title">Results for "<span id="searchQueryDisplay">${escapeHTML(query)}</span>"</div>
      <button class="search-results-clear" aria-label="Clear search" type="button">✕</button>
    </div>
  `;

  const container = document.querySelector('.container');
  const searchWrap = document.querySelector('.search-wrap');
  container.insertBefore(header, searchWrap.nextSibling);

  const clearBtn = header.querySelector('.search-results-clear');
  clearBtn.addEventListener('click', () => {
    history.replaceState(null, '', window.location.pathname + window.location.search);
    loadLibrary('');
  });

  grid.innerHTML = '<div class="loading">Loading results...</div>';

  try {
    const results = await searchGamesFull(query, {
      limit: SEARCH_PAGE_SIZE,
      offset: 0,
    });
    // Learn which results are already in the library so cards get a badge
    // and clicks open the edit form instead of a destructive "Add" form.
    ownedIds.clear();
    const ids = results.map(r => r.id);
    for (const id of await library.check(ids)) {
      ownedIds.add(Number(id));
    }
    renderPagedItems(grid, results, true);
  } catch (err) {
    grid.innerHTML = `<div class="empty-state">Failed to load results: ${err.message}</div>`;
  }
}

export async function loadLibrary(status, tag = '') {
  const grid = document.getElementById('gameGrid');
  if (!grid) return;

  // Reset pagination state
  paginationState = {
    currentStatus: status || '',
    tagFilter: tag || '',
    offset: 0,
    loading: false,
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

  grid.innerHTML = '<div class="loading">Loading library...</div>';

  try {
    const { items, total, hasMore } = await library.list(status || '', PAGE_SIZE, 0, tag || '');
    renderPagedItems(grid, items, true, hasMore);
    refreshTabCounts();
  } catch (err) {
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
      const emptyMsg = paginationState.mode === 'search'
        ? 'No games found.'
        : 'No games in this collection yet. Search above to add games.';
      grid.innerHTML = `<div class="empty-state">${emptyMsg}</div>`;
      paginationState.hasMore = false;
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
    offset: 0,
    loading: false,
    hasMore: true,
    pageSize: PAGE_SIZE,
    mode: 'library',
    searchQuery: '',
  };

  if (!items || items.length === 0) {
    grid.innerHTML = '<div class="empty-state">No games in this collection yet. Search above to add games.</div>';
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

  const grid = document.getElementById('gameGrid');
  if (!grid) return;

  try {
    if (paginationState.mode === 'search') {
      const items = await searchGamesFull(paginationState.searchQuery, {
        limit: paginationState.pageSize,
        offset: paginationState.offset,
      });
      renderPagedItems(grid, items, false); // false = append, not replace
    } else {
      const { items, hasMore } = await library.list(
        paginationState.currentStatus,
        paginationState.pageSize,
        paginationState.offset,
        paginationState.tagFilter
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
// labels ("Backlog (43)").
export async function refreshTabCounts() {
  const statusTabs = document.getElementById('statusTabs');
  if (!statusTabs) return;
  const counts = await library.counts();
  if (!counts) return;
  statusTabs.querySelectorAll('.tab').forEach(tab => {
    const status = tab.dataset.status || '';
    const n = counts[status || 'all'];
    const label = tab.dataset.label || (tab.dataset.label = tab.textContent);
    tab.textContent = typeof n === 'number' ? `${label} (${n})` : label;
  });
}

// showToast displays a transient confirmation message.
function showToast(message) {
  let toast = document.getElementById('toast');
  if (!toast) {
    toast = document.createElement('div');
    toast.id = 'toast';
    toast.setAttribute('role', 'status');
    document.body.appendChild(toast);
  }
  toast.textContent = message;
  toast.classList.add('visible');
  clearTimeout(showToast._timer);
  showToast._timer = setTimeout(() => toast.classList.remove('visible'), 2500);
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

  // Delegated click handler for tag chips on cards
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

// filterByTag sets the tag filter and reloads the library.
// Called when a tag chip on a card is clicked.
// If a filter is already active, adds the tag with AND (space-separated).
export function filterByTag(tag) {
  const activeTab = document.querySelector('.tab.active');
  const current = paginationState.tagFilter;
  const newFilter = current ? current + ' ' + tag : tag;
  loadLibrary(activeTab?.dataset?.status || '', newFilter);
}

// clearTagFilter removes the tag filter and reloads.
export function clearTagFilter() {
  const activeTab = document.querySelector('.tab.active');
  loadLibrary(activeTab?.dataset?.status || '', '');
}

// updateTagFilterBar shows or hides the "filtered by tag" bar.
function updateTagFilterBar() {
  const existing = document.getElementById('tagFilterBar');
  if (existing) existing.remove();

  const tag = paginationState.tagFilter;
  if (!tag) return;

  const hasPipe = tag.includes('|');
  const separator = hasPipe ? '|' : ' ';
  const tagList = tag.split(separator).map(t => t.trim()).filter(t => t.length > 0);
  const joiner = hasPipe ? ' OR ' : ' AND ';
  const display = tagList.map(t => `<span class="tag-filter-chip" data-tag="${escapeHTML(t)}">${escapeHTML(t)}<button type="button" class="tag-filter-chip-x" aria-label="Remove ${escapeHTML(t)}">×</button></span>`).join(joiner);

  const container = document.querySelector('.container');
  const statusTabs = document.getElementById('statusTabs');
  const bar = document.createElement('div');
  bar.id = 'tagFilterBar';
  bar.className = 'tag-filter-bar';
  bar.innerHTML = `
    <span class="tag-filter-label">${display}</span>
    <button class="tag-filter-clear" type="button" aria-label="Clear tag filter">✕</button>
  `;
  bar.querySelector('.tag-filter-clear').addEventListener('click', clearTagFilter);

  // Clicking an individual tag chip in the filter bar removes just that tag
  bar.querySelectorAll('.tag-filter-chip-x').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      const removeTag = btn.parentElement.dataset.tag;
      const remaining = tagList.filter(t => t !== removeTag);
      if (remaining.length === 0) {
        clearTagFilter();
      } else {
        const newFilter = hasPipe ? remaining.join('|') : remaining.join(' ');
        const activeTab = document.querySelector('.tab.active');
        loadLibrary(activeTab?.dataset?.status || '', newFilter);
      }
    });
  });

  // Insert after status tabs (or after search header, or after search wrap)
  const after = statusTabs && statusTabs.style.display !== 'none' ? statusTabs : document.getElementById('searchResultsHeader') || document.querySelector('.search-wrap');
  after.insertAdjacentElement('afterend', bar);
}

function buildCardHTML(items) {
  const isSearch = paginationState.mode === 'search';
  return items.map((item, index) => {
    // High priority for the first 8 cards
    const priority = index < 8 ? ' fetchpriority="high"' : '';
    const tagsHTML = (item.tags && item.tags.length)
      ? `<div class="card-tags">${item.tags.map(t => `<span class="tag-chip" data-tag="${escapeHTML(t)}">${escapeHTML(t)}</span>`).join('')}</div>`
      : '';
    const completedBadge = item.completed_at
      ? `<div class="completion-badge" title="Completed ${escapeHTML(formatDate(item.completed_at))}">✓ ${formatYear(item.completed_at)}</div>`
      : '';
    const ownedBadge = isSearch && ownedIds.has(Number(item.game_id))
      ? '<div class="owned-badge">In library ✓</div>'
      : '';
    return `
    <div class="game-card" data-game-id="${item.game_id}">
      <img src="${getCoverURL(item)}" alt="${escapeHTML(item.game_name)}" loading="lazy" decoding="async"${priority}>
      <div class="card-title">${escapeHTML(item.game_name)}</div>
      ${tagsHTML}
      ${completedBadge}
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

    card.addEventListener('click', () => {
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
                        inLibrary = false }) {
  // Replace any existing modal (e.g. user clicks a second result).
  const existing = document.getElementById('addGameModal');
  if (existing) existing.remove();

  const title = inLibrary ? 'Edit Library Entry' : 'Add to Library';
  const submitLabel = inLibrary ? 'Save' : 'Add to Library';
  const hours = Math.round((playtime / 60) * 100) / 100;

  // Dates are server-managed (auto-set on entering playing/completed). The
  // UI shows them read-only with an explicit clear affordance; clearing is
  // sent as "" on save.
  const pendingClears = { started: false, completed: false };
  const dateRow = (label, value, key) => `
    <div class="modal-date-row">
      <span class="modal-date-label">${label}</span>
      <span class="modal-date-value${pendingClears[key] ? ' cleared' : ''}" data-date="${key}">${value ? escapeHTML(formatDate(value)) : '—'}</span>
      ${value ? `<button type="button" class="modal-date-clear" data-clear="${key}" aria-label="Clear ${label.toLowerCase()} date" title="Clear">&times;</button>` : ''}
    </div>`;
  const datesHTML = inLibrary ? `
        <div class="modal-field modal-dates">
          ${dateRow('Added', createdAt, 'added')}
          ${dateRow('Started', startedAt, 'started')}
          ${dateRow('Completed', completedAt, 'completed')}
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
              <option value="${s}"${s === status ? ' selected' : ''}>${s}</option>
            `).join('')}
          </select>
        </label>
        <label class="modal-field">Rating: <span class="modal-rating-val">${Number(rating)}</span>
          <input type="range" min="0" max="100" value="${Number(rating)}" class="modal-rating">
        </label>
        <label class="modal-field">Hours:
          <input type="number" min="0" step="0.25" value="${hours}" class="modal-playtime" inputmode="decimal">
        </label>
        <label class="modal-field">Tags
          <div class="modal-tags-wrap">
            <div class="modal-tags-chips">${tags.map(t => `<span class="tag-chip tag-chip-removable">${escapeHTML(t)}<button type="button" class="tag-chip-x" aria-label="Remove ${escapeHTML(t)}">&times;</button></span>`).join('')}</div>
            <input type="text" class="modal-tags-input" placeholder="Add tag...">
          </div>
        </label>
        <label class="modal-field">Notes
          <textarea class="modal-notes" placeholder="Notes...">${escapeHTML(notes)}</textarea>
        </label>
      </div>
      <div class="modal-footer">
        <button class="btn btn-secondary modal-cancel" type="button">Cancel</button>
        ${inLibrary ? '<button class="btn modal-remove" type="button">Remove</button>' : ''}
        <button class="btn btn-primary modal-submit" type="button">${submitLabel}</button>
      </div>
    </div>`;
  document.body.appendChild(modal);
  document.body.classList.add('modal-open');

  const prevHash = window.location.hash;
  const prevHashWasGame = prevHash.startsWith('#game/');
  setGameHash(id);

  // Live-update the rating preview label. Playtime is entered directly in
  // hours (converted to minutes on save) — no more "type 2, see 0.0".
  modal.querySelector('.modal-rating').addEventListener('input', (e) => {
    modal.querySelector('.modal-rating-val').textContent = e.target.value;
  });

  // Date clear buttons — mark pending clears; actual "" sent on save.
  modal.querySelectorAll('.modal-date-clear').forEach(btn => {
    btn.addEventListener('click', () => {
      const key = btn.dataset.clear;
      pendingClears[key] = true;
      const valueEl = modal.querySelector(`[data-date="${key}"]`);
      if (valueEl) valueEl.textContent = '—';
      btn.remove();
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
    chip.innerHTML = `${escapeHTML(text)}<button type="button" class="tag-chip-x" aria-label="Remove ${escapeHTML(text)}">&times;</button>`;
    chip.querySelector('.tag-chip-x').addEventListener('click', () => chip.remove());
    chipsContainer.appendChild(chip);
  }

  chipsContainer.querySelectorAll('.tag-chip-x').forEach(btn => {
    btn.addEventListener('click', () => btn.parentElement.remove());
  });

  tagInput.addEventListener('keydown', (e) => {
    const val = tagInput.value.trim();
    if (e.key === 'Enter' || e.key === ',') {
      e.preventDefault();
      if (val) { addChip(val); tagInput.value = ''; }
    } else if (e.key === 'Backspace' && !val) {
      const chips = chipsContainer.querySelectorAll('.tag-chip-removable');
      if (chips.length) chips[chips.length - 1].remove();
    }
  });

  // On blur, convert any pending text to a chip
  tagInput.addEventListener('blur', () => {
    const val = tagInput.value.trim();
    if (val) { addChip(val); tagInput.value = ''; }
  });

  const close = () => {
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
  };
  modal.querySelector('.modal-close').addEventListener('click', close);
  modal.querySelector('.modal-cancel').addEventListener('click', close);
  modal.addEventListener('click', (e) => {
    if (e.target === modal) close();
  });
  const escHandler = (e) => { if (e.key === 'Escape') close(); };
  document.addEventListener('keydown', escHandler);

  modal.querySelector('.modal-submit').addEventListener('click', async () => {
    const newStatus = modal.querySelector('.modal-status').value;
    const newRating = parseInt(modal.querySelector('.modal-rating').value) || 0;
    const newHours = parseFloat(modal.querySelector('.modal-playtime').value) || 0;
    const newPlaytime = Math.max(0, Math.round(newHours * 60));
    const newNotes = modal.querySelector('.modal-notes').value;
    const chipEls = modal.querySelectorAll('.modal-tags-chips .tag-chip-removable');
    const newTags = Array.from(chipEls).map(c => c.firstChild.textContent.trim());
    const submitBtn = modal.querySelector('.modal-submit');

    submitBtn.disabled = true;
    submitBtn.textContent = inLibrary ? 'Saving...' : 'Adding...';
    try {
      const payload = {
        status: newStatus,
        rating: newRating,
        playtime_minutes: newPlaytime,
        tags: newTags,
        notes: newNotes,
      };
      // Explicit clears only — omitting the fields preserves existing dates.
      if (inLibrary && pendingClears.started) payload.started_at = '';
      if (inLibrary && pendingClears.completed) payload.completed_at = '';
      await library.add(id, payload);
      showToast(inLibrary ? 'Saved' : 'Added to library');
      const wasSearch = paginationState.mode === 'search';
      close();
      // Adding from a search results page keeps you on the results (close()
      // already restored the #search hash); only the library view needs a
      // reload to reflect the change.
      if (!wasSearch) {
        const activeTab = document.querySelector('.tab.active');
        await loadLibrary(activeTab?.dataset?.status || '', paginationState.tagFilter);
      }
    } catch (err) {
      submitBtn.disabled = false;
      submitBtn.textContent = submitLabel;
      alert('Failed to save game: ' + err.message);
    } finally {
      document.removeEventListener('keydown', escHandler);
    }
  });

  const removeBtn = modal.querySelector('.modal-remove');
  if (removeBtn) {
    removeBtn.addEventListener('click', async () => {
      if (!confirm('Remove this game from your library?')) return;
      removeBtn.disabled = true;
      removeBtn.textContent = 'Removing...';
      try {
        await library.remove(id);
        close();
        const activeTab = document.querySelector('.tab.active');
        await loadLibrary(activeTab?.dataset?.status || '', paginationState.tagFilter);
      } catch (err) {
        removeBtn.disabled = false;
        removeBtn.textContent = 'Remove';
        alert('Failed to remove game: ' + err.message);
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
