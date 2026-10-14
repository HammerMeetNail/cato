// year.js — Year in Review (#you/year/YYYY, plan §10): hours, finishes,
// favourites, the longest session, your genre and platform, a playthrough
// timeline and how you compare with the server. Shareable as an image drawn
// on a canvas in the browser (nothing leaves the device unless you share).
import { html, nothing } from '../ui/lit.js';
import { icon } from '../ui/icons.js';
import { cover, empty, skeletonRows } from '../ui/parts.js';
import { stars } from '../ui/stars.js';
import { toast, toastError } from '../ui/toast.js';
import { api } from '../core/api.js';
import { formatHoursLong, formatMinutes, plural, monthName, formatDay } from '../core/format.js';
import { app, ratingDisplay } from './app-state.js';
import { celebrate } from './delight.js';

const st = { year: null, data: null, error: null };

export async function enterYear(route) {
  const year = route.id || new Date().getFullYear();
  if (st.year !== year) Object.assign(st, { year, data: null, error: null });
  try { st.data = await api.get(`/api/v1/year/${year}`); st.error = null; } catch (err) { st.error = err; }
  app.redraw();
}

const big = (value, label) => html`<div class="yr-stat"><div class="value num">${value}</div><div class="label">${label}</div></div>`;

function gameTile(g, sub) {
  return html`<a class="upnext-card" href=${`#game/${g.id}`}>${cover(g)}<span class="title">${g.name}</span><span class="reason">${sub}</span></a>`;
}

function timeline(d) {
  if (!d.timeline.length) return nothing;
  const yStart = new Date(Date.UTC(d.year, 0, 1)).getTime();
  const span = new Date(Date.UTC(d.year + 1, 0, 1)).getTime() - yStart;
  const pos = (day, fallback) => {
    if (!day) return fallback;
    const t = (new Date(day + 'T00:00:00Z').getTime() - yStart) / span;
    return Math.max(0, Math.min(1, t));
  };
  return html`<section class="section" aria-labelledby="yr-timeline"><div class="section-head"><h2 id="yr-timeline">Your playthroughs</h2></div>
    <div class="yr-months" aria-hidden="true">${Array.from({ length: 12 }, (_, i) => html`<span>${monthName(i).slice(0, 1)}</span>`)}</div>
    <ol class="yr-timeline">${d.timeline.map(run => {
      const a = pos(run.started, 0);
      const b = pos(run.finished, 1);
      return html`<li><a href=${`#game/${run.game.id}`} class="yr-run-name">${run.game.name}</a>
        <span class="yr-track"><span class=${`yr-bar ${run.finished ? (run.dropped ? 'dropped' : 'done') : 'open'}`}
          style=${`left:${(a * 100).toFixed(1)}%;width:${Math.max(1.5, (b - a) * 100).toFixed(1)}%`}
          title=${`${run.started ? formatDay(run.started) : 'Before ' + d.year} → ${run.finished ? formatDay(run.finished) : 'still going'}`}></span></span></li>`;
    })}</ol></section>`;
}

function months(d) {
  const max = Math.max(1, ...d.months);
  return html`<div class="bars" role="img" aria-label=${`Hours played per month in ${d.year}`}>${d.months.map((m, i) => html`<div class="bar-col" title=${`${monthName(i, true)}: ${formatMinutes(m)}`}>
    <div class="bar-fill" style=${`height:${Math.round((m / max) * 100)}%`}></div><span>${monthName(i).slice(0, 1)}</span></div>`)}</div>`;
}

