import { searchGames, getCoverThumbnailURL, autocompleteTags } from './api.js';

let searchTimer = null;
let activeController = null;
let selectedIndex = -1;
let currentResults = [];
let currentQuery = '';

export function initSearch(inputEl, resultsEl, onSelect, onSubmit, onTagLookup) {
  inputEl.addEventListener('input', () => {
    scheduleSearch(inputEl.value, resultsEl, onSelect, onSubmit, onTagLookup);
  });

  inputEl.addEventListener('keydown', (e) => {
    handleKeyboard(e, inputEl, resultsEl, onSelect, onSubmit, onTagLookup);
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

function scheduleSearch(query, resultsEl, onSelect, onSubmit, onTagLookup) {
  clearTimeout(searchTimer);
  currentQuery = query;

  // Handle $tag prefix — autocomplete tags from user's library.
  // Space-separated = AND, pipe-separated = OR.
  if (query.startsWith('$') && onTagLookup) {
    const raw = query.slice(1).trim();
    if (raw.length < 1) {
      resultsEl.classList.remove('active');
      currentResults = [];
      currentQuery = '';
      return;
    }

    // Extract the last "word" for autocomplete (the prefix being typed)
    const lastSep = Math.max(raw.lastIndexOf(' '), raw.lastIndexOf('|'));
    const prefix = lastSep >= 0 ? raw.slice(lastSep + 1).trim() : raw;

    searchTimer = setTimeout(async () => {
      try {
        const [tagSuggestions, items] = await Promise.all([
          autocompleteTags(prefix),
          onTagLookup(raw),
        ]);
        currentResults = items;
        selectedIndex = -1;
        renderTagSuggestions(tagSuggestions, items, resultsEl, onSelect, prefix, raw);
      } catch (err) {
        currentResults = [];
        renderTagSuggestions([], [], resultsEl, onSelect, prefix, raw);
      }
    }, 200);
    return;
  }

  if (query.length < 2) {
    resultsEl.classList.remove('active');
    currentResults = [];
    return;
  }

  searchTimer = setTimeout(async () => {
    if (activeController) activeController.abort();
    const controller = new AbortController();
    activeController = controller;

    try {
      const results = await searchGames(query, controller.signal);
      if (controller === activeController) {
        currentResults = results;
        selectedIndex = -1;
        renderResults(results, resultsEl, onSelect, onSubmit);
      }
    } catch (err) {
      if (err.name !== 'AbortError' && controller === activeController) {
        currentResults = [];
        renderResults([], resultsEl, onSelect, onSubmit);
      }
    }
  }, 400);
}

function renderResults(results, resultsEl, onSelect, onSubmit) {
  if (results.length === 0) {
    resultsEl.innerHTML = '<div class="no-results">No games found</div>';
  } else {
    // Slice to first 8 results for dropdown display
    const displayResults = results.slice(0, 8);
    let html = displayResults.map((g, i) => {
      const year = g.first_release_date
        ? new Date(g.first_release_date * 1000).getFullYear()
        : '';
      return `
        <div class="search-result-item${i === selectedIndex ? ' selected' : ''}"
             data-index="${i}" data-id="${g.id}">
          <img src="${getCoverThumbnailURL(g)}"
               alt="${g.name}" loading="lazy" decoding="async">
          <div class="info">
            <div class="name">${escapeHTML(g.name)}</div>
            <div class="year">${year}</div>
          </div>
        </div>`;
    }).join('');

    // Add footer "See all results" row if there are results and onSubmit is provided
    if (results.length > 0 && onSubmit) {
      html += `<div class="search-result-more">See all results for "${escapeHTML(currentQuery)}" →</div>`;
    }

    resultsEl.innerHTML = html;

    resultsEl.querySelectorAll('.search-result-item').forEach(item => {
      item.addEventListener('click', () => {
        const id = Number(item.dataset.id);
        const game = results.find(g => g.id === id);
        if (!game) return;
        resultsEl.classList.remove('active');
        if (onSelect) onSelect(game);
      });
    });

    // Add footer row click handler
    const footerRow = resultsEl.querySelector('.search-result-more');
    if (footerRow && onSubmit) {
      footerRow.addEventListener('click', () => {
        resultsEl.classList.remove('active');
        if (onSubmit) onSubmit(currentQuery);
      });
    }
  }

  resultsEl.classList.add('active');
}

function renderTagSuggestions(tagSuggestions, items, resultsEl, onSelect, prefix, raw) {
  let html = '';

  // Build replacement function: replaces last word in raw with the given tag
  function replaceLastWord(tag) {
    const lastSep = Math.max(raw.lastIndexOf(' '), raw.lastIndexOf('|'));
    if (lastSep >= 0) {
      return raw.slice(0, lastSep + 1) + tag;
    }
    return tag;
  }

  // Tag autocomplete suggestions
  if (tagSuggestions.length > 0) {
    html += '<div class="tag-suggestions">';
    html += tagSuggestions.map(t => {
      const matchStart = t.toLowerCase().indexOf(prefix.toLowerCase());
      const before = t.slice(0, matchStart);
      const match = t.slice(matchStart, matchStart + prefix.length);
      const after = t.slice(matchStart + prefix.length);
      return `<span class="tag-suggestion-chip" data-tag="${escapeHTML(t)}">${escapeHTML(before)}<strong>${escapeHTML(match)}</strong>${escapeHTML(after)}</span>`;
    }).join('');
    // Also show the literal prefix as an option if not already a suggestion
    if (!tagSuggestions.some(t => t.toLowerCase() === prefix.toLowerCase())) {
      html += `<span class="tag-suggestion-chip tag-suggestion-new" data-tag="${escapeHTML(prefix)}">"${escapeHTML(prefix)}"</span>`;
    }
    html += '</div>';
  } else {
    // No autocomplete matches — show the literal prefix as a filter option
    html += `<div class="tag-suggestions"><span class="tag-suggestion-chip tag-suggestion-new" data-tag="${escapeHTML(prefix)}">Search tag "${escapeHTML(prefix)}"</span></div>`;
  }

  // Matching library items
  if (items && items.length > 0) {
    if (html) html += '<div class="search-result-divider"></div>';
    const displayItems = items.slice(0, 8);
    html += displayItems.map((item, i) => {
      const year = item.first_release_date
        ? new Date(item.first_release_date * 1000).getFullYear()
        : '';
      return `
        <div class="search-result-item tag-result${i === selectedIndex ? ' selected' : ''}"
             data-index="${i}" data-id="${item.game_id}">
          <img src="${getCoverThumbnailURL(item)}"
               alt="${escapeHTML(item.game_name)}" loading="lazy" decoding="async">
          <div class="info">
            <div class="name">${escapeHTML(item.game_name)}</div>
            <div class="year">${year} · ${escapeHTML(item.status)}</div>
          </div>
        </div>`;
    }).join('');

    html += `<div class="search-result-more">Filter library by "${escapeHTML(raw)}" →</div>`;
  } else if (tagSuggestions.length === 0) {
    html += `<div class="no-results">No games tagged "${escapeHTML(raw)}"</div>`;
  }

  resultsEl.innerHTML = html;

  // Click handlers for tag suggestion chips — replace last word in input, re-search
  resultsEl.querySelectorAll('.tag-suggestion-chip').forEach(chip => {
    chip.addEventListener('click', () => {
      const tag = chip.dataset.tag;
      const newRaw = replaceLastWord(tag);
      // Find the search input and update its value
      const inputEl = document.getElementById('searchInput');
      if (inputEl) {
        inputEl.value = '$' + newRaw;
        inputEl.focus();
      }
      resultsEl.classList.remove('active');
      // Re-trigger search with new value
      inputEl.dispatchEvent(new Event('input'));
    });
  });

  // Click handlers for library items
  resultsEl.querySelectorAll('.search-result-item').forEach(item => {
    item.addEventListener('click', () => {
      const id = Number(item.dataset.id);
      const match = items.find(g => String(g.game_id) === String(id));
      if (!match) return;
      resultsEl.classList.remove('active');
      if (onSelect) onSelect(match);
    });
  });

  // Footer "Filter library" click — use the full raw string
  const footerRow = resultsEl.querySelector('.search-result-more');
  if (footerRow) {
    footerRow.addEventListener('click', () => {
      resultsEl.classList.remove('active');
      const event = new CustomEvent('tagfilter', { detail: { tag: raw } });
      resultsEl.dispatchEvent(event);
    });
  }

  resultsEl.classList.add('active');
}

function handleKeyboard(e, inputEl, resultsEl, onSelect, onSubmit, onTagLookup) {
  switch (e.key) {
    case 'ArrowDown':
      if (!resultsEl.classList.contains('active')) return;
      e.preventDefault();
      selectedIndex = Math.min(selectedIndex + 1, currentResults.length - 1);
      updateSelection(resultsEl);
      break;
    case 'ArrowUp':
      if (!resultsEl.classList.contains('active')) return;
      e.preventDefault();
      selectedIndex = Math.max(selectedIndex - 1, 0);
      updateSelection(resultsEl);
      break;
    case 'Enter':
      e.preventDefault();
      if (resultsEl.classList.contains('active') && selectedIndex >= 0 && selectedIndex < currentResults.length) {
        resultsEl.classList.remove('active');
        if (onSelect) onSelect(currentResults[selectedIndex]);
      } else if (currentQuery && currentQuery.startsWith('$') && onTagLookup) {
        // $tag with no selection — filter library by tag(s)
        const tag = currentQuery.slice(1).trim();
        if (tag) {
          resultsEl.classList.remove('active');
          const event = new CustomEvent('tagfilter', { detail: { tag } });
          resultsEl.dispatchEvent(event);
        }
      } else if (currentQuery && currentQuery.length >= 2 && onSubmit) {
        resultsEl.classList.remove('active');
        if (onSubmit) onSubmit(currentQuery);
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
