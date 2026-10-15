// swipe.js — swipe navigation on touch screens: a long, clearly horizontal
// swipe on the page goes back (to the right) or forward (to the left).
// Not from the screen edges, where the browser's or OS's own back gesture
// lives; not inside horizontal scrollers, fields or the rating slider; not
// while a sheet or menu is open; not when turned off in Settings. Touch
// events keep arriving while the page scrolls, unlike pointer events.
//
// Every sideways gesture is reported to the server log with what came of
// it (navigated, ignored and why, cancelled by the browser, too short), so
// a swipe that does nothing on a phone we can't test says why.
import { anySheetOpen } from '../ui/sheet.js';
import { api } from '../core/api.js';
import { app } from './app-state.js';

const EDGE = 24; // px from either side left to the system gesture
const START = 14; // px of movement before deciding the direction
const COMMIT = 90; // px of horizontal travel to navigate
const REPORT = 40; // px of sideways travel worth reporting
const MAX_REPORTS = 20; // per page

let swipe = null;
let reports = 0;
// The element the current touch started on: touchmove/touchend are sent
// there even after a re-render removes it from the page (and then they
// don't bubble to the document), so the gesture listens on it.
let bound = null;
const OPTS = { passive: true };

function bind(target) {
  unbind();
  bound = target;
  target.addEventListener('touchmove', onMove, OPTS);
  target.addEventListener('touchend', onEnd, OPTS);
  target.addEventListener('touchcancel', onCancel, OPTS);
}

function unbind() {
  if (!bound) return;
  bound.removeEventListener('touchmove', onMove, OPTS);
  bound.removeEventListener('touchend', onEnd, OPTS);
  bound.removeEventListener('touchcancel', onCancel, OPTS);
  bound = null;
}

// ignored says why a swipe starting here doesn't navigate ('' if it may).
function ignored(target, x) {
  if (app.prefs.swipe_nav === false) return 'turned off';
  if (x < EDGE || x > innerWidth - EDGE) return 'screen edge';
  if (anySheetOpen() || document.querySelector('.menu')) return 'sheet or menu open';
  if (target.closest?.('input, textarea, select, [contenteditable="true"], [role="slider"], video')) return 'field';
  // A horizontally scrollable ancestor (shelves, chips, calendar) keeps
  // the gesture. <main> scrolls vertically: its overflow-x computes to
  // auto too, so only real horizontal overflow counts.
  for (let el = target; el && el !== document.body; el = el.parentElement) {
    if (el.scrollWidth > el.clientWidth + 1 && /(auto|scroll)/.test(getComputedStyle(el).overflowX)) {
      return `scroller ${el.tagName.toLowerCase()}${el.className && typeof el.className === 'string' ? '.' + el.className.trim().split(/\s+/).join('.') : ''}`;
    }
  }
  return '';
}

function hint() {
  let el = document.getElementById('swipe-hint');
  if (!el) {
    el = Object.assign(document.createElement('div'), { id: 'swipe-hint', className: 'swipe-hint' });
    el.setAttribute('aria-hidden', 'true');
    document.body.append(el);
  }
  return el;
}

function hideHint() {
  const el = document.getElementById('swipe-hint');
  if (el) {
    el.style.opacity = '0';
    el.classList.remove('ready');
  }
}

function reset() {
  swipe = null;
  unbind();
  hideHint();
}

function report(s, outcome) {
  if (Math.abs(s.dx) < REPORT || Math.abs(s.dx) < Math.abs(s.dy) || reports >= MAX_REPORTS) return;
  reports++;
  api.post('/api/v1/diagnostics/swipe', {
    outcome: outcome.slice(0, 200), dx: Math.round(s.dx), dy: Math.round(s.dy), start_x: Math.round(s.x),
    width: innerWidth, ms: Math.round(performance.now() - s.at),
  }).catch(() => {});
}

function onStart(e) {
  reset();
  if (e.touches.length !== 1) return;
  const t = e.touches[0];
  swipe = { x: t.clientX, y: t.clientY, horizontal: false, vertical: false, dx: 0, dy: 0, at: performance.now(), why: ignored(e.target, t.clientX) };
  bind(e.target);
}

function onMove(e) {
  if (!swipe || e.touches.length !== 1) return;
  const t = e.touches[0];
  swipe.dx = t.clientX - swipe.x;
  swipe.dy = t.clientY - swipe.y;
  if (swipe.why || swipe.vertical) return;
  const dx = Math.abs(swipe.dx);
  const dy = Math.abs(swipe.dy);
  if (!swipe.horizontal) {
    if (dy > START && dy >= dx) {
      swipe.vertical = true; // a scroll
      return;
    }
    if (dx < START || dx < 2 * dy) return;
    swipe.horizontal = true;
  }
  const el = hint();
  const progress = Math.min(1, dx / COMMIT);
  el.dataset.dir = swipe.dx > 0 ? 'back' : 'forward';
  el.style.opacity = String(progress);
  el.classList.toggle('ready', progress >= 1);
}

function onEnd() {
  const s = swipe;
  reset();
  if (!s) return;
  if (s.why) return report(s, `ignored: ${s.why}`);
  if (s.vertical) return report(s, 'ignored: began as a scroll');
  if (!s.horizontal) return report(s, 'ignored: not horizontal enough');
  if (Math.abs(s.dx) < COMMIT) return report(s, 'too short');
  report(s, s.dx > 0 ? 'back' : 'forward');
  if (s.dx > 0) history.back();
  else history.forward();
}

// The browser took the touch over (its own gesture, a scroll it claimed).
function onCancel() {
  const s = swipe;
  reset();
  if (s) report(s, s.horizontal ? 'cancelled by the browser' : 'cancelled by the browser before deciding');
}

const handlers = { touchstart: onStart, touchmove: onMove, touchend: onEnd, touchcancel: onCancel };

// install starts listening, after replaying the touches the shell recorded
// while this module loaded ({type, target, touches: [{clientX, clientY}]}).
// A gesture still in progress continues through bind()'s listeners.
export function install(recorded = []) {
  for (const e of recorded) handlers[e.type]?.(e);
  document.addEventListener('touchstart', onStart, OPTS);
}
