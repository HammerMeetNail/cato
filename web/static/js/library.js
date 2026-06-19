import { library, getCoverURL } from './api.js';

const VALID_STATUSES = ['wishlist', 'backlog', 'playing', 'completed', 'abandoned'];

export async function loadLibrary(status) {
  const grid = document.getElementById('gameGrid');
  if (!grid) return;

  grid.innerHTML = '<div class="loading">Loading library...</div>';

  try {
    const items = await library.list(status || '');
    if (items.length === 0) {
      grid.innerHTML = '<div class="empty-state">No games in this collection yet. Search above to add games.</div>';
      return;
    }
    renderCards(grid, items);
  } catch (err) {
    grid.innerHTML = `<div class="empty-state">Failed to load library: ${err.message}</div>`;
  }
}

function renderCards(grid, items) {
  grid.innerHTML = items.map(item => `
    <div class="game-card" data-game-id="${item.game_id}" data-status="${item.status}">
      <div class="game-card-inner">
        <div class="game-card-front">
          <img src="${getCoverURL(item)}" alt="${item.game_name}" loading="lazy"
               onerror="this.src='/covers/placeholder.jpg'">
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
  `).join('');

  attachCardEvents(grid);
}

function attachCardEvents(grid) {
  grid.querySelectorAll('.game-card').forEach(card => {
    card.addEventListener('click', (e) => {
      if (e.target.closest('button') || e.target.closest('select') ||
          e.target.closest('input') || e.target.closest('textarea')) {
        return;
      }
      const flipped = card.dataset.flipped === 'true';
      card.dataset.flipped = flipped ? 'false' : 'true';
    });
  });

  grid.querySelectorAll('.card-rating').forEach(input => {
    input.addEventListener('input', () => {
      const val = input.value;
      const label = input.previousElementSibling?.querySelector('.rating-val');
      if (label) label.textContent = val;
    });
  });

  grid.querySelectorAll('.card-playtime').forEach(input => {
    input.addEventListener('input', () => {
      const val = input.value;
      const label = input.previousElementSibling?.querySelector('.playtime-val');
      if (label) label.textContent = (parseInt(val || 0) / 60).toFixed(1);
    });
  });

  grid.querySelectorAll('button.save').forEach(btn => {
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
  });

  grid.querySelectorAll('button.danger').forEach(btn => {
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
  });
}

export async function addGameToLibrary(game, status = 'backlog') {
  try {
    await library.add(game.id, {
      status,
      rating: 0,
      playtime_minutes: 0,
      tags: [],
      notes: '',
    });
    const activeTab = document.querySelector('.tab.active');
    loadLibrary(activeTab?.dataset?.status || '');
  } catch (err) {
    alert('Failed to add game: ' + err.message);
  }
}

function escapeHTML(str) {
  const div = document.createElement('div');
  div.textContent = str;
  return div.innerHTML;
}
