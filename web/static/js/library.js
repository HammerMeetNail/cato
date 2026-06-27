import { library, getCoverURL, getGame } from './api.js';

const VALID_STATUSES = ['wishlist', 'backlog', 'playing', 'completed', 'abandoned'];

// Hash routing utilities
export function getHashStatus() {
  const hash = window.location.hash.slice(1);
  if (!hash || hash.startsWith('game/')) return '';
  if (VALID_STATUSES.includes(hash)) return hash;
  return '';
}

export function getHashGameId() {
  const hash = window.location.hash.slice(1);
  if (!hash.startsWith('game/')) return null;
  const id = parseInt(hash.slice(5), 10);
  return isNaN(id) ? null : id;
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
export function renderLibraryItems(items, status = '') {
  const grid = document.getElementById('gameGrid');
  if (!grid) return;

  paginationState = {
    currentStatus: status || '',
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

// openGameModal fetches a game by ID from the API and opens the add-to-library
// modal. Used for direct game links (#game/<id>).
export async function openGameModal(gameID) {
  try {
    const game = await getGame(gameID);
    openAddToLibraryModal(game);
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
  openAddToLibraryModal(game);
}

function openAddToLibraryModal(game) {
  // Replace any existing modal (e.g. user clicks a second result).
  const existing = document.getElementById('addGameModal');
  if (existing) existing.remove();

  const year = game.first_release_date
    ? new Date(game.first_release_date * 1000).getFullYear()
    : '';

  const modal = document.createElement('div');
  modal.id = 'addGameModal';
  modal.className = 'modal-overlay';
  modal.setAttribute('role', 'dialog');
  modal.setAttribute('aria-modal', 'true');
  modal.setAttribute('aria-labelledby', 'addGameModalTitle');
  modal.innerHTML = `
    <div class="modal-card">
      <div class="modal-header">
        <h2 id="addGameModalTitle">Add to Library</h2>
        <button class="modal-close" type="button" aria-label="Close">&times;</button>
      </div>
      <div class="modal-body">
        <div class="modal-game-info">
          <img src="${getCoverURL(game)}" alt="${escapeHTML(game.name)}" decoding="async">
          <div class="modal-game-meta">
            <h3>${escapeHTML(game.name)}</h3>
            ${year ? `<div class="modal-year">${year}</div>` : ''}
          </div>
        </div>
        <label class="modal-field">Status
          <select class="modal-status">
            ${VALID_STATUSES.map(s => `
              <option value="${s}"${s === 'backlog' ? ' selected' : ''}>${s}</option>
            `).join('')}
          </select>
        </label>
        <label class="modal-field">Rating: <span class="modal-rating-val">0</span>
          <input type="range" min="0" max="100" value="0" class="modal-rating">
        </label>
        <label class="modal-field">Hours: <span class="modal-playtime-val">0.0</span>
          <input type="number" min="0" value="0" class="modal-playtime" step="15">
        </label>
        <label class="modal-field">Notes
          <textarea class="modal-notes" placeholder="Notes..."></textarea>
        </label>
      </div>
      <div class="modal-footer">
        <button class="btn btn-secondary modal-cancel" type="button">Cancel</button>
        <button class="btn btn-primary modal-submit" type="button">Add to Library</button>
      </div>
    </div>`;
  document.body.appendChild(modal);
  document.body.classList.add('modal-open');

  const prevHash = window.location.hash;
  const prevHashWasGame = prevHash.startsWith('#game/');
  setGameHash(game.id);

  // Live-update the rating/playtime preview labels, mirroring the card back.
  modal.querySelector('.modal-rating').addEventListener('input', (e) => {
    modal.querySelector('.modal-rating-val').textContent = e.target.value;
  });
  modal.querySelector('.modal-playtime').addEventListener('input', (e) => {
    modal.querySelector('.modal-playtime-val').textContent =
      (parseInt(e.target.value || 0) / 60).toFixed(1);
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
    const status = modal.querySelector('.modal-status').value;
    const rating = parseInt(modal.querySelector('.modal-rating').value) || 0;
    const playtime = parseInt(modal.querySelector('.modal-playtime').value) || 0;
    const notes = modal.querySelector('.modal-notes').value;
    const submitBtn = modal.querySelector('.modal-submit');

    submitBtn.disabled = true;
    submitBtn.textContent = 'Adding...';
    try {
      await library.add(game.id, {
        status,
        rating,
        playtime_minutes: playtime,
        tags: [],
        notes,
      });
      close();
      const activeTab = document.querySelector('.tab.active');
      await loadLibrary(activeTab?.dataset?.status || '');
    } catch (err) {
      submitBtn.disabled = false;
      submitBtn.textContent = 'Add to Library';
      alert('Failed to add game: ' + err.message);
    } finally {
      document.removeEventListener('keydown', escHandler);
    }
  });

  modal.querySelector('.modal-close').focus();
}

function escapeHTML(str) {
  const div = document.createElement('div');
  div.textContent = str;
  return div.innerHTML;
}
