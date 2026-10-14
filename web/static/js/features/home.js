// home.js — Home: what you're playing (one-tap time chips), Up Next,
// recent activity, the streak line, and first-run onboarding.
import { html, nothing, repeat } from '../ui/lit.js';
import { icon } from '../ui/icons.js';
import { cover, statusPill, empty, skeletonRows } from '../ui/parts.js';
import { stars } from '../ui/stars.js';
import { api } from '../core/api.js';
import { formatMinutes, relativeDay, localDay, timestampDay, plural } from '../core/format.js';
import { app, ratingDisplay, savePrefs } from './app-state.js';
import { timeChips, gameMenu } from './sheets.js';
import * as actions from './actions.js';
import { longPress } from '../ui/menu.js';
import { openLogSheet } from './log.js';
import { startTimer } from './timer.js';

const state = { data: null, error: null, loading: false };

export async function enterHome() {
  if (state.loading) return;
  state.loading = true;
  try {
    state.data = await api.get(`/api/v1/home?today=${localDay()}`);
    state.error = null;
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
  return html`<div class="upnext-card">
    <a href=${`#game/${e.id}`} aria-label=${`Open ${e.name}`}>${cover(e)}</a>
    <a class="title" href=${`#game/${e.id}`} style="color:inherit;text-decoration:none">${e.name}</a>
    <span class="reason">${pick.reason}</span>
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

export function homeView() {
  const playing = app.store.entries()
    .filter(e => e.status === 'playing' || e.status === 'paused')
    .sort((a, b) => (a.status === b.status ? b.updated - a.updated : a.status === 'playing' ? -1 : 1));
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
      <div class="section-head"><h2 id="upnext-title">Up next</h2><a href="#discover">More ideas</a></div>
      <div class="upnext-grid">${repeat(d.up_next, p => p.item.game_id, upNextCard)}</div>
    </section>` : nothing}

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
