// clipboard.js — copy text, including on plain HTTP (the NAS), where
// browsers don't offer the Clipboard API: there, copy from a selected field.

export async function copyText(text, field = null) {
  if (navigator.clipboard && globalThis.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch { /* fall back below */ }
  }
  let el = field;
  let temp = null;
  if (!el) {
    temp = Object.assign(document.createElement('textarea'), { value: text, readOnly: true });
    Object.assign(temp.style, { position: 'fixed', top: '0', left: '0', opacity: '0' });
    // Inside an open modal <dialog> the rest of the page is inert: add the
    // field to the dialog so it can be selected.
    (document.querySelector('dialog[open]') || document.body).append(temp);
    el = temp;
  }
  try {
    el.focus();
    el.select();
    el.setSelectionRange?.(0, text.length);
    return document.execCommand('copy');
  } catch {
    return false;
  } finally {
    temp?.remove();
  }
}
