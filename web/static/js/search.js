import { searchGames, getCoverThumbnailURL, autocompleteTags, formatTagForQuery, library } from './api.js';
import { escapeHTML, showToast, formatPlatformName } from './library.js';

// Tuning knobs (SEARCH_IMPROVEMENTS.md §1): 200ms feels responsive without
// hammering the API; the dropdown renders at most 8 rows; the client-side
// result cache makes backspacing instant without any network round-trip.
const SEARCH_DEBOUNCE_MS = 200;
const DROPDOWN_MAX = 8;
const RESULT_CACHE_MAX = 50;
const RECENTS_KEY = 'cato-recent-searches';
const RECENTS_MAX = 8;

let searchTimer = null;
let activeController = null;
let selectedIndex = -1;
let currentResults = [];
let currentQuery = '';
let activeInputEl = null;
// How many result rows are actually rendered in the dropdown. The games list
// renders only the first 8 results while ArrowDown used to index into all 10,
// letting the selection become invisible (FINDINGS §3.6).
let renderedCount = 0;

// resultCache maps raw query -> results array (per page session). Serving a
// repeat query from memory avoids both latency and an IGDB round-trip.
const resultCache = new Map();
// ownedIds caches game IDs confirmed to be in the user's library, so badges
// survive re-renders within the session. Positives only — a missing ID just
// means "unknown", never "confirmed absent".
const ownedIds = new Set();

// --- recent searches (localStorage) ---------------------------------------

function getRecentSearches() {
  try {
    const list = JSON.parse(localStorage.getItem(RECENTS_KEY));
    return Array.isArray(list) ? list.filter(q => typeof q === 'string') : [];
  } catch {
    return [];
  }
}

function saveRecentSearches(list) {
  try {
    localStorage.setItem(RECENTS_KEY, JSON.stringify(list.slice(0, RECENTS_MAX)));
  } catch { /* private mode / quota — recents are best-effort */ }
}

// recordRecentSearch stores a submitted query (deduped, newest first).
// Exported so hash-route loads (#search/q) can record too.
export function recordRecentSearch(query) {
  const q = String(query || '').trim();
  if (!q || q.startsWith('$')) return;
  const list = getRecentSearches().filter(x => x.toLowerCase() !== q.toLowerCase());
  list.unshift(q);
  saveRecentSearches(list);
}

function removeRecentSearch(query) {
  saveRecentSearches(getRecentSearches().filter(x => x !== query));
}

function clearRecentSearches() {
  try { localStorage.removeItem(RECENTS_KEY); } catch {}
}

// --- dropdown open/close + ARIA --------------------------------------------

function openDropdown(inputEl, resultsEl) {
  resultsEl.classList.add('active');
  inputEl.setAttribute('aria-expanded', 'true');
}

function closeDropdown(inputEl, resultsEl) {
  resultsEl.classList.remove('active');
  inputEl.setAttribute('aria-expanded', 'false');
  inputEl.removeAttribute('aria-activedescendant');
}

// --- highlight --------------------------------------------------------------

// highlightName wraps the matched substring in <mark>. Tries the whole query
// case-insensitively first, then its longest ≥3-char token (trigram matches
// aren't always contiguous). All pieces are escaped independently, so this is
// injection-safe.
export function highlightName(name, query) {
  const q = String(query || '').trim().toLowerCase();
  if (!q) return escapeHTML(name);
  const lower = name.toLowerCase();
  let idx = lower.indexOf(q);
  let len = q.length;
  if (idx === -1) {
    const tokens = q.split(/\s+/).filter(t => t.length >= 3)
      .sort((a, b) => b.length - a.length);
    for (const tok of tokens) {
      idx = lower.indexOf(tok);
      if (idx !== -1) { len = tok.length; break; }
    }
  }
  if (idx === -1) return escapeHTML(name);
  return escapeHTML(name.slice(0, idx)) +
    '<mark>' + escapeHTML(name.slice(idx, idx + len)) + '</mark>' +
    escapeHTML(name.slice(idx + len));
}

// --- ownership + quick-add --------------------------------------------------

function ownedBadgeHTML() {
  return '<span class="owned-badge owned-badge-sm">In library ✓</span>';
}

function swapToAddBadge(btn) {
  if (btn && btn.isConnected) {
    btn.insertAdjacentHTML('beforebegin', ownedBadgeHTML());
    btn.remove();
  }
}