export function yearView() {
  if (st.error) return html`<div class="page">${empty({ iconName: 'triangle-alert', title: 'Couldn’t load your year', body: st.error.message })}</div>`;
  const d = st.data;
  if (!d) return html`<div class="page">${skeletonRows(4)}</div>`;
  const years = [...new Set([...d.years, new Date().getFullYear()])].sort((a, b) => b - a);
  return html`<div class="page yr">
    <header class="page-head"><h1>${d.year} in review</h1>
      ${d.minutes ? html`<button type="button" class="btn btn-secondary btn-sm" @click=${() => share(d)}>${icon('share-2')}Share</button>` : nothing}</header>
    <nav class="chips chips-scroll" aria-label="Years">${years.map(y => html`<a class="chip" href=${`#you/year/${y}`} aria-current=${y === d.year ? 'page' : nothing}>${y}</a>`)}</nav>
    ${!d.minutes && !d.finished.length ? empty({ iconName: 'calendar-days', title: `Nothing logged in ${d.year}`, body: 'Log play time and finish games, and your year comes together here.' }) : html`
    <section class="yr-hero section">
      ${big(formatHoursLong(d.minutes), 'played')}
      ${big(d.finished.length, d.finished.length === 1 ? 'game finished' : 'games finished')}
      ${big(d.games_played, d.games_played === 1 ? 'game played' : 'games played')}
      ${big(d.days_played, d.days_played === 1 ? 'day with play' : 'days with play')}
    </section>
    ${d.first_finish ? html`<p class="yr-line section">${icon('trophy')} Your first finish of ${d.year}: <a href=${`#game/${d.first_finish.id}`}>${d.first_finish.name}</a>, on ${formatDay(d.first_finish.day)}.</p>` : nothing}
    ${d.top_rated.length ? html`<section class="section"><div class="section-head"><h2>Your favourites</h2></div>
      <div class="shelf-row">${d.top_rated.map(g => gameTile(g, stars(g.rating, { display: ratingDisplay() })))}</div></section>` : nothing}
    ${d.most_played.length ? html`<section class="section"><div class="section-head"><h2>Most played</h2></div>
      <div class="shelf-row">${d.most_played.map(g => gameTile(g, formatHoursLong(g.minutes)))}</div></section>` : nothing}
    <section class="section yr-facts">
      ${d.longest_session ? html`<div class="card">${icon('timer')}<div><div class="label">Longest session</div><strong>${formatMinutes(d.longest_session.minutes)}</strong> of ${d.longest_session.name}, ${formatDay(d.longest_session.day)}</div></div>` : nothing}
      ${d.busiest_day ? html`<div class="card">${icon('flame')}<div><div class="label">Biggest day</div><strong>${formatMinutes(d.busiest_day.minutes)}</strong> on ${formatDay(d.busiest_day.day)}</div></div>` : nothing}
      ${d.top_genre ? html`<div class="card">${icon('gamepad-2')}<div><div class="label">Most-played genre</div><strong>${d.top_genre}</strong></div></div>` : nothing}
      ${d.top_platform ? html`<div class="card">${icon('monitor')}<div><div class="label">Most-played platform</div><strong>${d.top_platform}</strong></div></div>` : nothing}
      ${d.oldest_game ? html`<div class="card">${icon('history')}<div><div class="label">Oldest game you played</div><strong>${d.oldest_game.name}</strong> (${new Date(d.oldest_game.release_date * 1000).getUTCFullYear()})</div></div>` : nothing}
    </section>
    <section class="section"><div class="section-head"><h2>Month by month</h2></div>${months(d)}</section>
    ${timeline(d)}
    ${d.instance ? html`<section class="section card"><h2 style="font-size:var(--fs-18)">${icon('users')} On this server</h2>
      <p>You played more than <strong>${d.instance.hours_percentile}%</strong> of the ${d.instance.people} people here who share their games.
        The middle of the pack played ${plural(d.instance.median_hours, 'hour')} and finished ${d.instance.median_finished} ${d.instance.median_finished === 1 ? 'game' : 'games'}.</p></section>` : nothing}
    ${d.finished.length ? html`<section class="section"><div class="section-head"><h2>Everything you finished</h2></div>
      <ol class="list">${d.finished.map(g => html`<li><a class="list-row" href=${`#game/${g.id}`}>${cover(g, { cls: 'thumb' })}
        <div class="grow"><div class="title">${g.name}</div><div class="sub">${formatDay(g.day)}${g.minutes ? ` · ${formatMinutes(g.minutes)}` : ''}</div></div>
        ${stars(g.rating, { display: ratingDisplay() })}</a></li>`)}</ol></section>` : nothing}
    `}
  </div>`;
}

// ---- the share image ----------------------------------------------------------

function loadImage(src) {
  return new Promise(resolve => {
    if (!src) { resolve(null); return; }
    const img = new Image();
    // IGDB covers need CORS to be drawn; same-origin covers always work.
    if (/^https?:/.test(src)) img.crossOrigin = 'anonymous';
    img.onload = () => resolve(img);
    img.onerror = () => resolve(null);
    img.src = src;
  });
}

// drawCard paints a 1080×1350 summary (a phone-friendly 4:5 image).
export async function drawCard(d) {
  const W = 1080, H = 1350;
  const canvas = document.createElement('canvas');
  canvas.width = W;
  canvas.height = H;
  const ctx = canvas.getContext('2d');
  const css = getComputedStyle(document.documentElement);
  const accent = css.getPropertyValue('--accent').trim() || '#4f3fe0';
  const grad = ctx.createLinearGradient(0, 0, W, H);
  grad.addColorStop(0, '#14121f');
  grad.addColorStop(1, accent);
  ctx.fillStyle = grad;
  ctx.fillRect(0, 0, W, H);
  const font = (w, px) => `${w} ${px}px system-ui, -apple-system, "Segoe UI", Roboto, sans-serif`;
  ctx.fillStyle = '#fff';
  ctx.font = font(800, 96);
  ctx.fillText(String(d.year), 80, 170);
  ctx.font = font(600, 44);
  ctx.globalAlpha = 0.85;
  ctx.fillText('in games', 80, 230);
  ctx.globalAlpha = 1;
  const stats = [[formatHoursLong(d.minutes), 'played'], [String(d.finished.length), 'finished'], [String(d.games_played), 'games'], [String(d.days_played), 'days']];
  stats.forEach(([v, l], i) => {
    const x = 80 + (i % 2) * 470;
    const y = 360 + Math.floor(i / 2) * 170;
    ctx.font = font(800, 84);
    ctx.fillText(v, x, y);
    ctx.font = font(500, 36);
    ctx.globalAlpha = 0.8;
    ctx.fillText(l, x, y + 52);
    ctx.globalAlpha = 1;
  });
  const games = (d.top_rated.length ? d.top_rated : d.most_played).slice(0, 4);
  const imgs = await Promise.all(games.map(g => loadImage(g.cover)));
  const cw = 210, ch = 280;
  games.forEach((g, i) => {
    const x = 80 + i * (cw + 23);
    const y = 760;
    ctx.fillStyle = 'rgba(255,255,255,0.12)';
    ctx.fillRect(x, y, cw, ch);
    if (imgs[i]) {
      try { ctx.drawImage(imgs[i], x, y, cw, ch); } catch { /* tainted: keep the placeholder */ }
    } else {
      ctx.fillStyle = '#fff';
      ctx.font = font(600, 26);
      wrap(ctx, g.name, x + 14, y + 44, cw - 28, 32);
    }
  });
  ctx.fillStyle = '#fff';
  ctx.font = font(600, 34);
  ctx.globalAlpha = 0.9;
  if (d.top_genre) ctx.fillText(`Most played: ${d.top_genre}`, 80, 1130);
  if (d.longest_session) ctx.fillText(`Longest session: ${formatMinutes(d.longest_session.minutes)} of ${truncate(d.longest_session.name, 28)}`, 80, 1185);
  ctx.globalAlpha = 0.6;
  ctx.font = font(600, 30);
  ctx.fillText('Cato', 80, 1290);
  ctx.globalAlpha = 1;
  return canvas;
}

function truncate(s, n) { return s.length > n ? s.slice(0, n - 1) + '…' : s; }

function wrap(ctx, text, x, y, width, lineHeight) {
  let line = '';
  for (const word of text.split(' ')) {
    const test = line ? `${line} ${word}` : word;
    if (ctx.measureText(test).width > width && line) {
      ctx.fillText(line, x, y);
      line = word;
      y += lineHeight;
    } else {
      line = test;
    }
  }
  ctx.fillText(line, x, y);
}

async function share(d) {
  try {
    const canvas = await drawCard(d);
    let blob;
    try {
      blob = await new Promise(r => canvas.toBlob(r, 'image/png'));
    } catch {
      blob = null; // a cross-origin cover tainted the canvas
    }
    if (!blob) {
      toast('Covers from IGDB can’t be drawn into the image here; try again once they’re cached.', { kind: 'error' });
      return;
    }
    const file = new File([blob], `cato-${d.year}.png`, { type: 'image/png' });
    if (navigator.canShare?.({ files: [file] })) {
      await navigator.share({ files: [file], title: `My ${d.year} in games` });
    } else {
      const a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = file.name;
      a.click();
      setTimeout(() => URL.revokeObjectURL(a.href), 10000);
      toast('Image saved');
    }
    celebrate();
  } catch (err) {
    if (err?.name !== 'AbortError') toastError(err, 'Couldn’t make the image');
  }
}
