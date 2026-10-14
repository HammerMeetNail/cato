// you.js — You: the hub (profile summary and links), Stats and Diary.
import { html, nothing, repeat, ref } from '../ui/lit.js';
import { icon } from '../ui/icons.js';
import { cover, empty, skeletonRows } from '../ui/parts.js';
import { api } from '../core/api.js';
import {
  formatMinutes, formatHoursLong, plural, monthName, localDay, addDays, formatDay, relativeDay, initials, completionLabel, statusLabel,
} from '../core/format.js';
import { app } from './app-state.js';

export const youSections = [
  { hash: '#you/stats', icon: 'chart-column', label: 'Stats', sub: 'Hours, finishes, ratings and your backlog' },
  { hash: '#you/diary', icon: 'calendar-days', label: 'Diary', sub: 'Every session, day by day' },
  { hash: '#you/lists', icon: 'list', label: 'Lists', sub: 'Favourites, rankings and collections to share' },
  { hash: '#you/people', icon: 'users', label: 'People', sub: 'Follow friends, requests, invites and blocks' },
  { hash: '#you/inbox', icon: 'inbox', label: 'Inbox', sub: 'Follows, likes and alerts', badge: () => app.unread },
  { hash: '#you/settings', icon: 'settings', label: 'Settings', sub: 'Theme, ratings, platforms, devices, account' },
  { hash: '#you/help', icon: 'circle-question-mark', label: 'Help', sub: 'How Cato works and keyboard shortcuts' },
];

// Later phases register more sections (lists, people, year in review…).
export function addYouSection(section, index = youSections.length) {
  if (!youSections.some(s => s.hash === section.hash)) youSections.splice(index, 0, section);
}

export function youView() {
  const counts = {};
  let minutes = 0;
  for (const e of app.store.entries()) {
    counts[e.status] = (counts[e.status] || 0) + 1;
    minutes += e.minutes;
  }
  const name = app.me?.display_name || app.me?.email || 'You';
  const sections = app.me?.role === 'admin'
    ? [...youSections, { hash: '#admin', icon: 'shield', label: 'Admin', sub: 'People, sign-up, reports and server health' }] : youSections;
  return html`<div class="page">
    <header class="you-head">
      <span class="avatar avatar-lg" aria-hidden="true">${app.me?.avatar ? html`<img src=${app.me.avatar} alt="" referrerpolicy="no-referrer">` : initials(name)}</span>
      <div><h1>${name}</h1>
        <p class="muted num">${plural(app.store.entries().length, 'game')} · ${formatHoursLong(minutes)} · ${plural(counts.completed || 0, 'finish', 'finishes')}</p>
        ${app.me?.username ? html`<p class="row row-wrap" style="margin-top:var(--sp-2)"><a class="btn btn-sm btn-secondary" href=${`#u/${encodeURIComponent(app.me.username)}`}>${icon('user')}Your profile</a>
          <button type="button" class="btn btn-sm btn-ghost" @click=${() => import('./people.js').then(m => m.editProfile())}>${icon('pencil')}Edit profile</button></p>` : nothing}</div>
    </header>
    <nav class="section" aria-label="You">
      <ul class="settings-list">${sections.map(s => html`<li style="padding:0"><a class="list-row" href=${s.hash} style="padding:var(--sp-3) var(--sp-4)">
        ${icon(s.icon)}<div class="grow"><div class="title">${s.label}</div><div class="sub">${s.sub}</div></div>${(typeof s.badge === 'function' ? s.badge() : s.badge) ? html`<span class="badge">${typeof s.badge === 'function' ? s.badge() : s.badge}</span>` : nothing}${icon('chevron-right')}</a></li>`)}</ul>
    </nav>
  </div>`;
}

// ---- Stats --------------------------------------------------------------------

const stats = { year: new Date().getFullYear(), data: null, error: null, loading: false };

export async function enterStats(route) {
  const year = Number(route.query.year) || new Date().getFullYear();
  if (stats.year !== year) stats.data = null;
  stats.year = year;
  stats.loading = true;
  app.redraw();
  try {
    stats.data = await api.get(`/api/v1/stats?year=${year}&today=${localDay()}`);
    stats.error = null;
  } catch (err) {
    stats.error = err;
  }
  stats.loading = false;
  app.redraw();
}

