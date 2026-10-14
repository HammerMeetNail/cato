// parts.js — small shared pieces: covers, status pills, empty states,
// skeletons, segmented controls and chips.
import { html, nothing } from './lit.js';
import { icon } from './icons.js';
import { statusLabel, STATUSES, coverAlt } from '../core/format.js';

// coverSources picks the right size: local covers have an @2x variant,
// IGDB URLs are rewritten to the size the slot needs (thumbnails small).
export function coverSources(src, thumb = false) {
  if (!src) return { src: '', srcset: '' };
  const local = /^\/covers\/(-?\d+)\.jpg(\?.*)?$/.exec(src);
  if (local) return { src, srcset: local[2] ? '' : `${src} 1x, /covers/${local[1]}@2x.jpg 2x` };
  if (src.startsWith('https://images.igdb.com/') && src.includes('/t_cover_big/')) {
    if (thumb) return { src: src.replace('/t_cover_big/', '/t_cover_small/'), srcset: `${src.replace('/t_cover_big/', '/t_cover_small/')} 1x, ${src} 2x` };
    return { src, srcset: `${src} 1x, ${src.replace('/t_cover_big/', '/t_cover_big_2x/')} 2x` };
  }
  return { src, srcset: '' };
}

// cover renders a game cover with a text fallback when the image is
// missing or fails (no inline handlers: CSP).
export function cover(entry, { cls = '', eager = false } = {}) {
  const { src, srcset } = coverSources(entry?.cover, cls.includes('thumb'));
  const fallback = html`<span class="cover-fallback">${entry?.name ?? ''}</span>`;
  return html`<div class=${`cover ${cls}`}>
    ${src ? html`<img src=${src} srcset=${srcset || nothing} alt=${coverAlt(entry.name)} loading=${eager ? 'eager' : 'lazy'} decoding="async"
      @error=${e => { e.target.replaceWith(Object.assign(document.createElement('span'), { className: 'cover-fallback', textContent: entry.name })); }}>` : fallback}
  </div>`;
}

export function statusPill(status) {
  if (!status) return nothing;
  return html`<span class=${`status status-${status}`}>${statusLabel(status)}</span>`;
}

export function empty({ iconName = 'gamepad-2', title, body = '', action = nothing }) {
  return html`<div class="empty">
    ${icon(iconName)}
    <h2>${title}</h2>
    ${body ? html`<p>${body}</p>` : nothing}
    ${action}
  </div>`;
}

export function skeletonTiles(n = 9) {
  return html`<div class="lib-grid" aria-busy="true" aria-label="Loading">
    ${Array.from({ length: n }, () => html`<div><div class="cover skeleton"></div><div class="skeleton skeleton-line"></div></div>`)}
  </div>`;
}

export function skeletonRows(n = 4) {
  return html`<div class="list" aria-busy="true" aria-label="Loading">
    ${Array.from({ length: n }, () => html`<div class="list-row"><div class="thumb cover skeleton"></div><div class="grow"><div class="skeleton skeleton-line"></div><div class="skeleton skeleton-line" style="width:60%"></div></div></div>`)}
  </div>`;
}

// segmented is a single-choice control (radio group semantics).
export function segmented({ label, value, options, onChange, cls = '' }) {
  const keydown = e => {
    const i = options.findIndex(o => o.value === value);
    let n = i;
    if (e.key === 'ArrowRight' || e.key === 'ArrowDown') n = (i + 1) % options.length;
    else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') n = (i - 1 + options.length) % options.length;
    else return;
    e.preventDefault();
    onChange(options[n].value);
    requestAnimationFrame(() => e.currentTarget?.querySelectorAll('button')[n]?.focus());
  };
  return html`<div class=${`segmented ${cls}`} role="radiogroup" aria-label=${label} @keydown=${keydown}>
    ${options.map(o => html`<button type="button" role="radio" aria-checked=${String(o.value === value)}
      tabindex=${o.value === value || (!options.some(x => x.value === value) && o === options[0]) ? '0' : '-1'}
      style=${o.color ? `--seg-color: ${o.color}` : nothing}
      @click=${() => onChange(o.value)}>${o.icon ? icon(o.icon) : nothing}${o.label}</button>`)}
  </div>`;
}

// statusControl is the one status control (design principle 7).
export function statusControl(value, onChange, { label = 'Status' } = {}) {
  return segmented({
    label, value, onChange, cls: 'status-seg',
    options: STATUSES.map(s => ({ value: s.value, label: s.label, color: `var(--status-${s.value})` })),
  });
}

export function chip(label, { pressed = undefined, onClick, cls = '', iconName = '', aria = '' } = {}) {
  return html`<button type="button" class=${`chip ${cls}`} aria-pressed=${pressed === undefined ? nothing : String(pressed)}
    aria-label=${aria || nothing} @click=${onClick}>${iconName ? icon(iconName) : nothing}${label}</button>`;
}