// checkOwnership batch-confirms which dropdown rows are already in the
// library, then patches their rows: "+ Add" flips to an "In library ✓" badge.
async function checkOwnership(resultsEl, results) {
  const ids = results.map(g => Number(g.id)).filter(id => !ownedIds.has(id));
  if (ids.length === 0) return;
  try {
    const confirmed = await library.check(ids);
    for (const id of confirmed) ownedIds.add(Number(id));
    // Patch whatever is currently rendered; a newer render may have replaced
    // the nodes already — detached patches are harmless no-ops.
    resultsEl.querySelectorAll('.qa-add').forEach(btn => {
      if (ownedIds.has(Number(btn.dataset.addId))) swapToAddBadge(btn);
    });
  } catch { /* ownership badges are cosmetic */ }
}

async function quickAdd(btn) {
  const id = Number(btn.dataset.addId);
  const name = btn.dataset.addName || 'game';
  btn.disabled = true;
  try {
    // Guard against a destructive upsert: POST /library/{id} overwrites the
    // stored item, so confirm the game isn't owned before adding.
    let owned = ownedIds.has(id);
    if (!owned) {
      const checked = await library.check([id]);
      owned = checked.length > 0;
    }
    if (owned) {
      ownedIds.add(id);
      swapToAddBadge(btn);
      showToast(`${name} is already in your library`);
      return;
    }
    await library.add(id, { status: 'backlog' });
    ownedIds.add(id);
    swapToAddBadge(btn);
    showToast(`Added ${name} to Backlog`);
  } catch (err) {
    btn.disabled = false;
    showToast(`Couldn't add ${name}: ${err.message}`, { type: 'error' });
  }
}

// --- lastTagSegment (tag autocomplete parsing) ------------------------------

// lastTagSegment finds the tag currently being typed in a raw query string.
// A double-quoted run counts as one segment even if it contains spaces/pipes,
// so typing $"Switch 2 autocompletes against "Switch 2", not "2".
// Returns { start, text } — start is where the segment begins (including any
// opening quote) so it can be replaced wholesale; text is quote-stripped.
function lastTagSegment(raw) {
  let segStart = 0;
  let quoted = false;
  for (let i = 0; i < raw.length; i++) {
    const ch = raw[i];
    if (ch === '"') {
      if (!quoted) {
        segStart = i;
        quoted = true;
      } else {
        quoted = false;
      }
    } else if (!quoted && (ch === ' ' || ch === '|')) {
      segStart = i + 1;
    }
  }
  let text = raw.slice(segStart);
  if (text.startsWith('"')) text = text.slice(1);
  if (!quoted && text.endsWith('"')) text = text.slice(0, -1);
  return { start: segStart, text };
}

// --- init -------------------------------------------------------------------

export function initSearch(inputEl, resultsEl, onSelect, onSubmit, onTagLookup) {
  activeInputEl = inputEl;

  // ARIA combobox wiring (SEARCH_IMPROVEMENTS.md §1.8). The static HTML
  // carries these too; setting them here keeps the contract in one place.
  inputEl.setAttribute('role', 'combobox');
  inputEl.setAttribute('aria-autocomplete', 'list');
  inputEl.setAttribute('aria-controls', resultsEl.id || 'searchResults');
  inputEl.setAttribute('aria-expanded', 'false');
  resultsEl.setAttribute('role', 'listbox');

  inputEl.addEventListener('input', () => {
    scheduleSearch(inputEl.value, resultsEl, onSelect, onSubmit, onTagLookup);
  });

  inputEl.addEventListener('keydown', (e) => {
    handleKeyboard(e, inputEl, resultsEl, onSelect, onSubmit, onTagLookup);
  });

  inputEl.addEventListener('focus', () => {
    if (!inputEl.value) {
      renderRecents(resultsEl);
    } else if (currentResults.length > 0) {
      openDropdown(inputEl, resultsEl);
    }
  });

  document.addEventListener('click', (e) => {
    if (!inputEl.contains(e.target) && !resultsEl.contains(e.target)) {
      closeDropdown(inputEl, resultsEl);
    }
  });

  return {
    clear() {
      inputEl.value = '';
      closeDropdown(inputEl, resultsEl);
      currentResults = [];
      selectedIndex = -1;
    },
  };
}

