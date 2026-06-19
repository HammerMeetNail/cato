import { searchGames } from './api.js';

let searchTimer = null;
let activeController = null;
let selectedIndex = -1;
let currentResults = [];

export function initSearch(inputEl, resultsEl, onSelect) {
  inputEl.addEventListener('input', () => {
    scheduleSearch(inputEl.value, resultsEl, onSelect);
  });

  inputEl.addEventListener('keydown', (e) => {
    handleKeyboard(e, resultsEl, onSelect);
  });

  inputEl.addEventListener('focus', () => {
    if (currentResults.length > 0) {
      resultsEl.classList.add('active');
    }
  });

  document.addEventListener('click', (e) => {
    if (!inputEl.contains(e.target) && !resultsEl.contains(e.target)) {
      resultsEl.classList.remove('active');
    }
  });

  return {
    clear() {
      inputEl.value = '';
      resultsEl.classList.remove('active');
      currentResults = [];
    }
  };
}

function scheduleSearch(query, resultsEl, onSelect) {
  clearTimeout(searchTimer);

  if (query.length < 2) {
    resultsEl.classList.remove('active');
    currentResults = [];
    return;
  }

  searchTimer = setTimeout(async () => {
    if (activeController) activeController.abort();
    activeController = new AbortController();

    try {
      const results = await searchGames(query);
      if (activeController.signal === activeController.signal) {
        currentResults = results;
        selectedIndex = -1;
        renderResults(results, resultsEl, onSelect);
      }
    } catch (err) {
      if (err.name !== 'AbortError') {
        currentResults = [];
        renderResults([], resultsEl, onSelect);
      }
    }
  }, 200);
}

function renderResults(results, resultsEl, onSelect) {
  if (results.length === 0) {
    resultsEl.innerHTML = '<div class="no-results">No games found</div>';
  } else {
    resultsEl.innerHTML = results.map((g, i) => {
      const year = g.first_release_date
        ? new Date(g.first_release_date * 1000).getFullYear()
        : '';
      return `
        <div class="search-result-item${i === selectedIndex ? ' selected' : ''}"
             data-index="${i}" data-id="${g.id}">
          <img src="${g.local_cover_path || g.cover_url || '/covers/placeholder.jpg'}"
               alt="${g.name}" loading="lazy"
               onerror="this.src='/covers/placeholder.jpg'">
          <div class="info">
            <div class="name">${escapeHTML(g.name)}</div>
            <div class="year">${year}</div>
          </div>
        </div>`;
    }).join('');

    resultsEl.querySelectorAll('.search-result-item').forEach(item => {
      item.addEventListener('click', () => {
        const id = parseInt(item.dataset.id);
        const game = results.find(g => g.id === id);
        resultsEl.classList.remove('active');
        if (onSelect) onSelect(game);
      });
    });
  }

  resultsEl.classList.add('active');
}

function handleKeyboard(e, resultsEl, onSelect) {
  if (!resultsEl.classList.contains('active')) return;

  switch (e.key) {
    case 'ArrowDown':
      e.preventDefault();
      selectedIndex = Math.min(selectedIndex + 1, currentResults.length - 1);
      updateSelection(resultsEl);
      break;
    case 'ArrowUp':
      e.preventDefault();
      selectedIndex = Math.max(selectedIndex - 1, 0);
      updateSelection(resultsEl);
      break;
    case 'Enter':
      e.preventDefault();
      if (selectedIndex >= 0 && selectedIndex < currentResults.length) {
        resultsEl.classList.remove('active');
        if (onSelect) onSelect(currentResults[selectedIndex]);
      }
      break;
    case 'Escape':
      resultsEl.classList.remove('active');
      break;
  }
}

function updateSelection(resultsEl) {
  resultsEl.querySelectorAll('.search-result-item').forEach((item, i) => {
    item.classList.toggle('selected', i === selectedIndex);
  });
}

function escapeHTML(str) {
  const div = document.createElement('div');
  div.textContent = str;
  return div.innerHTML;
}
