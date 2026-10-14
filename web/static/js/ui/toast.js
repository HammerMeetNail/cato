// toast.js — the one toast system. A new toast replaces the current one.
// Routine actions happen at once and offer Undo here for a few seconds
// (design principle 2: undo, don't ask).
import { html, render, nothing } from './lit.js';

let region;
let timer;
let current = null;

// prepareToasts creates the live region up front: screen readers announce
// changes to a region that already exists, not one inserted with its text
// (and the first toast then costs no extra layout).
export function prepareToasts() {
  ensureRegion();
}

function ensureRegion() {
  if (region) return region;
  region = document.createElement('div');
  region.className = 'toast-region';
  region.setAttribute('role', 'status');
  region.setAttribute('aria-live', 'polite');
  document.body.append(region);
  return region;
}

// toast shows message; action {label, run} adds a button (Undo). Returns a
// dismiss function.
export function toast(message, { action = null, kind = 'info', duration = action ? 7000 : 3500 } = {}) {
  ensureRegion();
  clearTimeout(timer);
  const id = Symbol('toast');
  current = id;
  const dismiss = () => {
    if (current !== id) return;
    current = null;
    render(nothing, region);
  };
  render(html`<div class=${`toast ${kind === 'error' ? 'toast-error' : ''}`}>
    <span class="toast-msg">${message}</span>
    ${action ? html`<button type="button" class="btn btn-sm toast-action" @click=${async () => {
      dismiss();
      try { await action.run(); } catch (err) { toast(err.message || 'That didn’t work', { kind: 'error' }); }
    }}>${action.label}</button>` : nothing}
  </div>`, region);
  timer = setTimeout(dismiss, duration);
  return dismiss;
}

export function toastError(err, fallback = 'Something went wrong') {
  return toast(err?.message || fallback, { kind: 'error', duration: 6000 });
}
