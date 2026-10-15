// swipe.js — swipe navigation on touch screens: a long, clearly horizontal
// swipe on the page goes back (to the right) or forward (to the left).
// Not from the screen edges, where the browser's or OS's own back gesture
// lives; not inside horizontal scrollers, fields or the rating slider; not
// while a sheet or menu is open; not when turned off in Settings. Touch
// events keep arriving while the page scrolls, unlike pointer events.
import { anySheetOpen } from '../ui/sheet.js';
import { app } from './app-state.js';

const EDGE = 24; // px from either side left to the system gesture
const START = 14; // px of movement before deciding the direction
const COMMIT = 90; // px of horizontal travel to navigate

let swipe = null;

function blocked(target) {
  if (app.prefs.swipe_nav === false) return true;
  if (anySheetOpen() || document.querySelector('.menu')) return true;
  if (target.closest?.('input, textarea, select, [contenteditable="true"], [role="slider"], video')) return true;
  // A horizontally scrollable ancestor (shelves, chips, calendar) keeps
  // the gesture. <main> scrolls vertically: its overflow-x computes to
  // auto too, so only real horizontal overflow counts.
  for (let el = target; el && el !== document.body; el = el.parentElement) {
    if (el.scrollWidth > el.clientWidth + 1 && /(auto|scroll)/.test(getComputedStyle(el).overflowX)) return true;
  }
  return false;
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

function reset() {
  swipe = null;
  const el = document.getElementById('swipe-hint');
  if (el) {
    el.style.opacity = '0';
    el.classList.remove('ready');
  }
}

function onStart(e) {
  reset();
  if (e.touches.length !== 1) return;
  const t = e.touches[0];
  if (t.clientX < EDGE || t.clientX > innerWidth - EDGE || blocked(e.target)) return;
  swipe = { x: t.clientX, y: t.clientY, horizontal: false, dx: 0 };
}

function onMove(e) {
  if (!swipe || e.touches.length !== 1) return;
  const t = e.touches[0];
  const dx = t.clientX - swipe.x;
  const dy = t.clientY - swipe.y;
  if (!swipe.horizontal) {
    if (Math.abs(dy) > START && Math.abs(dy) >= Math.abs(dx)) {
      swipe = null; // a vertical scroll
      return;
    }
    if (Math.abs(dx) < START || Math.abs(dx) < 2 * Math.abs(dy)) return;
    swipe.horizontal = true;
  }
  swipe.dx = dx;
  const el = hint();
  const progress = Math.min(1, Math.abs(dx) / COMMIT);
  el.dataset.dir = dx > 0 ? 'back' : 'forward';
  el.style.opacity = String(progress);
  el.classList.toggle('ready', progress >= 1);
}

function onEnd() {
  const s = swipe;
  reset();
  if (!s?.horizontal || Math.abs(s.dx) < COMMIT) return;
  if (s.dx > 0) history.back();
  else history.forward();
}

// install starts listening; first is the touchstart that loaded the module.
export function install(first) {
  document.addEventListener('touchstart', onStart, { passive: true });
  document.addEventListener('touchmove', onMove, { passive: true });
  document.addEventListener('touchend', onEnd, { passive: true });
  document.addEventListener('touchcancel', reset, { passive: true });
  if (first) onStart(first);
}
