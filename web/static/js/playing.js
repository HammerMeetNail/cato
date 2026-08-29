import { library, getCoverURL } from './api.js';
import { escapeHTML, openLibraryItemModal, showToast } from './library.js';

// Helpers duplicated from library.js hero section (kept there for card quick actions).
function fmtHours(minutes) {
  const h = Math.round((minutes / 60) * 10) / 10;
  return h === 1 ? '1h' : `${h}h`;
}

function fmtDelta(minutes) {
  return minutes % 60 === 0 ? `${minutes / 60}h` : `${minutes}m`;
}

function playingCardHTML(item) {
  const bits = [];
  if (item.playtime_minutes > 0) bits.push(fmtHours(item.playtime_minutes));
  if (item.started_at) {
    const days = Math.max(1, Math.floor((Date.now() - new Date(item.started_at).getTime()) / 86400000) + 1);
    bits.push(`day ${days}`);
  }
  const sub = bits.join(' · ');
  return `
    <div class="hero-card" data-game-id="${item.game_id}">
      <img src="${getCoverURL(item)}" alt="${escapeHTML(item.game_name)}" loading="lazy" decoding="async" onerror="this.onerror=null;this.src='/covers/${item.game_id}.jpg'">
      <div class="hero-body">
        <div class="hero-name">${escapeHTML(item.game_name)}</div>
        <div class="hero-controls">
          <span class="hero-sub">${escapeHTML(sub)}</span>
          <div class="hero-actions">
            <button type="button" class="hero-btn" data-hero-time="30" title="Log 30 minutes">+30m</button>
            <button type="button" class="hero-btn" data-hero-time="60" title="Log 1 hour">+1h</button>
            <button type="button" class="hero-btn" data-hero-time="120" title="Log 2 hours">+2h</button>
            <button type="button" class="hero-btn hero-finish" data-hero-finish title="Mark as finished" aria-label="Mark as finished">✓</button>
          </div>
        </div>
      </div>
    </div>`;
}

const playingItems = new Map();

export async function renderPlayingView(container) {
  if (!container) return;
  container.innerHTML = '<div class="playing-page"><div class="loading">Loading now playing…</div></div>';

  let items;
  try {
    ({ items } = await library.list('playing', 100, 0));
  } catch {
    container.innerHTML = '<div class="playing-page"><div class="empty-state"><p>Failed to load playing games.</p></div></div>';
    return;
  }

  playingItems.clear();
  for (const it of items || []) playingItems.set(String(it.game_id), it);

  if (!items || items.length === 0) {
    container.innerHTML = `
      <div class="playing-page">
        <div class="empty-state">
          <div class="empty-title">Nothing playing right now</div>
          <p>Pick something from your backlog and hit play!</p>
          <button type="button" class="btn btn-primary btn-inline" id="playingEmptyCta">Go to backlog</button>
        </div>
      </div>`;
    container.querySelector('#playingEmptyCta')?.addEventListener('click', () => {
      window.location.hash = '#backlog';
    });
    return;
  }

  const page = document.createElement('div');
  page.className = 'playing-page';
  page.innerHTML = `
    <h2>Now Playing</h2>
    <div class="playing-count">${items.length} ${items.length === 1 ? 'game' : 'games'} in progress</div>
    <div class="playing-list">
      ${items.map(it => playingCardHTML(it)).join('')}
    </div>`;

  // Clear and append
  container.innerHTML = '';
  container.appendChild(page);

  const listEl = page.querySelector('.playing-list');
  if (!listEl) return;

  listEl.addEventListener('click', async (e) => {
    const timeBtn = e.target.closest('[data-hero-time]');
    const finishBtn = e.target.closest('[data-hero-finish]');
    const coverEl = e.target.closest('.hero-card img');
    if (!timeBtn && !finishBtn && !coverEl) return;
    const cardEl = (timeBtn || finishBtn || coverEl).closest('.hero-card');
    const gameId = Number(cardEl?.dataset.gameId);
    if (!gameId) return;

    if (coverEl) {
      const item = playingItems.get(String(gameId));
      if (item) openLibraryItemModal(item);
      return;
    }

    if (timeBtn) {
      const minutes = parseInt(timeBtn.dataset.heroTime, 10) || 0;
      timeBtn.disabled = true;
      try {
        const updated = await library.patch(gameId, { playtime_delta_minutes: minutes });
        playingItems.set(String(updated.game_id), updated);
        const sub = cardEl.querySelector('.hero-sub');
        const bits = [];
        if (updated.playtime_minutes > 0) bits.push(fmtHours(updated.playtime_minutes));
        if (updated.started_at) {
          const days = Math.max(1, Math.floor((Date.now() - new Date(updated.started_at).getTime()) / 86400000) + 1);
          bits.push(`day ${days}`);
        }
        if (sub) sub.textContent = bits.join(' · ');
        showToast(`+${fmtDelta(minutes)} logged for ${updated.game_name}`);
      } catch (err) {
        showToast(`Couldn't log time: ${err.message}`, { type: 'error' });
      } finally {
        timeBtn.disabled = false;
      }
      return;
    }

    // Finish
    const finishItem = playingItems.get(String(gameId));
    if (!finishItem) return;
    finishBtn.disabled = true;
    try {
      const updated = await library.patch(gameId, { status: 'completed' });
      showToast(`Finished ${updated.game_name} ✓`);
      cardEl.classList.add('card-removing');
      setTimeout(() => {
        cardEl.remove();
        playingItems.delete(String(gameId));
        // Update count
        const countEl = page.querySelector('.playing-count');
        const remaining = page.querySelectorAll('.hero-card').length;
        if (countEl) countEl.textContent = `${remaining} ${remaining === 1 ? 'game' : 'games'} in progress`;
        if (remaining === 0) {
          container.innerHTML = `
            <div class="playing-page">
              <div class="empty-state">
                <div class="empty-title">All caught up!</div>
                <p>No games in progress — time to start the next one.</p>
                <button type="button" class="btn btn-primary btn-inline" id="playingEmptyCta2">Go to backlog</button>
              </div>
            </div>`;
          container.querySelector('#playingEmptyCta2')?.addEventListener('click', () => {
            window.location.hash = '#backlog';
          });
        }
      }, 260);
    } catch (err) {
      showToast(`Couldn't mark as finished: ${err.message}`, { type: 'error' });
      finishBtn.disabled = false;
    }
  });
}
