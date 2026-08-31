import { searchGames, getCoverThumbnailURL, autocompleteTags, autocompletePlatforms, autocompleteGlobalPlatforms, formatTagForQuery, library } from './api.js';
import { escapeHTML, showToast, formatPlatformName, statusBadgeLabel, refreshTabCounts, refreshSearchResults, refreshLibraryView } from './library.js';
import { releaseLabel, releaseStatus } from './dates.js';

const CONTEMPORARY_RANK = {
  'ps5': 0, 'playstation 5': 0,
  'ps4': 1, 'playstation 4': 1,
  'ps3': 2, 'playstation 3': 2,
  'ps2': 3, 'playstation 2': 3,
  'ps1': 4, 'playstation': 4, 'psx': 4,
  'xbox series x|s': 5, 'xsx': 5, 'xss': 5,
  'nintendo switch 2': 6, 'sw2': 6, 'switch 2': 6, 'ns2': 6,
  'nintendo switch': 7, 'switch': 7,
  'xbox one': 8, 'xb1': 8,
  'xbox 360': 9, 'x360': 9,
  'pc (microsoft windows)': 10, 'win': 10,
};
function rankPlatformSuggestions(list, query) {
  const q = String(query || '').trim().toLowerCase();
  if (!q || !Array.isArray(list) || list.length === 0) return list;
  const isShort = q.length <= 2;
  return [...list].sort((a, b) => {
    const al = String(a).toLowerCase(), bl = String(b).toLowerCase();
    const ar = CONTEMPORARY_RANK[al] ?? 99, br = CONTEMPORARY_RANK[bl] ?? 99;
    if (isShort && ar !== br) return ar - br;
    const aPrefix = al.startsWith(q) ? 0 : 1, bPrefix = bl.startsWith(q) ? 0 : 1;
    if (aPrefix !== bPrefix) return aPrefix - bPrefix;
    if (ar !== br) return ar - br;
    return 0;
  });
}

// Tuning knobs: 200ms feels responsive without hammering the API; the
// dropdown renders at most 8 rows; the client-side result cache makes
// backspacing instant without any network round-trip.
const SEARCH_DEBOUNCE_MS = 200;
const DROPDOWN_MAX = 8;
const RESULT_CACHE_MAX = 50;
const RECENTS_KEY = 'cato-recent-searches';
const RECENTS_MAX = 5;

let searchTimer = null;
let activeController = null;
let selectedIndex = -1;
let currentResults = [];
let currentQuery = '';
let activeInputEl = null;
let activeOnSubmit = null;
// How many result rows are actually rendered in the dropdown. The games list
// renders only the first 8 results; ArrowDown is clamped to visible rows.
let renderedCount = 0;

// resultCache maps raw query -> results array (per page session). Serving a
// repeat query from memory avoids both latency and an IGDB round-trip.
const resultCache = new Map();
// ownedStatuses caches game IDs confirmed to be in the user's library,
// mapped to their status, so badges say WHICH list the game is in ("Completed ✓").
// Positives only — a missing ID just means "unknown", never "confirmed absent".
const ownedStatuses = new Map();

const QUICK_ADD_STATUSES = ['wishlist', 'backlog', 'playing', 'completed', 'abandoned'];
const QUICK_ADD_LABELS = {
  wishlist: 'Wishlist',
  backlog: 'Backlog',
  playing: 'Playing',
  completed: 'Completed',
  abandoned: 'Abandoned',
};
const QUICK_ADD_ICONS = {
  wishlist: '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M19 21l-7-5-7 5V5a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2z"></path></svg>',
  backlog: '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><circle cx="12" cy="12" r="9"></circle><polyline points="12 7 12 12 15 13"></polyline></svg>',
  playing: '<svg viewBox="0 0 24 24" width="13" height="13" fill="currentColor" stroke="none" aria-hidden="true"><polygon points="5 3 19 12 5 21 5 3"></polygon></svg>',
  completed: '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polyline points="20 6 9 17 4 12"></polyline></svg>',
  abandoned: '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><line x1="18" y1="6" x2="6" y2="18"></line><line x1="6" y1="6" x2="18" y2="18"></line></svg>',
};

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
  if (!q) return;
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