// initVoiceSearch wires the Web Speech API mic button (§2.3). Progressive
// enhancement: unsupported browsers get the button removed and a null return.
export function initVoiceSearch(micBtn, inputEl) {
  if (!micBtn) return null;
  const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
  if (!SR) {
    micBtn.remove();
    return null;
  }
  let recognition = null;

  micBtn.addEventListener('click', () => {
    if (recognition) {
      recognition.stop();
      return;
    }
    recognition = new SR();
    recognition.lang = navigator.language || 'en-US';
    recognition.interimResults = false;
    recognition.maxAlternatives = 1;
    micBtn.classList.add('listening');

    recognition.onresult = (e) => {
      const text = e.results?.[0]?.[0]?.transcript?.trim();
      if (text) {
        inputEl.value = text;
        inputEl.focus();
        inputEl.dispatchEvent(new Event('input'));
      }
    };
    const done = () => {
      micBtn.classList.remove('listening');
      recognition = null;
    };
    recognition.onerror = done;
    recognition.onend = done;
    recognition.start();
  });

  return SR;
}

// --- scheduling -------------------------------------------------------------

function scheduleSearch(query, resultsEl, onSelect, onSubmit, onTagLookup) {
  clearTimeout(searchTimer);
  currentQuery = query;
  selectedIndex = -1;

  // Handle $tag prefix — autocomplete tags from user's library.
  // Space-separated = AND, pipe-separated = OR.
  if (query.startsWith('$') && onTagLookup) {
    const raw = query.slice(1).trim();
    if (raw.length < 1) {
      closeDropdown(activeInputEl, resultsEl);
      currentResults = [];
      currentQuery = '';
      return;
    }

    // Extract the segment currently being typed for autocomplete
    const { text: prefix, start: segStart } = lastTagSegment(raw);

    searchTimer = setTimeout(async () => {
      renderLoading(resultsEl);
      try {
        const [tagSuggestions, items] = await Promise.all([
          autocompleteTags(prefix),
          onTagLookup(raw),
        ]);
        currentResults = items;
        selectedIndex = -1;
        renderTagSuggestions(tagSuggestions, items, resultsEl, onSelect, prefix, raw, segStart);
      } catch (err) {
        currentResults = [];
        renderTagSuggestions([], [], resultsEl, onSelect, prefix, raw, segStart);
      }
    }, SEARCH_DEBOUNCE_MS);
    return;
  }

  if (query.length === 0) {
    renderRecents(resultsEl);
    currentResults = [];
    return;
  }

  if (query.length < 2) {
    closeDropdown(activeInputEl, resultsEl);
    currentResults = [];
    return;
  }

  // Cache hit: instant render, cancel anything in flight.
  if (resultCache.has(query)) {
    if (activeController) activeController.abort();
    activeController = null;
    currentResults = resultCache.get(query);
    selectedIndex = -1;
    renderResults(currentResults, resultsEl, onSelect, onSubmit);
    return;
  }

  searchTimer = setTimeout(async () => {
    renderLoading(resultsEl);

    if (activeController) activeController.abort();
    const controller = new AbortController();
    activeController = controller;

    try {
      const results = await searchGames(query, controller.signal);
      if (controller === activeController) {
        currentResults = results;
        selectedIndex = -1;
        cacheResults(query, results);
        renderResults(results, resultsEl, onSelect, onSubmit);
      }
    } catch (err) {
      if (err.name !== 'AbortError' && controller === activeController) {
        currentResults = [];
        renderError(resultsEl, err.message);
      }
    }
  }, SEARCH_DEBOUNCE_MS);
}

function cacheResults(query, results) {
  resultCache.set(query, results);
  if (resultCache.size > RESULT_CACHE_MAX) {
    resultCache.delete(resultCache.keys().next().value);
  }
}

// --- rendering ---------------------------------------------------------------

function renderLoading(resultsEl) {
  renderedCount = 0;
  resultsEl.innerHTML =
    '<div class="search-loading"><span class="spinner"></span>Searching…</div>';
  openDropdown(activeInputEl, resultsEl);
}

function renderError(resultsEl, message) {
  renderedCount = 0;
  resultsEl.innerHTML =
    `<div class="no-results">Search failed${message ? `: ${escapeHTML(message)}` : ''}</div>`;
  openDropdown(activeInputEl, resultsEl);
}

