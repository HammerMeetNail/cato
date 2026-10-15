// home.js — Home: what you're playing (one-tap time chips), Up Next,
// recent activity, the streak line, and first-run onboarding.
import { html, nothing, repeat } from '../ui/lit.js';
import { icon } from '../ui/icons.js';
import { cover, statusPill, empty, skeletonRows } from '../ui/parts.js';
import { stars } from '../ui/stars.js';
import { api } from '../core/api.js';
import { formatMinutes, relativeDay, localDay, timestampDay, plural, formatRelease } from '../core/format.js';
import { app, ratingDisplay, savePrefs } from './app-state.js';
import { timeChips, gameMenu } from './sheets.js';
import * as actions from './actions.js';
import { longPress } from '../ui/menu.js';
import { openLogSheet } from './log.js';
import { startTimer } from './timer.js';

const state = { data: null, error: null, loading: false, fit: '', mood: '', picks: null, spinning: false };

const MOODS = ['cozy', 'co-op', 'short', 'story-rich', 'challenging', 'spooky'];
const upNextQuery = n => `/api/v1/up-next?n=${n}${state.fit ? `&fit=${state.fit}` : ''}${state.mood ? `&mood=${encodeURIComponent(state.mood)}` : ''}`;

const FITS = [
  { value: '', label: 'Anything' },
  { value: 'tonight', label: '1h tonight' },
  { value: 'weekend', label: 'A weekend' },
  { value: 'long', label: 'A long haul' },
];

async function chooseFit(fit, mood = state.mood) {
  state.fit = fit;
  state.mood = mood;
  app.redraw();
  try {
    state.picks = fit || mood ? (await api.get(upNextQuery(3))).items : null;
  } catch (err) {
    state.picks = null;
  }
  app.redraw();
}

// surprise spins through the top ten and lands on one (reduced motion: no spin).
async function surprise() {
  let list;
  try {
    list = (await api.get(upNextQuery(10))).items;
  } catch (err) {
    return;
  }
  if (!list.length) return;
  const pick = list[Math.floor(Math.random() * list.length)];
  const still = matchMedia('(prefers-reduced-motion: reduce)').matches;
  state.spinning = true;
  for (let i = 0; !still && i < 12; i++) {
    state.picks = [list[i % list.length]];
    app.redraw();
    await new Promise(r => setTimeout(r, 60 + i * 18));
  }
  state.spinning = false;
  state.picks = [{ ...pick, reason: `Surprise! ${pick.reason}` }];
  app.redraw();
}

// order is the card order for the current visit (see homeView).
let order = [];

// beginHomeVisit lets the next render order cards by recency again.
export function beginHomeVisit() {
  order = [];
}

// The friends feed and goal rings load after first paint (they're not
// needed to log time, and keep the initial JS within budget).
let feed = null;
let goals = null;

export async function enterHome() {
  if (feed) feed.loadFeed();
  if (state.loading) return;
  state.loading = true;
  try {
    state.data = await api.get(`/api/v1/home?today=${localDay()}`);
    state.error = null;
    if (state.data.goals?.length && !goals) import('./goals.js').then(m => { goals = m; app.redraw(); }).catch(() => {});
    if (state.data.following > 0 && !feed) import('./feed.js').then(m => { feed = m; m.loadFeed(); }).catch(() => {});
  } catch (err) {
    state.error = err;
  } finally {
    state.loading = false;
    app.redraw();
  }
}

function greeting() {
  const h = new Date().getHours();
  const part = h < 5 ? 'Up late' : h < 12 ? 'Good morning' : h < 18 ? 'Good afternoon' : 'Good evening';
  const name = (app.me?.display_name || '').split(' ')[0];
  return name ? `${part}, ${name}` : part;
}

