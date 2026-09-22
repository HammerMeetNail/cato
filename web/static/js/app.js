import { checkAuth, logout, library, getLibraryRevision } from '/js/api.js';
import { initSearch, initVoiceSearch, recordRecentSearch } from '/js/search.js';
import { closeGameModal, resumeLibraryRoute, dismissLibraryFilters, loadLibrary, loadSearchResults, addGameToLibrary, getHashStatus, getHashGameId, getHashSearch, openGameModal, openLibraryItemModal, filterByTag, filterByPlatform, refreshTabCounts } from '/js/library.js';
import { renderStatsView } from '/js/stats.js';
import { renderSettingsView } from '/js/settings.js';
import { renderPlayingView } from '/js/playing.js';

const searchInput = document.getElementById('searchInput');
const searchResults = document.getElementById('searchResults');
const userDisplay = document.getElementById('userDisplay');
const loginLink = document.getElementById('loginLink');
const userMenuBtn = document.getElementById('userMenuBtn');
const userMenuDropdown = document.getElementById('userMenuDropdown');
const logoutLink = document.getElementById('logoutLink');
const settingsLink = document.getElementById('settingsLink');
const statusTabs = document.getElementById('statusTabs');
const bottomTabs = document.getElementById('bottom-tabs');
const libraryView = document.getElementById('libraryView');
const statsView = document.getElementById('statsView');
const playingView = document.getElementById('playingView');
const settingsView = document.getElementById('settingsView');
const fabAdd = document.getElementById('fabAdd');
const libFilterFab = document.getElementById('libFilterFab');
const libFilterPanel = document.getElementById('libFilterPanel');
const statusFilterFab = document.getElementById('statusFilterFab');
const statusFilterPanel = document.getElementById('statusFilterPanel');

function updateBottomTabs(route) {
  if (!bottomTabs) return;
  bottomTabs.querySelectorAll('.tab-item').forEach(tab => {
    const r = tab.dataset.route || '';
    const active = r === route;
    tab.classList.toggle('active', active);
    if (active) tab.setAttribute('aria-current', 'page');
    else tab.removeAttribute('aria-current');
  });
}

let activeView = '';
let lastLibraryHash = '#library';
const scrollPositions = new Map();
let routeVersion = 0;

function showView(name) {
  const shell = document.querySelector('.app-shell');
  if (activeView !== name) {
    if (activeView) scrollPositions.set(activeView, shell ? shell.scrollTop : window.scrollY);
    dismissLibraryFilters();
    activeView = name;
    requestAnimationFrame(() => {
      if (activeView !== name) return;
      const top = scrollPositions.get(name) || 0;
      if (shell) shell.scrollTop = top;
      else window.scrollTo(0, top);
    });
  }
  if (libraryView) libraryView.hidden = name !== 'library';
  if (statsView) statsView.hidden = name !== 'stats';
  if (playingView) playingView.hidden = name !== 'playing';
  if (settingsView) settingsView.hidden = name !== 'settings';
  if (fabAdd) fabAdd.hidden = name === 'library';
  const allFabs = [libFilterFab, statusFilterFab];
  const allPanels = [libFilterPanel, statusFilterPanel];
  if (name !== 'library') {
    allFabs.forEach(fab => { if (fab) fab.hidden = true; });
    allPanels.forEach(panel => { if (panel) { panel.hidden = true; panel.classList.remove('lib-filter-panel--open', 'status-filter-panel--open'); } });
    const libFilterBtn = document.getElementById('libFilterBtn');
    if (libFilterBtn) libFilterBtn.classList.remove('lib-filter-btn--open');
    const statusFilterBtn = document.getElementById('statusFilterBtn');
    if (statusFilterBtn) statusFilterBtn.classList.remove('lib-filter-btn--open');
    // Also reset inline search filter buttons
    const searchStatusBtn = document.getElementById('searchStatusBtn');
    if (searchStatusBtn) { searchStatusBtn.classList.remove('lib-filter-btn--open'); searchStatusBtn.setAttribute('aria-expanded', 'false'); }
    const searchAdvancedBtn = document.getElementById('searchAdvancedBtn');
    if (searchAdvancedBtn) { searchAdvancedBtn.classList.remove('lib-filter-btn--open'); searchAdvancedBtn.setAttribute('aria-expanded', 'false'); }
  } else {
    // Library view: let library.js decide (search mode hides FAB, library mode shows)
  }
  if (name !== 'library') {
    document.getElementById('statsDialog')?.remove();
  }
}

