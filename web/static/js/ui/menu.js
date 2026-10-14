// menu.js — the one popup menu (long-press and right-click quick actions,
// overflow buttons). Arrow keys move, Escape closes, focus returns to the
// opener.
import { html, render, nothing } from './lit.js';
import { icon } from './icons.js';

let active = null;

export function closeMenu() {
  active?.close();
}

// openMenu items: [{label, icon, run, danger, checked, separator, heading}].
// Position at {x, y} or under anchor (an element).
export function openMenu({ items, anchor = null, x = 0, y = 0, label = 'Actions' }) {
  closeMenu();
  const opener = document.activeElement;
  const el = document.createElement('div');
  el.className = 'menu';
  el.setAttribute('role', 'menu');
  el.setAttribute('aria-label', label);
  document.body.append(el);
  const close = () => {
    if (active?.el !== el) return;
    active = null;
    removeEventListener('pointerdown', outside, true);
    removeEventListener('keydown', keys, true);
    removeEventListener('resize', close);
    render(nothing, el);
    el.remove();
    opener?.focus?.({ preventScroll: true });
  };
  const run = async item => {
    close();
    await item.run?.();
  };
  render(html`${items.map(item => item.separator ? html`<hr>`
    : item.heading ? html`<div class="menu-label">${item.heading}</div>`
      : html`<button type="button" role=${item.checked === undefined ? 'menuitem' : 'menuitemradio'}
          aria-checked=${item.checked === undefined ? nothing : String(!!item.checked)}
          class=${item.danger ? 'danger' : ''} @click=${() => run(item)}>
          ${item.icon ? icon(item.icon) : nothing}<span>${item.label}</span>
          ${item.checked ? html`<span class="spacer"></span>${icon('check')}` : nothing}
        </button>`)}`, el);

  const box = anchor?.getBoundingClientRect();
  const w = el.offsetWidth;
  const h = el.offsetHeight;
  let left = box ? box.right - w : x;
  let top = box ? box.bottom + 4 : y;
  left = Math.max(8, Math.min(left, innerWidth - w - 8));
  if (top + h > innerHeight - 8) top = Math.max(8, (box ? box.top - h - 4 : y - h));
  el.style.left = `${left}px`;
  el.style.top = `${top}px`;

  const buttons = () => [...el.querySelectorAll('button')];
  function outside(e) {
    if (!el.contains(e.target)) close();
  }
  function keys(e) {
    const list = buttons();
    const i = list.indexOf(document.activeElement);
    if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); close(); }
    else if (e.key === 'ArrowDown') { e.preventDefault(); list[(i + 1) % list.length]?.focus(); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); list[(i - 1 + list.length) % list.length]?.focus(); }
    else if (e.key === 'Tab') close();
  }
  addEventListener('pointerdown', outside, true);
  addEventListener('keydown', keys, true);
  addEventListener('resize', close);
  active = { el, close };
  buttons()[0]?.focus();
  return active;
}

// longPress calls fn(event) after a 500 ms press without much movement, and
// on right-click; it suppresses the click that follows a long press.
export function longPress(fn) {
  let timer = null;
  let start = null;
  let fired = false;
  return {
    pointerdown(e) {
      if (e.pointerType === 'mouse') return;
      fired = false;
      start = { x: e.clientX, y: e.clientY };
      const target = e.currentTarget;
      timer = setTimeout(() => {
        fired = true;
        navigator.vibrate?.(10);
        fn({ x: start.x, y: start.y, target });
      }, 500);
    },
    pointermove(e) {
      if (timer && start && Math.hypot(e.clientX - start.x, e.clientY - start.y) > 10) {
        clearTimeout(timer);
        timer = null;
      }
    },
    pointerup() { clearTimeout(timer); timer = null; },
    pointercancel() { clearTimeout(timer); timer = null; },
    contextmenu(e) {
      e.preventDefault();
      clearTimeout(timer);
      fn({ x: e.clientX, y: e.clientY, target: e.currentTarget });
    },
    click(e) {
      if (fired) {
        e.preventDefault();
        e.stopImmediatePropagation();
        fired = false;
      }
    },
  };
}