function getSearchBackdrop() {
  return document.getElementById('searchBackdrop');
}

function openDropdown(inputEl, resultsEl) {
  resultsEl.classList.add('active');
  inputEl.setAttribute('aria-expanded', 'true');
  const backdrop = getSearchBackdrop();
  if (backdrop) {
    backdrop.hidden = false;
    // Use rAF so the transition from opacity 0 to 1 is animated
    requestAnimationFrame(() => backdrop.classList.add('search-backdrop--open'));
  }
}

function closeDropdown(inputEl, resultsEl) {
  resultsEl.classList.remove('active');
  inputEl.setAttribute('aria-expanded', 'false');
  inputEl.removeAttribute('aria-activedescendant');
  const backdrop = getSearchBackdrop();
  if (backdrop) {
    backdrop.classList.remove('search-backdrop--open');
    // Delay hidden for transition (180ms matches CSS)
    setTimeout(() => {
      if (!backdrop.classList.contains('search-backdrop--open')) {
        backdrop.hidden = true;
      }
    }, 180);
  }
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

function ownedBadgeHTML(status) {
  // The status class adopts the library pill color for that list (see the
  // shared .status-* rules in app.css); unknown statuses keep the accent.
  const cls = status ? ` status-${escapeHTML(status)}` : '';
  return `<span class="owned-badge owned-badge-sm${cls}">${escapeHTML(statusBadgeLabel(status))}</span>`;
}

function quickAddButtonsHTML(id, name) {
  const safeName = escapeHTML(name);
  return `<div class="qa-actions" role="group" aria-label="Add ${safeName} to library">` +
    QUICK_ADD_STATUSES.map(s =>
      `<button type="button" class="qa-add qa-add--${s}" data-add-id="${id}" data-add-name="${safeName}" data-add-status="${s}" aria-label="Add ${safeName} to ${QUICK_ADD_LABELS[s]}" title="${QUICK_ADD_LABELS[s]}">${QUICK_ADD_ICONS[s]}</button>`
    ).join('') +
    `</div>`;
}

// swapToAddBadge replaces the quick-add controls (single button or group)
// with the ownership badge, labeled by the game's status from the ownedStatuses cache.
function swapToAddBadge(el) {
  if (!el || !el.isConnected) return;
  const container = el.closest ? (el.closest('.qa-actions') || el) : el;
  if (!container.isConnected) return;
  if (container.dataset.swapped) return;
  const idAttr = container.dataset.addId || el.dataset.addId || (container.querySelector && container.querySelector('.qa-add')?.dataset.addId);
  const id = Number(idAttr);
  if (!Number.isFinite(id)) return;
  container.dataset.swapped = '1';
  container.insertAdjacentHTML('beforebegin', ownedBadgeHTML(ownedStatuses.get(id)));
  container.remove();
}

// checkOwnership batch-confirms which dropdown rows are already in the
// library, then patches their rows: quick-add controls flip to a status badge
// ("Completed ✓", "Wishlist ✓", …).
async function checkOwnership(resultsEl, results) {
  const ids = results.map(g => Number(g.id)).filter(id => !ownedStatuses.has(id));
  if (ids.length === 0) return;
  try {
    const confirmed = await library.check(ids);
    for (const it of confirmed) ownedStatuses.set(Number(it.game_id), it.status);
    // Patch whatever is currently rendered; a newer render may have replaced
    // the nodes already — detached patches are harmless no-ops.
    resultsEl.querySelectorAll('.qa-add').forEach(btn => {
      if (ownedStatuses.has(Number(btn.dataset.addId))) swapToAddBadge(btn);
    });
  } catch { /* ownership badges are cosmetic */ }
}

async function quickAdd(btn) {
  const id = Number(btn.dataset.addId);
  const name = btn.dataset.addName || 'game';
  const rawStatus = btn.dataset.addStatus || 'backlog';
  const targetStatus = QUICK_ADD_STATUSES.includes(rawStatus) ? rawStatus : 'backlog';
  const label = QUICK_ADD_LABELS[targetStatus] || 'library';
  const actions = btn.closest ? btn.closest('.qa-actions') : null;
  const toDisable = actions ? actions.querySelectorAll('.qa-add') : [btn];
  toDisable.forEach(b => b.disabled = true);
  try {
    // Guard against a destructive upsert: POST /library/{id} overwrites the
    // stored item, so confirm the game isn't owned before adding.
    let owned = ownedStatuses.has(id);
    if (!owned) {
      const checked = await library.check([id]);
      if (checked.length > 0) {
        ownedStatuses.set(id, checked[0].status);
        owned = true;
      }
    }
    if (owned) {
      swapToAddBadge(btn);
      showToast(`${name} is already in your library`);
      return;
    }
    await library.add(id, { status: targetStatus });
    ownedStatuses.set(id, targetStatus);
    swapToAddBadge(btn);
    showToast(`Added ${name} to ${label}`);
    refreshTabCounts().catch(() => {});
    refreshSearchResults().catch(() => {});
    refreshLibraryView().catch(() => {});
  } catch (err) {
    toDisable.forEach(b => b.disabled = false);
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
  activeOnSubmit = onSubmit;
  const searchRegion = inputEl.closest('.search-wrap') || inputEl;
  const dismissSearchFocus = () => {
    closeDropdown(inputEl, resultsEl);
    inputEl.blur();
  };
  let suppressOutsideClick = false;
  let suppressOutsideClickTimer = null;
  const isOutsideSearch = (target) =>
    !searchRegion.contains(target) && !resultsEl.contains(target);
  // Navigation chrome (bottom tabs, topbar) should never be blocked by the
  // search outside handler — tapping them while search is focused should
  // dismiss the dropdown and still navigate.
  const isNavChrome = (target) =>
    !!(target.closest && (target.closest('#bottom-tabs') || target.closest('.topbar')));
  const blockOutsidePointer = (e) => {
    if (!isOutsideSearch(e.target) || document.activeElement !== inputEl) return;
    if (isNavChrome(e.target)) {
      // Dismiss search but let the navigation click proceed.
      suppressOutsideClick = true;
      clearTimeout(suppressOutsideClickTimer);
      suppressOutsideClickTimer = setTimeout(() => { suppressOutsideClick = false; }, 500);
      dismissSearchFocus();
      return;
    }
    // Handle the gesture before a touch/mouse can blur the input and then
    // deliver the same tap to a game card underneath the search UI.
    e.preventDefault();
    e.stopPropagation();
    suppressOutsideClick = true;
    clearTimeout(suppressOutsideClickTimer);
    suppressOutsideClickTimer = setTimeout(() => { suppressOutsideClick = false; }, 500);
    dismissSearchFocus();
  };
  // Pointer/touch down is the earliest reliable point to consume the gesture;
  // mousedown keeps the guard working in browsers without Pointer Events.
  document.addEventListener('pointerdown', blockOutsidePointer, { capture: true, passive: false });
  document.addEventListener('touchstart', blockOutsidePointer, { capture: true, passive: false });
  document.addEventListener('mousedown', blockOutsidePointer, { capture: true, passive: false });

  // Search backdrop — like the library filter backdrop, it sits above the
  // game grid but below the dropdown/chrome and captures outside taps so a
  // tap on a game card dismisses the search instead of opening the game.
  const searchBackdrop = getSearchBackdrop();
  if (searchBackdrop && !searchBackdrop.dataset.wired) {
    searchBackdrop.dataset.wired = '1';
    searchBackdrop.addEventListener('click', (e) => {
      e.preventDefault();
      e.stopPropagation();
      dismissSearchFocus();
    });
  }

  // ARIA combobox wiring. The static HTML carries these too; setting them
  // here keeps the contract in one place.
  inputEl.setAttribute('role', 'combobox');
  inputEl.setAttribute('aria-autocomplete', 'list');
  inputEl.setAttribute('aria-controls', resultsEl.id || 'searchResults');
  inputEl.setAttribute('aria-expanded', 'false');
  resultsEl.setAttribute('role', 'listbox');
  resultsEl.addEventListener('mousedown', (e) => {
    // Keep the input focused while a dropdown control is being clicked. This
    // prevents the focused layout from reflowing before its click handler.
    if (e.target.closest && e.target.closest('.search-result-item, .search-result-more, .tag-suggestion-chip, .qa-add, .qa-actions')) {
      e.preventDefault();
    }
  });

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
    if (!isOutsideSearch(e.target)) {
      suppressOutsideClick = false;
      clearTimeout(suppressOutsideClickTimer);
      return;
    }
    if (isNavChrome(e.target)) {
      suppressOutsideClick = false;
      clearTimeout(suppressOutsideClickTimer);
      dismissSearchFocus();
      return;
    }
    if (suppressOutsideClick) {
      suppressOutsideClick = false;
      clearTimeout(suppressOutsideClickTimer);
      e.preventDefault();
      e.stopPropagation();
      dismissSearchFocus();
      return;
    }
    if (document.activeElement === inputEl) {
      // Consume the first outside click so a focused search cannot pass the
      // tap through to a game card or another background control.
      e.preventDefault();
      e.stopPropagation();
      dismissSearchFocus();
      return;
    }
    closeDropdown(inputEl, resultsEl);
  }, true);

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

  // Handle @platform prefix — autocomplete platform names from the user's
  // library, then filter it (Enter or the footer row). Unlike $tags,
  // everything after @ is ONE phrase: platforms don't combine with AND/OR,
  // so multi-word names ("switch 2") need no quoting or segmentation.
  if (query.startsWith('@')) {
    const raw = query.slice(1).trim();
    if (raw.length < 1) {
      closeDropdown(activeInputEl, resultsEl);
      currentResults = [];
      currentQuery = '';
      return;
    }

    searchTimer = setTimeout(async () => {
      renderLoading(resultsEl);
      try {
        // Prefer global contemporary ranking (ps5/ps4 for "ps"), fallback to library
        let suggestions = [];
        try { suggestions = await autocompleteGlobalPlatforms(raw); } catch {}
        if (!suggestions || suggestions.length === 0) {
          try { suggestions = await autocompletePlatforms(raw); } catch {}
        }
        suggestions = rankPlatformSuggestions(suggestions, raw);
        currentResults = [];
        selectedIndex = -1;
        renderPlatformSuggestions(suggestions, resultsEl, raw);
      } catch {
        currentResults = [];
        renderPlatformSuggestions([], resultsEl, raw);
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
    <div class="recent-item" data-q="${escapeHTML(q)}">
      <button type="button" class="recent-item-main" data-q="${escapeHTML(q)}">
        <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true">
          <circle cx="12" cy="12" r="9"></circle><polyline points="12 7 12 12 15.5 13.5"></polyline>
        </svg>
        <span class="recent-q">${escapeHTML(q)}</span>
      </button>
      <button type="button" class="recent-x" data-x="${escapeHTML(q)}" aria-label="Remove ${escapeHTML(q)}">×</button>
    </div>`).join('');

  resultsEl.innerHTML =
    '<div class="recents-header">Recent searches</div>' +
    rows +
    '<button type="button" class="recents-clear">Clear all</button>';
  openDropdown(activeInputEl, resultsEl);

  resultsEl.querySelectorAll('.recent-item-main').forEach(btn => {
    btn.addEventListener('click', () => {
      const q = btn.dataset.q;
      if (!q) return;
      clearTimeout(searchTimer);
      if (activeController) { activeController.abort(); activeController = null; }
      closeDropdown(activeInputEl, resultsEl);
      activeInputEl?.blur();
      if (activeOnSubmit) {
        recordRecentSearch(q);
        activeOnSubmit(q);
      } else if (activeInputEl) {
        activeInputEl.value = q;
        activeInputEl.focus();
        activeInputEl.dispatchEvent(new Event('input', { bubbles: true }));
        if (q.length >= 2) {
          recordRecentSearch(q);
          window.location.hash = '#search/' + encodeURIComponent(q);
        }
      }
    });
    // Keep focus from leaving the input on mousedown — without this the
    // input blurs before the click fires and some browsers suppress the click,
    // making the panel appear to ignore taps.
    btn.addEventListener('mousedown', (e) => e.preventDefault());
  });
  resultsEl.querySelectorAll('.recent-x').forEach(x => {
    x.addEventListener('click', (e) => {
      e.stopPropagation();
      removeRecentSearch(x.dataset.x);
      renderRecents(resultsEl);
      if (activeInputEl) activeInputEl.focus();
    });
    x.addEventListener('mousedown', (e) => e.preventDefault());
  });
  const clearAll = resultsEl.querySelector('.recents-clear');
  if (clearAll) {
    clearAll.addEventListener('click', () => {
      clearRecentSearches();
      closeDropdown(activeInputEl, resultsEl);
      if (activeInputEl) activeInputEl.focus();
    });
    clearAll.addEventListener('mousedown', (e) => e.preventDefault());
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
      const release = releaseLabel(g.first_release_date);
      const relStatus = releaseStatus(g.first_release_date);
      const id = Number(g.id);
      const ownedStatus = ownedStatuses.get(id);
      const action = ownedStatus !== undefined
        ? ownedBadgeHTML(ownedStatus)
        : quickAddButtonsHTML(id, g.name);
      // Up to three platform names, shortened ("PC (Microsoft Windows)" →
      // "PC"), so it's obvious what a result can be played on.
      const plats = (g.platforms || []).map(formatPlatformName).filter(Boolean).slice(0, 3).join(' · ');
      return `
        <div class="search-result-item${i === selectedIndex ? ' selected' : ''}"
             ${optionAttrs(i)} data-index="${i}" data-id="${id}">
          <img src="${getCoverThumbnailURL(g)}"
               alt="${escapeHTML(g.name)}" loading="lazy" decoding="async"
               onerror="this.onerror=null;this.src='/covers/${id}.jpg'">
          <div class="info">
            <div class="name">${highlightName(g.name, currentQuery)}</div>
            <div class="year release-${relStatus}">${[release, plats].filter(Boolean).join(' · ')}</div>
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
      activeInputEl?.blur();
      if (onSelect) onSelect(game);
    });
  });

  // Footer "See all results" click — records the query as recent, submits.
  const footerRow = resultsEl.querySelector('.search-result-more');
  if (footerRow && onSubmit) {
    footerRow.addEventListener('click', () => {
      closeDropdown(activeInputEl, resultsEl);
      recordRecentSearch(currentQuery);
      activeInputEl?.blur();
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
      const release = releaseLabel(item.first_release_date);
      const relStatus = releaseStatus(item.first_release_date);
      return `
        <div class="search-result-item tag-result${i === selectedIndex ? ' selected' : ''}"
             ${optionAttrs(i)} data-index="${i}" data-id="${item.game_id}">
          <img src="${getCoverThumbnailURL(item)}"
               alt="${escapeHTML(item.game_name)}" loading="lazy" decoding="async"
               onerror="this.onerror=null;this.src='/covers/${item.game_id}.jpg'">
          <div class="info">
            <div class="name">${escapeHTML(item.game_name)}</div>
            <div class="year release-${relStatus}">${escapeHTML(release)} · ${escapeHTML(item.status)}</div>
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
      activeInputEl?.blur();
      if (onSelect) onSelect(match);
    });
  });

  // Footer "Filter library" click — use the full raw string
  const footerRow = resultsEl.querySelector('.search-result-more');
  if (footerRow) {
    footerRow.addEventListener('click', () => {
      closeDropdown(activeInputEl, resultsEl);
      activeInputEl?.blur();
      recordRecentSearch('$' + raw);
      const event = new CustomEvent('tagfilter', { detail: { tag: raw } });
      resultsEl.dispatchEvent(event);
    });
  }
}

 // renderPlatformSuggestions shows @prefix autocomplete chips plus a footer
// that applies the library platform filter (mirrors the $tag flow; no live
// item list — the filtered library view is the feedback). raw is everything
// after '@' — one phrase, no segmentation.
function renderPlatformSuggestions(suggestions, resultsEl, raw) {
  renderedCount = 0;
  let html = '';

  if (suggestions.length > 0) {
    html += '<div class="tag-suggestions">';
    html += suggestions.map(p => {
      const matchStart = p.toLowerCase().indexOf(raw.toLowerCase());
      const before = p.slice(0, matchStart);
      const match = p.slice(matchStart, matchStart + raw.length);
      const after = p.slice(matchStart + raw.length);
      return `<span class="tag-suggestion-chip" data-plat="${escapeHTML(p)}">${escapeHTML(before)}<strong>${escapeHTML(match)}</strong>${escapeHTML(after)}</span>`;
    }).join('');
    html += '</div>';
  } else {
    html += `<div class="no-results">No platforms in your library matching "${escapeHTML(raw)}"</div>`;
  }

  html += `<div class="search-result-more">Filter library by "@${escapeHTML(raw)}" →</div>`;

  resultsEl.innerHTML = html;
  openDropdown(activeInputEl, resultsEl);

  // Chip click — swap the typed phrase for the full platform name,
  // re-triggering autocomplete against the completed name.
  resultsEl.querySelectorAll('.tag-suggestion-chip').forEach(chip => {
    chip.addEventListener('click', () => {
      const inputEl = document.getElementById('searchInput');
      if (inputEl) {
        inputEl.value = '@' + chip.dataset.plat;
        inputEl.focus();
        inputEl.dispatchEvent(new Event('input'));
      }
    });
  });

  const footerRow = resultsEl.querySelector('.search-result-more');
  if (footerRow) {
    footerRow.addEventListener('click', () => {
      closeDropdown(activeInputEl, resultsEl);
      activeInputEl?.blur();
      recordRecentSearch('@' + raw);
      resultsEl.dispatchEvent(new CustomEvent('platformfilter', { detail: { platform: raw } }));
    });
  }
}

// --- keyboard -----------------------------------------------------------------

function handleKeyboard(e, inputEl, resultsEl, onSelect, onSubmit, onTagLookup) {
  switch (e.key) {
    case 'ArrowDown':
      if (!resultsEl.classList.contains('active') || renderedCount === 0) return;
      e.preventDefault();
      // Clamp to the RENDERED rows — don't let ArrowDown index past visible
      // items.
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
        inputEl.blur();
        if (onSelect) onSelect(currentResults[selectedIndex]);
      } else if (currentQuery && currentQuery.startsWith('$') && onTagLookup) {
        // $tag with no selection — filter library by tag(s)
        const tag = currentQuery.slice(1).trim();
        if (tag) {
          closeDropdown(inputEl, resultsEl);
          inputEl.blur();
          recordRecentSearch(currentQuery);
          const event = new CustomEvent('tagfilter', { detail: { tag } });
          resultsEl.dispatchEvent(event);
        }
      } else if (currentQuery && currentQuery.startsWith('@')) {
        // @platform with no selection — filter library by platform
        const platform = currentQuery.slice(1).trim();
        if (platform) {
          closeDropdown(inputEl, resultsEl);
          inputEl.blur();
          recordRecentSearch(currentQuery);
          const event = new CustomEvent('platformfilter', { detail: { platform } });
          resultsEl.dispatchEvent(event);
        }
      } else if (currentQuery && currentQuery.length >= 2 && onSubmit) {
        closeDropdown(inputEl, resultsEl);
        recordRecentSearch(currentQuery);
        inputEl.blur();
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
