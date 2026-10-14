// timer.js — the session timer: started on the server so it shows (and
// keeps running) on every device; "Stop & log" turns it into a session.
import { html, nothing } from '../ui/lit.js';
import { icon } from '../ui/icons.js';
import { openSheet } from '../ui/sheet.js';
import { toast, toastError } from '../ui/toast.js';
import { api } from '../core/api.js';
import { formatElapsed, formatMinutes, localDay } from '../core/format.js';
import { app } from './app-state.js';

let tick = null;
let asked = false;

function elapsedMs() {
  if (!app.timer) return 0;
  return Date.now() + app.serverSkew - Date.parse(app.timer.started_at);
}

function startTicking() {
  clearInterval(tick);
  if (!app.timer) return;
  tick = setInterval(() => {
    for (const el of document.querySelectorAll('[data-timer-elapsed]')) el.textContent = formatElapsed(elapsedMs());
  }, 1000);
}

function setTimer(res) {
  app.timer = res?.timer ?? null;
  if (res?.server_time) app.serverSkew = Date.parse(res.server_time) - Date.now();
  startTicking();
  app.redraw();
}

// refreshTimer loads the running timer (on start and when the app returns
// to the foreground). A timer left running for hours asks what to do.
export async function refreshTimer() {
  try {
    setTimer(await api.get('/api/v1/timer'));
  } catch {
    return;
  }
  if (app.timer && !asked && elapsedMs() > 4 * 3600 * 1000) {
    asked = true;
    openStopSheet({ stale: true });
  }
}

export async function startTimer(gameId) {
  try {
    setTimer(await api.post('/api/v1/timer', { game_id: gameId }));
    toast(`Session started: ${app.timer.game_name}`);
  } catch (err) {
    toastError(err);
  }
}

export function openStopSheet({ stale = false } = {}) {
  if (!app.timer) return;
  const t = app.timer;
  let minutes = Math.max(1, Math.min(1440, Math.round(elapsedMs() / 60000)));
  const sheet = openSheet({
    title: stale ? `Still playing ${t.game_name}?` : `Stop session · ${t.game_name}`,
    content: s => html`<div class="form-grid">
      ${stale ? html`<p>This timer has been running since ${new Date(t.started_at).toLocaleString()}. Log the time you actually played, or discard it.</p>` : nothing}
      <div class="row" style="justify-content:center">
        <button type="button" class="btn btn-secondary btn-icon" aria-label="15 minutes less" @click=${() => { minutes = Math.max(1, minutes - 15); s.update(); }}>${icon('minus')}</button>
        <output class="num" style="font-size:var(--fs-28);font-weight:800;min-width:5em;text-align:center">${formatMinutes(minutes)}</output>
        <button type="button" class="btn btn-secondary btn-icon" aria-label="15 minutes more" @click=${() => { minutes = Math.min(1440, minutes + 15); s.update(); }}>${icon('plus')}</button>
      </div>
    </div>`,
    footer: s => html`
      <button type="button" class="btn btn-ghost" @click=${() => s.close('discard')}>Discard</button>
      <span class="spacer"></span>
      <button type="button" class="btn btn-primary" @click=${() => s.close('log')}>Stop &amp; log ${formatMinutes(minutes)}</button>`,
  });
  return sheet.closed.then(async choice => {
    if (choice !== 'log' && choice !== 'discard') return;
    try {
      const res = await api.post('/api/v1/timer/stop', { log: choice === 'log', minutes, played_on: localDay() });
      setTimer(null);
      if (choice === 'log') {
        await app.store.sync();
        toast(`Logged ${formatMinutes(res.session?.minutes ?? minutes)} on ${t.game_name}`, res.undo_id ? {
          action: { label: 'Undo', run: async () => { await api.post(`/api/v1/undo/${res.undo_id}`); await app.store.sync(); } },
        } : {});
      }
    } catch (err) {
      toastError(err);
    }
  });
}

// timerPill is shown in the header on every screen while a timer runs.
export function timerPill() {
  if (!app.timer) return nothing;
  return html`<button type="button" class="timer-pill" @click=${() => openStopSheet()} aria-label=${`Session running for ${app.timer.game_name}. Stop and log.`}>
    <span class="dot" aria-hidden="true"></span><span class="num" data-timer-elapsed>${formatElapsed(elapsedMs())}</span>
    <span class="visually-hidden">${app.timer.game_name}</span>${icon('square')}
  </button>`;
}