function heatmap(days, year) {
  const byDay = new Map(days.map(d => [d.day, d.minutes]));
  const start = new Date(year, 0, 1);
  const lead = (start.getDay() + 6) % 7; // weeks start on Monday
  const cells = [];
  for (let i = 0; i < lead; i++) cells.push(html`<span style="visibility:hidden"></span>`);
  const end = new Date(year, 11, 31);
  for (let d = new Date(start); d <= end; d.setDate(d.getDate() + 1)) {
    const key = localDay(d);
    const m = byDay.get(key) || 0;
    const level = m === 0 ? 0 : m < 45 ? 1 : m < 120 ? 2 : m < 240 ? 3 : 4;
    cells.push(html`<span data-l=${level} title=${`${formatDay(key)}: ${formatMinutes(m)}`}></span>`);
  }
  // Show the most recent weeks first on narrow screens.
  const toEnd = ref(el => { if (el) requestAnimationFrame(() => { el.scrollLeft = el.scrollWidth; }); });
  return html`<div class="heatmap" ${toEnd} role="img" aria-label=${`Play calendar for ${year}: ${days.length} days with sessions`}>${cells}</div>`;
}

function barChart(items, label) {
  const max = Math.max(1, ...items.map(i => i.value));
  return html`<div class="bars" role="img" aria-label=${label}>${items.map(i => html`<div class="bar-col">
    <span class="bar-fill" style=${`height:${Math.round((i.value / max) * 100)}%`} title=${i.title}></span><span>${i.label}</span></div>`)}</div>`;
}

function hbars(items) {
  const max = Math.max(1, ...items.map(i => i.count));
  return html`<ul class="hbars">${items.map(i => html`<li><span class="name">${i.name}</span>
    <span class="bar"><span style=${`width:${Math.round((i.count / max) * 100)}%`}></span></span><span class="num">${i.count}</span></li>`)}</ul>`;
}

