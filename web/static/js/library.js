import { library, getCoverURL } from './api.js';

const VALID_STATUSES = ['wishlist', 'backlog', 'playing', 'completed', 'abandoned'];
const PAGE_SIZE = 60;

// Pagination state
let paginationState = {
  currentStatus: '',
  offset: 0,
  loading: false,
  hasMore: true,
  pageSize: PAGE_SIZE,
};

let scrollListenerAttached = false;

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
  };

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
      grid.innerHTML = '<div class="empty-state">No games in this collection yet. Search above to add games.</div>';
      paginationState.hasMore = false;
    }
    return;
  }

  if (isFirstPage) {
    grid.innerHTML = '';
  }

  // Render and append cards
  const html = buildCardHTML(items);
  grid.insertAdjacentHTML('beforeend', html);
  attachCardEvents(grid, items); // Attach events to new cards only

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
export function renderLibraryItems(items) {
  const grid = document.getElementById('gameGrid');
  if (!grid) return;

  // Initialize pagination state for empty status
  paginationState = {
    currentStatus: '',
    offset: 0,
    loading: false,
    hasMore: true,
    pageSize: PAGE_SIZE,
  };

  if (!items || items.length === 0) {
    grid.innerHTML = '<div class="empty-state">No games in this collection yet. Search above to add games.</div>';
    paginationState.hasMore = false;
    return;
  }

  grid.innerHTML = '';
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
    const items = await library.list(
      paginationState.currentStatus,
      paginationState.pageSize,
      paginationState.offset
    );
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
    return `
    <div class="game-card" data-game-id="${item.game_id}" data-status="${item.status}">
      <div class="game-card-inner">
        <div class="game-card-front">
          <img src="${getCoverURL(item)}" alt="${item.game_name}" loading="lazy" decoding="async"${priority}>
          <div class="card-title">${escapeHTML(item.game_name)}</div>
          ${item.rating > 0 ? `<div class="rating-display">${item.rating}</div>` : ''}
        </div>
        <div class="game-card-back">
          <h3>${escapeHTML(item.game_name)}</h3>
          <select class="card-status" data-game-id="${item.game_id}">
            ${VALID_STATUSES.map(s => `
              <option value="${s}" ${s === item.status ? 'selected' : ''}>${s}</option>
            `).join('')}
          </select>
          <label>Rating: <span class="rating-val">${item.rating}</span></label>
          <input type="range" min="0" max="100" value="${item.rating}"
                 class="card-rating" data-game-id="${item.game_id}">
          <label>Hours: <span class="playtime-val">${(item.playtime_minutes / 60).toFixed(1)}</span></label>
          <input type="number" min="0" value="${item.playtime_minutes}"
                 class="card-playtime" data-game-id="${item.game_id}" step="15">
          <textarea class="card-notes" data-game-id="${item.game_id}"
                    placeholder="Notes...">${escapeHTML(item.notes || '')}</textarea>
          <button class="save" data-game-id="${item.game_id}">Save</button>
          <button class="danger" data-game-id="${item.game_id}">Remove</button>
        </div>
      </div>
    </div>
  `}).join('');
}


// attachCardEvents attaches event handlers to game cards.
// If items are provided, only attach to cards for those items; otherwise attach to all cards.
function attachCardEvents(grid, newItems = null) {
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

    card.addEventListener('click', (e) => {
      if (e.target.closest('button') || e.target.closest('select') ||
          e.target.closest('input') || e.target.closest('textarea')) {
        return;
      }
      const flipped = card.dataset.flipped === 'true';
      card.dataset.flipped = flipped ? 'false' : 'true';
    });
  });

  // For simplicity, attach all input handlers to all matching elements in the grid
  // (event delegation would be cleaner but this is more straightforward)
  grid.querySelectorAll('.card-rating').forEach(input => {
    // Avoid duplicate listeners by checking if we already have one
    if (!input.dataset.listenerAttached) {
      input.addEventListener('input', () => {
        const val = input.value;
        const label = input.previousElementSibling?.querySelector('.rating-val');
        if (label) label.textContent = val;
      });
      input.dataset.listenerAttached = 'true';
    }
  });

  grid.querySelectorAll('.card-playtime').forEach(input => {
    if (!input.dataset.listenerAttached) {
      input.addEventListener('input', () => {
        const val = input.value;
        const label = input.previousElementSibling?.querySelector('.playtime-val');
        if (label) label.textContent = (parseInt(val || 0) / 60).toFixed(1);
      });
      input.dataset.listenerAttached = 'true';
    }
  });

  grid.querySelectorAll('button.save').forEach(btn => {
    if (!btn.dataset.listenerAttached) {
      btn.addEventListener('click', async (e) => {
        e.stopPropagation();
        const gameID = btn.dataset.gameId;
        const card = btn.closest('.game-card');
        const status = card.querySelector('.card-status').value;
        const rating = parseInt(card.querySelector('.card-rating').value) || 0;
        const playtime = parseInt(card.querySelector('.card-playtime').value) || 0;
        const notes = card.querySelector('.card-notes').value;

        try {
          await library.add(gameID, {
            status,
            rating,
            playtime_minutes: playtime,
            tags: [],
            notes,
          });
          card.dataset.flipped = 'false';
          const activeTab = document.querySelector('.tab.active');
          loadLibrary(activeTab?.dataset?.status || '');
        } catch (err) {
          alert('Failed to save: ' + err.message);
        }
      });
      btn.dataset.listenerAttached = 'true';
    }
  });

  grid.querySelectorAll('button.danger').forEach(btn => {
    if (!btn.dataset.listenerAttached) {
      btn.addEventListener('click', async (e) => {
        e.stopPropagation();
        const gameID = btn.dataset.gameId;
        if (!confirm('Remove this game from your library?')) return;

        try {
          await library.remove(gameID);
          const activeTab = document.querySelector('.tab.active');
          loadLibrary(activeTab?.dataset?.status || '');
        } catch (err) {
          alert('Failed to remove: ' + err.message);
        }
      });
      btn.dataset.listenerAttached = 'true';
    }
  });
}

export async function addGameToLibrary(game, status = 'backlog') {
  if (!game || !game.id) return;
  try {
    await library.add(game.id, {
      status,
      rating: 0,
      playtime_minutes: 0,
      tags: [],
      notes: '',
    });
    const activeTab = document.querySelector('.tab.active');
    await loadLibrary(activeTab?.dataset?.status || '');
  } catch (err) {
    alert('Failed to add game: ' + err.message);
  }
}

function escapeHTML(str) {
  const div = document.createElement('div');
  div.textContent = str;
  return div.innerHTML;
}
