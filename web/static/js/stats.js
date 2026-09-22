import { api, getLibraryRevision } from './api.js';
import { escapeHTML } from './library.js';

// Stats: lifetime totals, this-year activity, finished-per-year bars,
// top tags/platforms, and recent updates. Rendered as a full view in the
// SPA (previously a modal dialog).

async function fetchStats() {
  return api.get('/api/library/stats');
}

function relTime(iso) {
  if (!iso) return '';
  const then = new Date(iso.replace(' ', 'T')).getTime();
  if (isNaN(then)) return '';
  const days = Math.floor((Date.now() - then) / 86400000);
  if (days <= 0) return 'today';
  if (days === 1) return 'yesterday';
  if (days < 30) return `${days}d ago`;
  return iso.slice(0, 10);
}

function fmtHours(minutes) {
  const h = Math.round((minutes / 60) * 10) / 10;
  return h === 1 ? '1h' : `${h}h`;
}

function statCell(value, label) {
  return `<div class="stat-cell"><div class="stat-value">${escapeHTML(String(value))}</div><div class="stat-label">${label}</div></div>`;
}

function buildStatsHTML(s) {
  const totalGames = s.total_games || 0;
  const finished = s.total_finished || 0;
  const minutes = s.total_minutes || 0;
  const avg = s.avg_rating || 0;

  const byYear = (s.by_year || []);
  const maxYear = Math.max(1, ...byYear.map(y => y.count || 0));
  const yearRows = byYear.map(y => `
    <div class="stat-bar-row">
      <span class="stat-bar-year">${escapeHTML(y.year)}</span>
      <span class="stat-bar-track"><span class="stat-bar-fill" style="width:${Math.round((y.count || 0) / maxYear * 100)}%"></span></span>
      <span class="stat-bar-count">${y.count}</span>
    </div>`).join('');

  const tagsHTML = (s.top_tags || []).map(t =>
    `<span class="tag-chip">${escapeHTML(t.tag)} · ${t.count}</span>`).join('');

  const platformsHTML = (s.top_platforms || []).map(p => `
    <div class="stat-list-row"><span>${escapeHTML(p.platform)}</span><span class="stat-dim">${p.count} ${p.count === 1 ? 'game' : 'games'}</span></div>`).join('');

  const statusLabels = {
    wishlist: 'Wishlist', backlog: 'Backlog', playing: 'Playing',
    completed: 'Finished', abandoned: 'Abandoned',
  };
  const recentHTML = (s.recent || []).map(r => `
    <button type="button" class="stat-recent-row" data-game-id="${r.game_id}">
      <span class="stat-recent-name">${escapeHTML(r.game_name)}</span>
      <span class="stat-dim">${statusLabels[r.status] || escapeHTML(r.status)} · ${relTime(r.updated_at)}</span>
    </button>`).join('');

  const year = new Date().getFullYear();
  const thisYearBits = [];
  if ((s.started_this_year || 0) > 0) thisYearBits.push(`${s.started_this_year} started`);
  if ((s.finished_this_year || 0) > 0) thisYearBits.push(`${s.finished_this_year} finished`);
  if ((s.added_this_year || 0) > 0) thisYearBits.push(`${s.added_this_year} added`);

  if (totalGames === 0) {
    return `<div class="stats-page"><h2>Library in numbers</h2><div class="empty-state"><p>Add some games and your stats will live here.</p></div></div>`;
  }
  return `
    <div class="stats-page">
      <h2>Library in numbers</h2>
      <div class="stat-grid">
        ${statCell(totalGames, 'games')}
        ${statCell(finished, 'finished')}
        ${minutes > 0 ? statCell(fmtHours(minutes), 'logged') : ''}
        ${avg > 0 ? statCell(avg.toFixed(1), 'avg rating') : ''}
      </div>
      ${thisYearBits.length ? `<div class="stat-year-line">In ${year}: ${escapeHTML(thisYearBits.join(' · '))}</div>` : ''}
      ${yearRows ? `
        <div class="stat-section">
          <h3>Finished by year</h3>
          ${yearRows}
        </div>` : ''}
      ${tagsHTML ? `
        <div class="stat-section">
          <h3>Most-used tags</h3>
          <div class="stat-tags">${tagsHTML}</div>
        </div>` : ''}
      ${platformsHTML ? `
        <div class="stat-section">
          <h3>Platforms</h3>
          ${platformsHTML}
        </div>` : ''}
      ${recentHTML ? `
        <div class="stat-section">
          <h3>Recent updates</h3>
          <div class="stat-recent">${recentHTML}</div>
        </div>` : ''}
    </div>`;
}

const statsViews = new WeakMap();

export function renderStatsView(container) {
  if (!container) return Promise.resolve();
  let state = statsViews.get(container);
  if (!state) { state = { revision: -1, pending: null }; statsViews.set(container, state); }
  if (state.pending) return state.pending;
  if (state.revision === getLibraryRevision()) return Promise.resolve();
  state.pending = loadStatsView(container, state).finally(() => { state.pending = null; });
  return state.pending;
}

async function loadStatsView(container, state) {
  if (state.revision < 0) container.innerHTML = '<div class="stats-page"><div class="loading">Loading stats…</div></div>';
  try {
    // A mutation during the request makes that snapshot obsolete. One owner
    // retries it, so repeated tab taps cannot race two renders.
    let stats, revision;
    do {
      revision = getLibraryRevision();
      stats = await fetchStats();
    } while (revision !== getLibraryRevision());
    container.innerHTML = buildStatsHTML(stats);
    state.revision = revision;
    container.querySelectorAll('.stat-recent-row').forEach(btn => {
      btn.addEventListener('click', () => {
        window.location.hash = '#game/' + Number(btn.dataset.gameId);
      });
    });
  } catch {
    state.revision = -1;
    container.innerHTML = '<div class="stats-page"><div class="empty-state"><p>Failed to load stats.</p><button type="button" class="btn btn-secondary" data-stats-retry>Retry</button></div></div>';
    container.querySelector('[data-stats-retry]')?.addEventListener('click', () => renderStatsView(container));
  }
}

export async function openStatsDialog() {
  // Backwards compatibility: navigate to the Stats tab instead of a modal.
  window.location.hash = '#stats';
  const container = document.getElementById('statsView');
  if (container) await renderStatsView(container);
}