function nowCard(e) {
  const lp = longPress(pos => gameMenu(e.id, pos));
  return html`<article class="now-card" data-game-id=${e.id} aria-label=${e.name}
      @contextmenu=${lp.contextmenu}>
    <a href=${`#game/${e.id}`} aria-label=${`Open ${e.name}`} @pointerdown=${lp.pointerdown} @pointerup=${lp.pointerup}
      @pointermove=${lp.pointermove} @click=${lp.click}>${cover(e)}</a>
    <div class="info">
      <a class="title" href=${`#game/${e.id}`}>${e.name}</a>
      <div class="meta"><span class="num">${formatMinutes(e.minutes)}</span> played${e.status === 'paused' ? html` · ${statusPill('paused')}` : nothing}
        ${e.updated ? html` · ${relativeDay(timestampDay(new Date(e.updated * 1000).toISOString()))}` : nothing}</div>
      ${e.rating ? stars(e.rating, { display: ratingDisplay() }) : nothing}
    </div>
    <div class="actions">
      ${timeChips(e.id)}
      <button type="button" class="chip" @click=${() => startTimer(e.id)} aria-label=${`Start a session timer for ${e.name}`}>${icon('play')}Start session</button>
      ${e.status === 'paused' ? html`<button type="button" class="chip" @click=${() => actions.setStatus(e.id, 'playing').catch(() => {})}>${icon('play')}Resume</button>` : nothing}
    </div>
  </article>`;
}

function upNextCard(pick) {
  const e = app.store.get(pick.item.game_id) || { id: pick.item.game_id, name: pick.item.game.name, cover: pick.item.game.cover };
  const pinned = pick.pinned ? html`<span class="status status-paused">In your queue</span>` : nothing;
  return html`<div class="upnext-card">
    <a href=${`#game/${e.id}`} aria-label=${`Open ${e.name}`}>${cover(e)}</a>
    <a class="title" href=${`#game/${e.id}`} style="color:inherit;text-decoration:none">${e.name}</a>
    <span class="reason">${pick.reason}</span>${pinned}
    <button type="button" class="btn btn-sm btn-secondary" @click=${() => actions.setStatus(e.id, 'playing').catch(() => {})}>${icon('play')}Start</button>
  </div>`;
}

function onboarding() {
  return html`<section class="onboarding" aria-labelledby="onb-title">
    <div>
      <h2 id="onb-title">Welcome to Cato</h2>
      <p class="muted">A private, fast record of what you play. Three quick steps:</p>
    </div>
    <ol>
      <li><div><strong>Tell Cato your platforms.</strong> They pre-fill when you add games.
        <div><a href="#you/settings?section=platforms">Choose platforms</a></div></div></li>
      <li><div><strong>Add a few games you love, or what you're playing now.</strong>
        <div><button type="button" class="btn btn-sm btn-primary" @click=${() => openLogSheet()}>${icon('plus')}Add games</button></div></div></li>
      <li><div><strong>Bring your library.</strong> Import from Steam or a CSV.
        <div><a href="#you/import">Import</a></div></div></li>
    </ol>
    <div><button type="button" class="link" @click=${() => savePrefs({ onboarding_done: true }).catch(() => {})}>Hide this</button></div>
  </section>`;
}

// reviewSeason is the year to show a Year in Review card for (from
// December 15 until the end of January), or 0.
function reviewSeason(now = new Date()) {
  if (now.getMonth() === 11 && now.getDate() >= 15) return now.getFullYear();
  if (now.getMonth() === 0) return now.getFullYear() - 1;
  return 0;
}

export function homeView() {
  const fresh = app.store.entries()
    .filter(e => e.status === 'playing' || e.status === 'paused')
    .sort((a, b) => (a.status === b.status ? b.updated - a.updated : a.status === 'playing' ? -1 : 1));
  // Keep the order stable while Home is open: logging time makes a game
  // the most recent, and moving its card would put a different game under
  // the next tap. The order refreshes each time Home is entered.
  const rank = new Map(order.map((id, i) => [id, i]));
  const playing = [...fresh].sort((a, b) => (rank.get(a.id) ?? -1) - (rank.get(b.id) ?? -1));
  order = playing.map(e => e.id);
  const d = state.data;
  const total = app.store.entries().length;
  return html`<div class="page">
    <header class="greeting">
      <h1>${greeting()}</h1>
      <p class="num">${new Date().toLocaleDateString(undefined, { weekday: 'long', month: 'long', day: 'numeric' })}</p>
    </header>
    ${d?.streak && d.streak.current > 1 ? html`<p class="streak-line">${icon('flame')}You've played ${plural(d.streak.current, 'day')} in a row.</p>` : nothing}
    ${(d?.onboarding || (!d && total === 0 && app.store.synced)) && !app.prefs.onboarding_done ? html`<div class="section">${onboarding()}</div>` : nothing}

    <section class="section" aria-labelledby="now-title">
      <div class="section-head"><h2 id="now-title">Now playing</h2>
        ${playing.length ? html`<a href="#library?shelf=playing">See all</a>` : nothing}</div>
      ${playing.length
        ? html`<div class="now-grid">${repeat(playing.slice(0, 6), e => e.id, nowCard)}</div>`
        : empty({ iconName: 'gamepad-2', title: 'Nothing playing right now',
          body: total ? 'Pick something from Up Next, or start a game from your backlog.' : 'Add what you’re playing and log time in one tap.',
          action: html`<button type="button" class="btn btn-primary" @click=${() => openLogSheet()}>${icon('plus')}Log a game</button>` })}
    </section>

    ${d?.up_next?.length ? html`<section class="section" aria-labelledby="upnext-title">
      <div class="section-head"><h2 id="upnext-title">Up next</h2>
        <button type="button" class="btn btn-sm btn-ghost" ?disabled=${state.spinning} @click=${surprise}>${icon('dices')}Surprise me</button></div>
      <div class="chips chips-scroll fit-chips" role="radiogroup" aria-label="How much time do you have?">
        ${FITS.map(f => html`<button type="button" class="chip" role="radio" aria-checked=${String(state.fit === f.value)}
          @click=${() => chooseFit(f.value)}>${f.label}</button>`)}
      </div>
      <div class="chips chips-scroll fit-chips" role="radiogroup" aria-label="In the mood for">
        ${MOODS.map(m => html`<button type="button" class="chip" role="radio" aria-checked=${String(state.mood === m)}
          @click=${() => chooseFit(state.fit, state.mood === m ? '' : m)}>${m}</button>`)}
      </div>
      ${state.mood && state.picks && !state.picks.length ? html`<p class="muted">Nothing in your backlog is tagged or known as ${state.mood}. Tag a few games “${state.mood}” and they'll show up here.</p>` : nothing}
      <div class="upnext-grid" aria-live="polite">${repeat(state.picks ?? d.up_next, p => p.item.game_id, upNextCard)}</div>
    </section>` : nothing}

    ${d?.coming_soon?.length ? html`<section class="section" aria-labelledby="soon-title">
      <div class="section-head"><h2 id="soon-title">Coming soon from your wishlist</h2></div>
      <div class="shelf-row">${d.coming_soon.map(g => html`<a class="upnext-card" href=${`#game/${g.id}`}>
        ${cover(g)}<span class="title">${g.name}</span><span class="reason">${formatRelease(g.release_date)}</span></a>`)}</div>
    </section>` : nothing}

    ${reviewSeason() ? html`<a class="card yr-teaser section" href=${`#you/year/${reviewSeason()}`}>${icon('sparkles')}
      <span class="grow"><strong>Your ${reviewSeason()} in review</strong><span class="muted"> · hours, favourites and firsts, ready to share</span></span>${icon('chevron-right')}</a>` : nothing}

    ${d?.goals?.length && goals ? html`<section class="section" aria-labelledby="goals-title">
      <div class="section-head"><h2 id="goals-title">Your ${new Date().getFullYear()} goals</h2><a href="#you/goals">Goals</a></div>
      <div class="goal-row">${d.goals.map(g => goals.goalCard(g))}</div>
    </section>` : nothing}

    ${feed ? feed.feedSection() : nothing}

    <section class="section" aria-labelledby="recent-title">
      <div class="section-head"><h2 id="recent-title">Recent activity</h2><a href="#you/diary">Diary</a></div>
      ${!d && state.loading ? skeletonRows(3)
        : d?.recent?.length ? html`<div class="list">${d.recent.map(s => html`<a class="list-row" href=${`#game/${s.game_id}`}>
            ${cover({ name: s.game_name, cover: s.cover }, { cls: 'thumb' })}
            <div class="grow"><div class="title">${s.game_name}</div>
              <div class="sub">${relativeDay(s.played_on)} · ${formatMinutes(s.minutes)}${s.note ? ` · ${s.note}` : ''}</div></div>
          </a>`)}</div>`
        : html`<p class="muted">${state.error ? 'Activity needs a connection.' : 'Logged sessions show up here.'}</p>`}
    </section>
  </div>`;
}