async function handleRoute() {
  const version = ++routeVersion;
  const requestedHash = window.location.hash;
  if (!await closeGameModal(true)) return;
  if (version !== routeVersion) return;
  const raw = requestedHash.slice(1);
  if (raw === 'stats') {
    updateBottomTabs('stats');
    showView('stats');
    await renderStatsView(statsView);
    return;
  }
  if (raw === 'settings') {
    updateBottomTabs('settings');
    showView('settings');
    await renderSettingsView(settingsView);
    return;
  }
  // Library: explicit #library, status tabs (#backlog etc.), or #game/#search
  if (raw === 'library' || getHashSearch() !== null || getHashGameId() !== null || getHashStatus() !== '') {
    if (!raw.startsWith('game/')) lastLibraryHash = '#' + raw;
    updateBottomTabs('library');
    showView('library');
    const searchQuery = getHashSearch();
    if (searchQuery) {
      searchInput.value = searchQuery;
      recordRecentSearch(searchQuery);
      await resumeLibraryRoute(searchQuery);
      return;
    }
    const status = getHashStatus();
    const gameId = getHashGameId();
    await resumeLibraryRoute(null, status);
    if (gameId && version === routeVersion) {
      openGameModal(gameId, () => version === routeVersion);
    }
    return;
  }
  if (raw === 'now' || raw === 'now-playing' || raw === 'playing-now' || raw === 'playing' || raw === 'library/playing') {
    updateBottomTabs('now');
    showView('playing');
    await renderPlayingView(playingView);
    return;
  }

  // Default: Playing (stats | library | playing | settings, playing is home)
  updateBottomTabs('now');
  showView('playing');
  await renderPlayingView(playingView);
}

// Service worker registration (PWA) – keep cache fresh, show update toast.
if ('serviceWorker' in navigator) {
  navigator.serviceWorker.register('/service-worker.js').then(reg => {
    window.__catoSW = reg;
  }).catch(() => {});
  let hadController = !!navigator.serviceWorker.controller;
  let refreshing = false;
  navigator.serviceWorker.addEventListener('controllerchange', () => {
    if (refreshing) return;
    if (!hadController) { hadController = true; return; }
    const container = document.getElementById('toast-container');
    if (!container) return;
    const toast = document.createElement('div');
    toast.className = 'toast toast-info';
    toast.style.cssText = 'display:flex;align-items:center;gap:8px;background:var(--accent);color:#fff;padding:10px 14px;border-radius:8px;';
    const label = document.createElement('span');
    label.textContent = 'Update available';
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.textContent = 'Refresh';
    btn.style.cssText = 'margin-left:auto;background:rgba(255,255,255,0.2);border:none;color:#fff;padding:4px 10px;border-radius:6px;cursor:pointer;font-weight:600;';
    btn.addEventListener('click', () => { refreshing = true; window.location.reload(); });
    toast.append(label, btn);
    container.appendChild(toast);
    setTimeout(() => { if (!refreshing) toast.remove(); }, 30000);
  });
}

const auth = await checkAuth();

if (!auth.authenticated) {
  window.location.href = '/login' + window.location.hash;
  throw new Error('Not authenticated');
}

if (auth.display_name) {
  userDisplay.textContent = auth.display_name;
} else {
  userDisplay.style.display = 'none';
}
document.getElementById('userMenu').style.display = '';
loginLink.style.display = 'none';
if (bottomTabs) bottomTabs.hidden = false;

function setUserMenu(open) {
  userMenuDropdown.hidden = !open;
  userMenuBtn.setAttribute('aria-expanded', String(open));
}

userMenuBtn.addEventListener('click', () => setUserMenu(userMenuDropdown.hidden));

document.addEventListener('click', (e) => {
  if (e.target.closest('.skip-link')) {
    e.preventDefault();
    document.getElementById('mainContainer')?.focus({ preventScroll: true });
    return;
  }
  if (!userMenuDropdown.hidden && !e.target.closest('.user-menu')) setUserMenu(false);
  // Bottom tabs are anchors; prevent full-page navigation and use SPA hash.
  // Playing is default (empty hash), others use explicit hashes.
  const navEl = e.target.closest('#bottom-tabs .tab-item');
  if (navEl) {
    e.preventDefault();
    const route = navEl.dataset.route;
    if (activeView === 'library' && !window.location.hash.startsWith('#game/')) lastLibraryHash = window.location.hash || '#library';
    window.location.hash = route === 'library' ? lastLibraryHash : '#' + route;
  }
});
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && !userMenuDropdown.hidden) {
    e.stopPropagation();
    setUserMenu(false);
  }
});

if (settingsLink) {
  settingsLink.addEventListener('click', () => setUserMenu(false));
}

