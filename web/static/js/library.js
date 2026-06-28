import { library, getCoverURL, getGame, searchGamesFull } from './api.js';

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
    renderPagedItems(grid, results, true);
  } catch (err) {
    grid.innerHTML = `<div class="empty-state">Failed to load results: ${err.message}</div>`;
  }
}

export async function loadLibrary(status) {
  const grid = document.getElementById('gameGrid');
  if (!grid) return;

  // Reset pagination state
  paginationState = {
    currentStatus: status || '',
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

  grid.innerHTML = '<div class="loading">Loading library...</div>';

  try {
    const items = await library.list(status || '', PAGE_SIZE, 0);
    renderPagedItems(grid, items, true);
  } catch (err) {
    grid.innerHTML = `<div class="empty-state">Failed to load library: ${err.message}</div>`;
  }
}

// renderPagedItems renders library items into the grid, either replacing or appending.
// isFirstPage=true means clear and replace; false means append.
function renderPagedItems(grid, items, isFirstPage = true) {
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
  paginationState.hasMore = items.length === paginationState.pageSize;
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
    let items;
    if (paginationState.mode === 'search') {
      items = await searchGamesFull(paginationState.searchQuery, {
        limit: paginationState.pageSize,
        offset: paginationState.offset,
      });
    } else {
      items = await library.list(
        paginationState.currentStatus,
        paginationState.pageSize,
        paginationState.offset
      );
    }
    renderPagedItems(grid, items, false); // false = append, not replace
  } catch (err) {
    paginationState.loading = false;
    console.error('Failed to load more:', err.message);
  }
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
}

function buildCardHTML(items) {
  return items.map((item, index) => {
    // High priority for the first 8 cards
    const priority = index < 8 ? ' fetchpriority="high"' : '';
    const tagsHTML = (item.tags && item.tags.length)
      ? `<div class="card-tags">${item.tags.map(t => `<span class="tag-chip">${escapeHTML(t)}</span>`).join('')}</div>`
      : '';
    return `
    <div class="game-card" data-game-id="${item.game_id}">
      <img src="${getCoverURL(item)}" alt="${escapeHTML(item.game_name)}" loading="lazy" decoding="async"${priority}>
      <div class="card-title">${escapeHTML(item.game_name)}</div>
      ${tagsHTML}
      ${item.rating > 0 ? `<div class="rating-display">${item.rating}</div>` : ''}
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

    card.addEventListener('click', () => {
      const gameId = card.dataset.gameId;

      if (paginationState.mode === 'search') {
        // In search mode, use original search result
        const result = searchResultsById.get(gameId);
        if (result) addGameToLibrary(result);
      } else {
        // In library mode, use library item
        const item = itemsById.get(gameId);
        if (item) openLibraryItemModal(item);
      }
    });
  });
}

// openGameModal opens the routable game popup for a #game/<id> deep link. If the
// game is already rendered in the library it opens the edit form (pre-filled,
// with Save/Remove); otherwise it fetches the game metadata and opens the
// add-to-library form.
export async function openGameModal(gameID) {
  const item = itemsById.get(String(gameID));
  if (item) {
    openLibraryItemModal(item);
    return;
  }
  try {
    const game = await getGame(gameID);
    addGameToLibrary(game);
  } catch (err) {
    console.error('Failed to fetch game:', err.message);
  }
}

// addGameToLibrary opens a modal pre-filled with the game's info so the user
// can choose status/rating/playtime/notes before the game is added. The actual
// POST only happens on "Add to Library"; cancelling dismisses the modal and
// leaves the library untouched.
export function addGameToLibrary(game) {
  if (!game || !game.id) return;
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
    status: item.status,
    rating: item.rating || 0,
    playtime: item.playtime_minutes || 0,
    tags: item.tags || [],
    notes: item.notes || '',
    inLibrary: true,
  });
}

// openGameForm renders the routable game popup (#game/<id>). In "add" mode it
// shows a blank form with an "Add to Library" action; in "edit" mode
// (inLibrary=true) it is pre-filled and offers Save and Remove. Both actions
// POST to library.add, which upserts.
function openGameForm({ id, name, cover, year = '', status = 'backlog',
                        rating = 0, playtime = 0, tags = [], notes = '', inLibrary = false }) {
  // Replace any existing modal (e.g. user clicks a second result).
  const existing = document.getElementById('addGameModal');
  if (existing) existing.remove();

  const title = inLibrary ? 'Edit Library Entry' : 'Add to Library';
  const submitLabel = inLibrary ? 'Save' : 'Add to Library';

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
            ${year ? `<div class="modal-year">${year}</div>` : ''}
          </div>
        </div>
        <label class="modal-field">Status
          <select class="modal-status">
            ${VALID_STATUSES.map(s => `
              <option value="${s}"${s === status ? ' selected' : ''}>${s}</option>
            `).join('')}
          </select>
        </label>
        <label class="modal-field">Rating: <span class="modal-rating-val">${rating}</span>
          <input type="range" min="0" max="100" value="${rating}" class="modal-rating">
        </label>
        <label class="modal-field">Hours: <span class="modal-playtime-val">${(playtime / 60).toFixed(1)}</span>
          <input type="number" min="0" value="${playtime}" class="modal-playtime" step="15">
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

  // Live-update the rating/playtime preview labels.
  modal.querySelector('.modal-rating').addEventListener('input', (e) => {
    modal.querySelector('.modal-rating-val').textContent = e.target.value;
  });
  modal.querySelector('.modal-playtime').addEventListener('input', (e) => {
    modal.querySelector('.modal-playtime-val').textContent =
      (parseInt(e.target.value || 0) / 60).toFixed(1);
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
    const newPlaytime = parseInt(modal.querySelector('.modal-playtime').value) || 0;
    const newNotes = modal.querySelector('.modal-notes').value;
    const chipEls = modal.querySelectorAll('.modal-tags-chips .tag-chip-removable');
    const newTags = Array.from(chipEls).map(c => c.firstChild.textContent.trim());
    const submitBtn = modal.querySelector('.modal-submit');

    submitBtn.disabled = true;
    submitBtn.textContent = inLibrary ? 'Saving...' : 'Adding...';
    try {
      await library.add(id, {
        status: newStatus,
        rating: newRating,
        playtime_minutes: newPlaytime,
        tags: newTags,
        notes: newNotes,
      });
      const wasSearch = paginationState.mode === 'search';
      close();
      // Adding from a search results page keeps you on the results (close()
      // already restored the #search hash); only the library view needs a
      // reload to reflect the change.
      if (!wasSearch) {
        const activeTab = document.querySelector('.tab.active');
        await loadLibrary(activeTab?.dataset?.status || '');
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
        await loadLibrary(activeTab?.dataset?.status || '');
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

function escapeHTML(str) {
  const div = document.createElement('div');
  div.textContent = str;
  return div.innerHTML;
}
