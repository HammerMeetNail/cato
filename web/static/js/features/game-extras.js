// game-extras.js — the catalog sections of the game page: time to beat
// (and at your pace), studios, series, related games, media and the
// click-to-load trailer, where to get it, and the details.
import { html, nothing } from '../ui/lit.js';
import { icon } from '../ui/icons.js';
import { cover } from '../ui/parts.js';
import { openSheet } from '../ui/sheet.js';
import { formatMinutes, formatHoursLong, releaseYear, formatRelease } from '../core/format.js';
import { app } from './app-state.js';

const igdbImage = (id, size) => `https://images.igdb.com/igdb/image/upload/${size}/${id}.jpg`;

const RELATED = [
  ['similar', 'More like this'],
  ['expansion', 'Expansions'],
  ['standalone', 'Standalone expansions'],
  ['dlc', 'DLC'],
  ['remake', 'Remakes'],
  ['remaster', 'Remasters'],
  ['port', 'Ports'],
  ['bundle', 'Bundles'],
];

function strip(title, games, { subtitle = '', current = 0 } = {}) {
  if (!games?.length) return nothing;
  return html`<section class="section" aria-label=${title}>
    <div class="section-head"><h2>${title}</h2>${subtitle ? html`<span class="muted">${subtitle}</span>` : nothing}</div>
    <div class="shelf-row">${games.map(g => html`<a class="upnext-card" href=${`#game/${g.id}`} aria-current=${g.id === current ? 'page' : nothing}>
      ${cover(g)}<span class="title">${g.name}</span><span class="reason">${releaseYear(g.release_date) || ''}${g.id === current ? ' · this game' : ''}${app.store.get(g.id) ? ' · in your library' : ''}</span></a>`)}</div>
  </section>`;
}

function ttbRow(x, estimate) {
  if (!x.ttb) return nothing;
  const cell = (label, minutes) => minutes ? html`<div class="stat-tile"><div class="value">${formatMinutes(minutes)}</div><div class="label">${label}</div></div>` : nothing;
  let pace = nothing;
  if (estimate) {
    const p = estimate.pace;
    const weeks = estimate.weeks;
    const weeksText = weeks ? `, ≈ ${weeks < 1.5 ? 'a week' : `${Math.round(weeks)} weeks`} at how much you’ve been playing` : '';
    const versus = p.samples >= 2
      ? ` (you take ${p.multiplier > 1.05 ? `${Math.round((p.multiplier - 1) * 100)}% longer than` : p.multiplier < 0.95 ? `${Math.round((1 - p.multiplier) * 100)}% less time than` : 'about as long as'} most players)`
      : '';
    pace = html`<p class="muted" style="margin-top:var(--sp-2)">${icon('timer')} About <strong>${formatHoursLong(estimate.minutes)}</strong> at your pace${weeksText}${versus}.</p>`;
  }
  return html`<section class="section" aria-labelledby="ttb-title">
    <div class="section-head"><h2 id="ttb-title">Time to beat</h2>${x.ttb.count ? html`<span class="muted">from ${x.ttb.count} players</span>` : nothing}</div>
    <div class="stat-tiles">${cell('Main story', x.ttb.hastily)}${cell('Main + extras', x.ttb.normally)}${cell('Completionist', x.ttb.completely)}</div>
    ${pace}
  </section>`;
}

function trailer(v, poster) {
  // Click-to-load: nothing is requested from YouTube until the person asks.
  const load = e => {
    const frame = document.createElement('iframe');
    Object.assign(frame, {
      src: `https://www.youtube-nocookie.com/embed/${encodeURIComponent(v.video_id)}?autoplay=1&rel=0`,
      title: v.name || 'Trailer',
      allow: 'autoplay; encrypted-media; picture-in-picture; fullscreen',
      referrerPolicy: 'strict-origin-when-cross-origin',
    });
    frame.setAttribute('allowfullscreen', '');
    Object.assign(frame.style, { width: '100%', aspectRatio: '16 / 9', border: '0', borderRadius: 'var(--r-md)' });
    e.currentTarget.replaceWith(frame);
  };
  return html`<button type="button" class="trailer" @click=${load} aria-label=${`Play ${v.name || 'trailer'} (loads YouTube)`}>
    ${poster ? html`<img src=${igdbImage(poster, 't_screenshot_med')} alt="" loading="lazy">` : nothing}
    <span class="trailer-play">${icon('play')}</span>
    <span class="trailer-note">${v.name || 'Trailer'} · plays from YouTube</span>
  </button>`;
}

function openViewer(images, index) {
  let i = index;
  openSheet({
    cls: 'wide',
    label: 'Screenshots',
    content: s => html`<figure style="margin:0">
      <img src=${igdbImage(images[i], 't_720p')} alt=${`Screenshot ${i + 1} of ${images.length}`} style="width:100%;border-radius:var(--r-md)">
      <figcaption class="row" style="justify-content:center;margin-top:var(--sp-3)">
        <button type="button" class="btn btn-secondary btn-icon" aria-label="Previous" ?disabled=${i === 0} @click=${() => { i -= 1; s.update(); }}>${icon('chevron-left')}</button>
        <span class="num">${i + 1} / ${images.length}</span>
        <button type="button" class="btn btn-secondary btn-icon" aria-label="Next" ?disabled=${i === images.length - 1} @click=${() => { i += 1; s.update(); }}>${icon('chevron-right')}</button>
      </figcaption></figure>`,
  });
}