function optionAttrs(i) {
  return `role="option" id="search-opt-${i}" aria-selected="${i === selectedIndex ? 'true' : 'false'}"`;
}

function renderRecents(resultsEl) {
  const list = getRecentSearches();
  renderedCount = 0;
  if (list.length === 0) {
    closeDropdown(activeInputEl, resultsEl);
    return;
  }
  const rows = list.map(q => `
    <button type="button" class="recent-item" data-q="${escapeHTML(q)}">
      <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true">
        <circle cx="12" cy="12" r="9"></circle><polyline points="12 7 12 12 15.5 13.5"></polyline>
      </svg>
      <span class="recent-q">${escapeHTML(q)}</span>
      <span class="recent-x" role="button" tabindex="-1" data-x="${escapeHTML(q)}" aria-label="Remove ${escapeHTML(q)}">×</span>
    </button>`).join('');

  resultsEl.innerHTML =
    '<div class="recents-header">Recent searches</div>' +
    rows +
    '<button type="button" class="recents-clear">Clear all</button>';
  openDropdown(activeInputEl, resultsEl);

  resultsEl.querySelectorAll('.recent-item').forEach(item => {
    item.addEventListener('click', (e) => {
      if (e.target.closest('.recent-x')) return;
      if (!activeInputEl) return;
      activeInputEl.value = item.dataset.q;
      activeInputEl.focus();
      activeInputEl.dispatchEvent(new Event('input'));
    });
  });
  resultsEl.querySelectorAll('.recent-x').forEach(x => {
    x.addEventListener('click', (e) => {
      e.stopPropagation();
      removeRecentSearch(x.dataset.x);
      renderRecents(resultsEl);
    });
  });
  const clearAll = resultsEl.querySelector('.recents-clear');
  if (clearAll) {
    clearAll.addEventListener('click', () => {
      clearRecentSearches();
      closeDropdown(activeInputEl, resultsEl);
    });
  }
}

function renderResults(results, resultsEl, onSelect, onSubmit) {
  if (results.length === 0) {
    renderedCount = 0;
    resultsEl.innerHTML = '<div class="no-results">No games found</div>';
  } else {
    // Slice to first 8 results for dropdown display
    const displayResults = results.slice(0, DROPDOWN_MAX);
    renderedCount = displayResults.length;
    let html = displayResults.map((g, i) => {
      const year = g.first_release_date
        ? new Date(g.first_release_date * 1000).getFullYear()
        : '';
      const id = Number(g.id);
      const owned = ownedIds.has(id);
      const action = owned
        ? ownedBadgeHTML()
        : `<button type="button" class="qa-add" data-add-id="${id}" data-add-name="${escapeHTML(g.name)}" aria-label="Add ${escapeHTML(g.name)} to backlog">+</button>`;
      // Up to three platform names, shortened ("PC (Microsoft Windows)" →
      // "PC"), so it's obvious what a result can be played on.
      const plats = (g.platforms || []).map(formatPlatformName).filter(Boolean).slice(0, 3).join(' · ');
      return `
        <div class="search-result-item${i === selectedIndex ? ' selected' : ''}"
             ${optionAttrs(i)} data-index="${i}" data-id="${id}">
          <img src="${getCoverThumbnailURL(g)}"
               alt="${escapeHTML(g.name)}" loading="lazy" decoding="async">
          <div class="info">
            <div class="name">${highlightName(g.name, currentQuery)}</div>
            <div class="year">${[year, plats].filter(Boolean).join(' · ')}</div>
          </div>
          ${action}
        </div>`;
    }).join('');

    // Add footer "See all results" row if there are results and onSubmit is provided
    if (results.length > 0 && onSubmit) {
      html += `<div class="search-result-more">See all results for "${escapeHTML(currentQuery)}" →</div>`;
    }

    resultsEl.innerHTML = html;

    attachResultHandlers(resultsEl, displayResults, results, onSelect, onSubmit);
    checkOwnership(resultsEl, displayResults);
  }

  openDropdown(activeInputEl, resultsEl);
}