export function statsView() {
  const s = stats.data;
  const y = stats.year;
  const thisYear = new Date().getFullYear();
  if (!s) {
    return html`<div class="page"><header class="page-head"><h1>Stats</h1></header>
      ${stats.error ? empty({ iconName: 'wifi-off', title: 'Stats need a connection', body: stats.error.message }) : skeletonRows(6)}</div>`;
  }
  const ratingLabels = ['½', '1', '1½', '2', '2½', '3', '3½', '4', '4½', '5'];
  const backlogPace = s.year_finished > 0 && s.backlog_count > 0
    ? Math.ceil(s.backlog_count / (s.year_finished / Math.max(1, new Date().getMonth() + 1))) : 0;
  return html`<div class="page">
    <header class="page-head"><h1>Stats</h1>
      <div class="row">
        <a class="btn btn-ghost btn-icon" href=${`#you/stats?year=${y - 1}`} aria-label="Previous year">${icon('chevron-left')}</a>
        <strong class="num">${y}</strong>
        ${y < thisYear ? html`<a class="btn btn-ghost btn-icon" href=${`#you/stats?year=${y + 1}`} aria-label="Next year">${icon('chevron-right')}</a>` : nothing}
      </div>
    </header>
    <div class="stat-tiles">
      <div class="stat-tile"><div class="value">${formatMinutes(s.year_minutes)}</div><div class="label">played in ${y}</div></div>
      <div class="stat-tile"><div class="value">${s.year_finished}</div><div class="label">finished in ${y}</div></div>
      <div class="stat-tile"><div class="value">${s.year_days_played}</div><div class="label">days with play</div></div>
      <div class="stat-tile"><div class="value">${s.streak_longest || 0}</div><div class="label">longest streak (days)</div></div>
    </div>

    <section class="section"><div class="section-head"><h2>Play calendar</h2><span class="muted">${plural(s.year_sessions, 'session')}</span></div>
      ${heatmap(s.heatmap, y)}</section>

    <section class="section"><div class="section-head"><h2>Hours per month</h2></div>
      ${barChart(s.minutes_by_month.map(m => ({ value: m.minutes, label: monthName(Number(m.month.slice(5)) - 1).slice(0, 1), title: `${monthName(Number(m.month.slice(5)) - 1, true)} ${m.month.slice(0, 4)}: ${formatMinutes(m.minutes)}` })), 'Hours played per month over the last year')}</section>

    <section class="section"><div class="section-head"><h2>Finished per year</h2></div>
      ${s.by_year.length ? html`<ul class="hbars">${s.by_year.map(yr => html`<li><span class="name">${yr.year}</span>
        <span class="bar"><span style=${`width:${Math.round((yr.finished / Math.max(1, ...s.by_year.map(x => x.finished))) * 100)}%`}></span></span>
        <span class="num" title=${Object.entries(yr.by_completion).map(([k, v]) => `${completionLabel(k) || 'Unspecified'}: ${v}`).join(', ')}>${yr.finished}</span></li>`)}</ul>
        <p class="muted" style="margin-top:var(--sp-2);font-size:var(--fs-14)">${s.by_year[0] ? Object.entries(s.by_year[0].by_completion).map(([k, v]) => `${completionLabel(k) || 'Unspecified'} ${v}`).join(' · ') : ''}</p>`
        : html`<p class="muted">Finish a game to start this chart.</p>`}</section>

    <section class="section"><div class="section-head"><h2>Your ratings</h2><span class="muted">average ${s.avg_rating ? (s.avg_rating / 20).toFixed(1) + ' ★' : '—'}</span></div>
      ${barChart(s.rating_buckets.map((n, i) => ({ value: n, label: ratingLabels[i], title: `${ratingLabels[i]} stars: ${n}` })), 'Rating distribution')}</section>

    <div class="section" style="display:grid;gap:var(--sp-6);grid-template-columns:repeat(auto-fit,minmax(260px,1fr))">
      ${s.top_genres.length ? html`<section><div class="section-head"><h2>Top genres</h2></div>${hbars(s.top_genres)}</section>` : nothing}
      ${s.top_platforms.length ? html`<section><div class="section-head"><h2>Top platforms</h2></div>${hbars(s.top_platforms)}</section>` : nothing}
      ${s.top_tags.length ? html`<section><div class="section-head"><h2>Top tags</h2></div>${hbars(s.top_tags)}</section>` : nothing}
    </div>

    <section class="section card"><div class="section-head"><h2>Backlog</h2><a href="#library?shelf=backlog">Open</a></div>
      <p><strong class="num">${s.backlog_count}</strong> games waiting${s.backlog_over_2y ? html` · <strong>${s.backlog_over_2y}</strong> for more than 2 years` : s.backlog_over_1y ? html` · <strong>${s.backlog_over_1y}</strong> for more than a year` : ''}.</p>
      ${s.backlog_ttb_minutes ? html`<p class="muted">About <strong>${formatHoursLong(s.backlog_ttb_minutes)}</strong> to clear (time-to-beat known for ${s.backlog_with_ttb} of them).</p>` : nothing}
      ${backlogPace ? html`<p class="muted">At this year’s pace that’s about ${plural(backlogPace, 'month')}.</p>` : nothing}
      ${s.backlog_oldest.length ? html`<div class="shelf-row" style="margin-top:var(--sp-3)">${s.backlog_oldest.map(b => html`<a class="upnext-card" href=${`#game/${b.game_id}`}>
        ${cover({ name: b.name, cover: b.cover })}<span class="title">${b.name}</span><span class="reason">since ${formatDay(b.added_at.slice(0, 10))}</span></a>`)}</div>` : nothing}
    </section>

    ${s.longest_session ? html`<section class="section"><div class="section-head"><h2>Longest session in ${y}</h2></div>
      <a class="list-row" href=${`#game/${s.longest_session.game_id}`}>${cover({ name: s.longest_session.game_name, cover: s.longest_session.cover }, { cls: 'thumb' })}
        <div class="grow"><div class="title">${s.longest_session.game_name}</div><div class="sub">${formatMinutes(s.longest_session.minutes)} on ${formatDay(s.longest_session.played_on)}</div></div></a></section>` : nothing}

    <p class="muted section">All time: ${plural(s.total_games, 'game')}, ${formatHoursLong(s.total_minutes)}, ${plural(s.total_finished, 'finish', 'finishes')}.</p>
  </div>`;
}

// ---- Diary ----------------------------------------------------------------------

const diary = { month: '', items: [], loading: false, error: null };

export async function enterDiary(route) {
  const month = /^\d{4}-\d{2}$/.test(route.query.month || '') ? route.query.month : localDay().slice(0, 7);
  diary.month = month;
  diary.loading = true;
  app.redraw();
  const [y, m] = month.split('-').map(Number);
  const last = new Date(y, m, 0).getDate();
  try {
    diary.items = (await api.get(`/api/v1/sessions?from=${month}-01&to=${month}-${String(last).padStart(2, '0')}`)).items;
    diary.error = null;
  } catch (err) {
    diary.error = err;
  }
  diary.loading = false;
  app.redraw();
}

function shiftMonth(month, by) {
  const [y, m] = month.split('-').map(Number);
  const d = new Date(y, m - 1 + by, 1);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`;
}

export function diaryView() {
  const month = diary.month || localDay().slice(0, 7);
  const [y, m] = month.split('-').map(Number);
  const first = new Date(y, m - 1, 1);
  const days = new Date(y, m, 0).getDate();
  const weekStartSunday = app.prefs.week_start === 'sun';
  const lead = weekStartSunday ? first.getDay() : (first.getDay() + 6) % 7;
  const byDay = new Map();
  for (const s of diary.items) {
    if (!byDay.has(s.played_on)) byDay.set(s.played_on, []);
    byDay.get(s.played_on).push(s);
  }
  const today = localDay();
  const dows = weekStartSunday ? ['S', 'M', 'T', 'W', 'T', 'F', 'S'] : ['M', 'T', 'W', 'T', 'F', 'S', 'S'];
  const total = diary.items.reduce((n, s) => n + s.minutes, 0);
  return html`<div class="page">
    <header class="page-head"><h1>Diary</h1>
      <div class="row">
        <a class="btn btn-ghost btn-icon" href=${`#you/diary?month=${shiftMonth(month, -1)}`} aria-label="Previous month">${icon('chevron-left')}</a>
        <strong>${monthName(m - 1, true)} ${y}</strong>
        ${month < today.slice(0, 7) ? html`<a class="btn btn-ghost btn-icon" href=${`#you/diary?month=${shiftMonth(month, 1)}`} aria-label="Next month">${icon('chevron-right')}</a>` : nothing}
      </div></header>
    <p class="muted">${plural(diary.items.length, 'session')} · ${formatMinutes(total)}</p>
    <div class="calendar section" role="group" aria-label=${`${monthName(m - 1, true)} ${y}`}>
      ${dows.map(d => html`<span class="dow" aria-hidden="true">${d}</span>`)}
      ${Array.from({ length: lead }, () => html`<span class="day blank"></span>`)}
      ${Array.from({ length: days }, (_, i) => {
        const key = `${month}-${String(i + 1).padStart(2, '0')}`;
        const has = byDay.has(key);
        return html`<a class=${`day ${has ? 'played' : ''} ${key === today ? 'today' : ''}`} href=${has ? `#you/diary?month=${month}&day=${key}` : nothing}
          aria-label=${`${formatDay(key)}${has ? `: ${formatMinutes(byDay.get(key).reduce((n, s) => n + s.minutes, 0))}` : ''}`}
          @click=${has ? e => { e.preventDefault(); document.getElementById(`d-${key}`)?.scrollIntoView({ behavior: 'smooth' }); } : null}>${i + 1}</a>`;
      })}
    </div>
    ${diary.loading && !diary.items.length ? skeletonRows(4)
      : diary.error ? empty({ iconName: 'wifi-off', title: 'The diary needs a connection', body: diary.error.message })
      : !diary.items.length ? empty({ iconName: 'book-open', title: 'No sessions this month', body: 'Log time from Home or the ⊕ button and it appears here.' })
      : [...byDay.keys()].sort().reverse().map(day => html`<section class="diary-day" id=${`d-${day}`}>
          <h3>${relativeDay(day)}</h3>
          <div class="list">${byDay.get(day).map(s => html`<a class="list-row" href=${`#game/${s.game_id}`}>
            ${cover({ name: s.game_name, cover: s.cover }, { cls: 'thumb' })}
            <div class="grow"><div class="title">${s.game_name}</div><div class="sub">${formatMinutes(s.minutes)}${s.source === 'timer' ? ' · timer' : ''}${s.note ? ` · ${s.note}` : ''}</div></div>
          </a>`)}</div></section>`)}
  </div>`;
}
