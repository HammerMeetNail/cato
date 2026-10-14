// feed.js — friends' activity on Home: what people you follow started,
// finished, reviewed and listed, with a day's logging grouped into one
// line ("Alex logged 6h across 2 games").
import { html, nothing } from '../ui/lit.js';
import { cover } from '../ui/parts.js';
import { stars } from '../ui/stars.js';
import { api } from '../core/api.js';
import { formatHoursLong, plural, relativeDay, timestampDay, initials } from '../core/format.js';
import { app, ratingDisplay } from './app-state.js';

const feed = { items: null, loading: false };

export async function loadFeed() {
  if (feed.loading) return;
  feed.loading = true;
  try { feed.items = (await api.get('/api/v1/feed?limit=12')).items; } catch { /* offline: keep what we had */ }
  feed.loading = false;
  app.redraw();
}

const name = p => p.display_name || `@${p.username}`;
const gameLink = g => html`<a href=${`#game/${g.id}`}>${g.name}</a>`;

function line(e) {
  const g = e.games[0];
  const who = html`<a href=${`#u/${encodeURIComponent(e.person.username)}`}><strong>${name(e.person)}</strong></a>`;
  switch (e.kind) {
    case 'logged': return e.games.length > 1
      ? html`${who} logged ${formatHoursLong(e.minutes)} across ${plural(e.games.length, 'game')}`
      : html`${who} played ${g ? gameLink(g) : 'a game'} for ${formatHoursLong(e.minutes)}`;
    case 'started': return html`${who} ${e.data?.replay ? 'started replaying' : 'started'} ${gameLink(g)}`;
    case 'finished': return html`${who} finished ${gameLink(g)} ${e.data?.rating ? stars(e.data.rating, { display: ratingDisplay() }) : nothing}`;
    case 'dropped': return html`${who} dropped ${gameLink(g)}`;
    case 'wishlisted': return e.games.length > 1 ? html`${who} wants to play ${plural(e.games.length, 'game')}` : html`${who} wants to play ${gameLink(g)}`;
    case 'added': return e.games.length > 1 ? html`${who} added ${plural(e.games.length, 'game')}` : html`${who} added ${gameLink(g)}`;
    case 'rated': return html`${who} rated ${gameLink(g)} ${stars(e.data?.rating, { display: ratingDisplay() })}`;
    case 'reviewed': return html`${who} reviewed ${gameLink(g)}${e.data?.spoiler ? ' (spoilers)' : ''}`;
    case 'listed': return html`${who} added ${e.games.length > 1 ? plural(e.games.length, 'game') : gameLink(g)} to <a href=${`#list/${e.ref_id}`}>${e.list_title}</a>`;
    default: return html`${who} ${e.kind} ${g ? gameLink(g) : ''}`;
  }
}

export function feedSection() {
  if (!feed.items?.length) return nothing;
  return html`<section class="section" aria-labelledby="feed-title">
    <div class="section-head"><h2 id="feed-title">Friends</h2><a href="#you/people">People</a></div>
    <ul class="list">${feed.items.map(e => html`<li class="list-row feed-row">
      <span class="avatar" aria-hidden="true">${e.person.avatar ? html`<img src=${e.person.avatar} alt="" loading="lazy" referrerpolicy="no-referrer">` : initials(name(e.person))}</span>
      <div class="grow"><div>${line(e)}</div><div class="sub">${relativeDay(timestampDay(e.created_at))}</div></div>
      <div class="feed-covers">${e.games.slice(0, 3).map(g => html`<a href=${`#game/${g.id}`} tabindex="-1" aria-hidden="true">${cover(g, { cls: 'thumb' })}</a>`)}</div>
    </li>`)}</ul>
  </section>`;
}
