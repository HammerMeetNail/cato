// sheet.js — the one sheet component: a modal <dialog> (the rest of the
// page is inert while it's open), bottom sheet on phones and centred panel
// on wider screens, dismissed by Escape, the backdrop or a swipe down on
// the grip, returning focus to whatever opened it.
import { html, render, nothing } from './lit.js';
import { icon } from './icons.js';

const open = [];

// openSheet renders content(api) into a new dialog. api: {close(result),
// update()}. Returns api plus a promise `closed` resolving to the result.
export function openSheet({ title = '', content, cls = '', label = title, onClose, initialFocus = null, footer = null }) {
  const opener = document.activeElement;
  const dialog = document.createElement('dialog');
  dialog.className = `sheet ${cls}`;
  dialog.setAttribute('aria-label', label || 'Dialog');
  document.body.append(dialog);
  let result;
  let resolveClosed;
  const closed = new Promise(r => { resolveClosed = r; });
  const api = {
    dialog,
    closed,
    close(value) {
      result = value;
      if (dialog.open) dialog.close();
    },
    update: () => draw(),
    setTitle(t) { title = t; draw(); },
  };
  const grip = html`<div class="sheet-grip" aria-hidden="true" @pointerdown=${startSwipe}></div>`;
  function draw() {
    render(html`${grip}
      ${title ? html`<header class="sheet-head">
        <h2>${title}</h2>
        <button type="button" class="btn btn-ghost btn-icon" aria-label="Close" @click=${() => api.close()}>${icon('x')}</button>
      </header>` : nothing}
      <div class="sheet-body">${content(api)}</div>
      ${footer ? html`<footer class="sheet-foot">${footer(api)}</footer>` : nothing}`, dialog);
  }

  let startY = 0;
  let dy = 0;
  function startSwipe(e) {
    startY = e.clientY;
    dy = 0;
    const move = ev => {
      dy = Math.max(0, ev.clientY - startY);
      dialog.style.transform = `translateY(${dy}px)`;
    };
    const up = () => {
      removeEventListener('pointermove', move);
      removeEventListener('pointerup', up);
      dialog.style.transform = '';
      if (dy > 80) api.close();
    };
    addEventListener('pointermove', move);
    addEventListener('pointerup', up);
  }

  dialog.addEventListener('click', e => {
    // A click on the dialog box itself (not its content) is the backdrop.
    if (e.target === dialog) {
      const r = dialog.getBoundingClientRect();
      const inside = e.clientX >= r.left && e.clientX <= r.right && e.clientY >= r.top && e.clientY <= r.bottom;
      if (!inside) api.close();
    }
  });
  dialog.addEventListener('close', () => {
    const i = open.indexOf(api);
    if (i >= 0) open.splice(i, 1);
    render(nothing, dialog);
    dialog.remove();
    onClose?.(result);
    resolveClosed(result);
    if (opener && opener.isConnected && typeof opener.focus === 'function') opener.focus({ preventScroll: true });
  });

  draw();
  dialog.showModal();
  open.push(api);
  const focusTarget = initialFocus ? dialog.querySelector(initialFocus) : null;
  if (focusTarget) focusTarget.focus();
  else dialog.querySelector('.sheet-body [autofocus], .sheet-body input, .sheet-body button')?.focus({ preventScroll: true });
  return api;
}

export function anySheetOpen() {
  return open.length > 0;
}

export function closeAllSheets() {
  for (const s of [...open]) s.close();
}

// confirmSheet asks a yes/no question (only for destructive, rare actions:
// routine actions use undo instead).
export function confirmSheet({ title, message, confirm = 'Confirm', danger = false }) {
  const s = openSheet({
    title,
    content: () => html`<p>${message}</p>`,
    footer: api => html`
      <button type="button" class="btn btn-secondary" @click=${() => api.close(false)}>Cancel</button>
      <span class="spacer"></span>
      <button type="button" class=${`btn ${danger ? 'btn-danger' : 'btn-primary'}`} @click=${() => api.close(true)}>${confirm}</button>`,
  });
  return s.closed.then(v => v === true);
}