document.addEventListener('keydown', (e) => {
  if (e.key !== '/' || e.ctrlKey || e.metaKey) return;
  const tag = document.activeElement?.tagName;
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
  e.preventDefault();
  if (libraryView.hidden) {
    window.location.hash = '#library';
    setTimeout(() => searchInput.focus(), 0);
  } else {
    searchInput.focus();
  }
});

fabAdd?.addEventListener('click', () => {
  // Keep focus in the same user gesture so iOS opens the keyboard.
  const needNav = libraryView.hidden;
  if (needNav) {
    // Make Library visible synchronously so the input is focusable in this tick.
    // Update hash without waiting for the async hashchange -> handleRoute cycle.
    try { history.replaceState(null, '', window.location.pathname + window.location.search + '#library'); } catch {}
    updateBottomTabs('library');
    showView('library');
    // Kick off data load in background (don't await — focus must stay synchronous).
    handleRoute().catch(() => {});
  }
  // Focus synchronously (preventScroll keeps iOS from blurring on scroll).
  try { searchInput.focus({ preventScroll: true }); } catch { searchInput.focus(); }
  // Then bring the search field into view smoothly.
  requestAnimationFrame(() => {
    try { searchInput.scrollIntoView({ behavior: 'smooth', block: 'center' }); } catch { window.scrollTo({ top: 0, behavior: 'smooth' }); }
  });
});

const statsStrip = document.getElementById('statsStrip');
if (statsStrip) {
  statsStrip.setAttribute('role', 'button');
  statsStrip.setAttribute('tabindex', '0');
  statsStrip.title = 'View detailed stats';
  const goStats = () => {
    if (statsStrip.dataset.context !== 'library') return;
    window.location.hash = '#stats';
  };
  statsStrip.addEventListener('click', goStats);
  statsStrip.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); goStats(); }
  });
}

logoutLink.addEventListener('click', async (e) => {
  e.preventDefault();
  await logout();
  window.location.href = '/login';
});

initSearch(searchInput, searchResults, (game) => {
  if (game.status) {
    openLibraryItemModal(game);
  } else {
    addGameToLibrary(game);
  }
  searchInput.value = '';
  searchInput.blur();
}, (query) => {
  if (query.startsWith('$')) {
    const tag = query.slice(1).trim();
    if (tag) {
      searchInput.value = '';
      searchInput.blur();
      recordRecentSearch(query);
      filterByTag(tag);
      return;
    }
  }
  if (query.startsWith('@')) {
    const platform = query.slice(1).trim();
    if (platform) {
      searchInput.value = '';
      searchInput.blur();
      recordRecentSearch(query);
      filterByPlatform(platform);
      return;
    }
  }
  searchInput.blur();
  window.location.hash = '#search/' + encodeURIComponent(query);
}, async (tag) => {
  try {
    return await library.list('', 8, 0, tag);
  } catch {
    return [];
  }
});

initVoiceSearch(document.getElementById('voiceBtn'), searchInput);

document.getElementById('searchClear')?.addEventListener('click', () => {
  searchInput.value = '';
  searchInput.focus();
  searchInput.dispatchEvent(new Event('input'));
});

searchResults.addEventListener('tagfilter', (e) => {
  recordRecentSearch('$' + e.detail.tag);
  searchInput.value = '';
  searchInput.blur();
  filterByTag(e.detail.tag);
});

searchResults.addEventListener('platformfilter', (e) => {
  recordRecentSearch('@' + e.detail.platform);
  searchInput.value = '';
  searchInput.blur();
  filterByPlatform(e.detail.platform);
});

// Status tabs: accessible tablist handling
statusTabs.querySelectorAll('.tab').forEach(tab => {
  tab.addEventListener('click', async () => {
    const status = tab.dataset.status || '';
    // Update aria-selected for accessibility
    statusTabs.querySelectorAll('.tab').forEach(t => t.setAttribute('aria-selected', String(t === tab)));
    if (status) {
      window.location.hash = '#' + status;
    } else {
      history.replaceState(null, '', window.location.pathname + window.location.search + '#library');
      searchInput.value = '';
      await handleRoute();
    }
  });
});

window.addEventListener('hashchange', handleRoute);
await handleRoute();

// Keyboard shortcut for bottom tabs: 1=Library, 2=Playing, 3=Stats, 4=Settings
document.addEventListener('keydown', (e) => {
  if (e.altKey || e.ctrlKey || e.metaKey) return;
  const tag = document.activeElement?.tagName;
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
  if (e.key === '3') { e.preventDefault(); window.location.hash = '#stats'; }
  if (e.key === '1') { e.preventDefault(); window.location.hash = '#library'; }
  if (e.key === '2') {
    e.preventDefault();
    window.location.hash = '#now';
  }
  if (e.key === '4') { e.preventDefault(); window.location.hash = '#settings'; }
});