function media(x) {
  const shots = [...x.screenshots, ...x.artworks];
  if (!shots.length && !x.videos.length) return nothing;
  return html`<section class="section" aria-labelledby="media-title">
    <div class="section-head"><h2 id="media-title">Media</h2></div>
    ${x.videos.length ? trailer(x.videos[0], x.artworks[0] || x.screenshots[0]) : nothing}
    ${shots.length ? html`<div class="shelf-row media-row" style="margin-top:var(--sp-3)">${shots.map((id, i) => html`<button type="button" class="media-thumb"
      aria-label=${`Open screenshot ${i + 1}`} @click=${() => openViewer(shots, i)}><img src=${igdbImage(id, 't_screenshot_med')} alt="" loading="lazy"></button>`)}</div>` : nothing}
  </section>`;
}

function details(x) {
  const mp = x.multiplayer;
  const badges = mp ? [mp.online_coop && `Online co-op${mp.online_max > 1 ? ` (${mp.online_max})` : ''}`, mp.offline_coop && `Local co-op${mp.offline_max > 1 ? ` (${mp.offline_max})` : ''}`,
    mp.splitscreen && 'Split screen', mp.campaign_coop && 'Co-op campaign', mp.lan && 'LAN'].filter(Boolean) : [];
  const rows = [
    ['Developer', x.developers.join(', ')], ['Publisher', x.publishers.join(', ')], ['Genres', x.genres.join(', ')],
    ['Themes', x.themes.join(', ')], ['Modes', x.modes.join(', ')], ['Perspective', x.perspectives.join(', ')],
    ['Type', x.game_type], ['Age rating', x.age_ratings.join(' · ')],
    ['Languages', x.languages.length > 8 ? `${x.languages.slice(0, 8).join(', ')} and ${x.languages.length - 8} more` : x.languages.join(', ')],
  ].filter(([, v]) => v);
  if (!rows.length && !badges.length && !x.release_dates.length) return nothing;
  const now = Date.now() / 1000;
  return html`<section class="section card" aria-labelledby="details-title">
    <h2 id="details-title" style="font-size:var(--fs-18);margin-bottom:var(--sp-3)">Details</h2>
    ${badges.length ? html`<div class="chips" style="margin-bottom:var(--sp-3)">${badges.map(b => html`<span class="chip">${icon('users')}${b}</span>`)}</div>` : nothing}
    <dl class="kv">${rows.map(([k, v]) => html`<dt>${k}</dt><dd>${v}</dd>`)}</dl>
    ${x.release_dates.length ? html`<h3 style="font-size:var(--fs-14);margin:var(--sp-4) 0 var(--sp-2)">Release dates</h3>
      <dl class="kv">${x.release_dates.slice(0, 12).map(r => html`<dt>${r.platform || 'Unknown'}${r.region && r.region !== 'Worldwide' ? ` (${r.region})` : ''}</dt>
        <dd>${r.date ? formatRelease(r.date) : r.human || 'TBA'}${r.date > now ? html` <span class="status status-wishlist">Upcoming</span>` : nothing}</dd>`)}</dl>` : nothing}
  </section>`;
}

function links(x) {
  if (!x.links.length) return nothing;
  return html`<section class="section" aria-labelledby="links-title">
    <div class="section-head"><h2 id="links-title">Where to get it</h2></div>
    <div class="chips">${x.links.map(l => html`<a class="chip" href=${l.url} target="_blank" rel="noopener noreferrer">${icon('external-link')}${l.label}</a>`)}</div>
  </section>`;
}

export function gameExtras(d) {
  const x = d?.extras;
  if (!x) return nothing;
  const id = d.game.id;
  return html`
    ${ttbRow(x, d.estimate)}
    ${x.base_game ? html`<p class="section muted">${icon('layers')} An edition of <a href=${`#game/${x.base_game.id}`}>${x.base_game.name}</a>.</p>` : nothing}
    ${media(x)}
    ${x.series.map(s => strip(s.kind === 'franchise' ? `${s.name} franchise` : `${s.name} series`, s.games, { current: id, subtitle: `${s.games.length} games` }))}
    ${RELATED.map(([k, title]) => strip(title, x.related[k]))}
    ${strip('Editions', x.editions)}
    ${links(x)}
    ${details(x)}
    ${x.source === 'custom' ? html`<p class="section muted">Added by hand — not in the IGDB catalog.</p>`
      : html`<p class="section muted" style="font-size:var(--fs-12)">Game data from <a href="https://www.igdb.com" target="_blank" rel="noopener noreferrer">IGDB</a>.</p>`}
  `;
}