function attachResultHandlers(resultsEl, displayResults, results, onSelect, onSubmit) {
  resultsEl.querySelectorAll('.qa-add').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation(); // never open the modal behind a quick-add
      quickAdd(btn);
    });
  });

  resultsEl.querySelectorAll('.search-result-item').forEach(item => {
    item.addEventListener('click', () => {
      const id = Number(item.dataset.id);
      const game = results.find(g => g.id === id);
      if (!game) return;
      closeDropdown(activeInputEl, resultsEl);
      if (onSelect) onSelect(game);
    });
  });

  // Footer "See all results" click — records the query as recent, submits.
  const footerRow = resultsEl.querySelector('.search-result-more');
  if (footerRow && onSubmit) {
    footerRow.addEventListener('click', () => {
      closeDropdown(activeInputEl, resultsEl);
      recordRecentSearch(currentQuery);
      if (onSubmit) onSubmit(currentQuery);
    });
  }
  void displayResults;
}

function renderTagSuggestions(tagSuggestions, items, resultsEl, onSelect, prefix, raw, segStart) {
  let html = '';
  renderedCount = 0;

  // Build replacement function: replaces the segment being typed with the
  // chosen tag, quoting it when it contains spaces/pipes.
  function replaceLastWord(tag) {
    return raw.slice(0, segStart) + formatTagForQuery(tag);
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
    const displayItems = items.slice(0, DROPDOWN_MAX);
    renderedCount = displayItems.length;
    html += displayItems.map((item, i) => {
      const year = item.first_release_date
        ? new Date(item.first_release_date * 1000).getFullYear()
        : '';
      return `
        <div class="search-result-item tag-result${i === selectedIndex ? ' selected' : ''}"
             ${optionAttrs(i)} data-index="${i}" data-id="${item.game_id}">
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
  openDropdown(activeInputEl, resultsEl);

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
      closeDropdown(activeInputEl, resultsEl);
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
      closeDropdown(activeInputEl, resultsEl);
      if (onSelect) onSelect(match);
    });
  });

  // Footer "Filter library" click — use the full raw string
  const footerRow = resultsEl.querySelector('.search-result-more');
  if (footerRow) {
    footerRow.addEventListener('click', () => {
      closeDropdown(activeInputEl, resultsEl);
      const event = new CustomEvent('tagfilter', { detail: { tag: raw } });
      resultsEl.dispatchEvent(event);
    });
  }
}

// --- keyboard -----------------------------------------------------------------

function handleKeyboard(e, inputEl, resultsEl, onSelect, onSubmit, onTagLookup) {
  switch (e.key) {
    case 'ArrowDown':
      if (!resultsEl.classList.contains('active') || renderedCount === 0) return;
      e.preventDefault();
      // Clamp to the RENDERED rows — ArrowDown used to index past the
      // visible items, producing an invisible selection (FINDINGS §3.6).
      selectedIndex = Math.min(selectedIndex + 1, renderedCount - 1);
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
      if (resultsEl.classList.contains('active') && selectedIndex >= 0 && selectedIndex < renderedCount) {
        closeDropdown(inputEl, resultsEl);
        if (onSelect) onSelect(currentResults[selectedIndex]);
      } else if (currentQuery && currentQuery.startsWith('$') && onTagLookup) {
        // $tag with no selection — filter library by tag(s)
        const tag = currentQuery.slice(1).trim();
        if (tag) {
          closeDropdown(inputEl, resultsEl);
          const event = new CustomEvent('tagfilter', { detail: { tag } });
          resultsEl.dispatchEvent(event);
        }
      } else if (currentQuery && currentQuery.length >= 2 && onSubmit) {
        closeDropdown(inputEl, resultsEl);
        recordRecentSearch(currentQuery);
        if (onSubmit) onSubmit(currentQuery);
      }
      break;
    case 'Escape':
      closeDropdown(inputEl, resultsEl);
      break;
  }
}

function updateSelection(resultsEl) {
  resultsEl.querySelectorAll('.search-result-item').forEach((item, i) => {
    const isSelected = i === selectedIndex;
    item.classList.toggle('selected', isSelected);
    item.setAttribute('aria-selected', isSelected ? 'true' : 'false');
    if (isSelected) {
      // Never let the selection leave the visible scroll area.
      item.scrollIntoView({ block: 'nearest' });
      if (activeInputEl) {
        activeInputEl.setAttribute('aria-activedescendant', item.id);
      }
    }
  });
}
