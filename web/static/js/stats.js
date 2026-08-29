import { api } from './api.js';
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

export async function renderStatsView(container) {
  if (!container) return;
  container.innerHTML = '<div class="stats-page"><div class="loading">Loading stats…</div></div>';
  let s;
  try {
    s = await fetchStats();
  } catch (err) {
    container.innerHTML = '<div class="stats-page"><div class="empty-state"><p>Failed to load stats.</p></div></div>';
    return;
  }
  container.innerHTML = buildStatsHTML(s);
  // Recent items deep-link into the game modal.
  container.querySelectorAll('.stat-recent-row').forEach(btn => {
    btn.addEventListener('click', async () => {
      const id = Number(btn.dataset.gameId);
      const mod = await import('./library.js');
      mod.openGameModal(id);
    });
  });
}

export async function openStatsDialog() {
  // Backwards compatibility: navigate to the Stats tab instead of a modal.
  window.location.hash = '#stats';
  const container = document.getElementById('statsView');
  if (container) await renderStatsView(container);
}
