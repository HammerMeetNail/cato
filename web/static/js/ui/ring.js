// ring.js — a progress ring (goals on Home and You → Goals).
import { html, svg } from './lit.js';

export function ring(progress, target, { size = 64, label = '' } = {}) {
  const pct = target > 0 ? Math.min(1, progress / target) : 0;
  const r = 26;
  const c = 2 * Math.PI * r;
  return html`<span class="ring" style=${`width:${size}px;height:${size}px`} role="img"
    aria-label=${`${label ? `${label}: ` : ''}${progress} of ${target}`}>
    ${svg`<svg viewBox="0 0 64 64" width=${size} height=${size} aria-hidden="true">
      <circle cx="32" cy="32" r=${r} fill="none" stroke="var(--surface-3)" stroke-width="7"></circle>
      <circle cx="32" cy="32" r=${r} fill="none" stroke=${pct >= 1 ? 'var(--status-completed)' : 'var(--accent)'} stroke-width="7"
        stroke-linecap="round" stroke-dasharray=${`${c * pct} ${c}`} transform="rotate(-90 32 32)"></circle></svg>`}
    <span class="ring-text num" aria-hidden="true">${pct >= 1 ? '✓' : `${Math.round(pct * 100)}%`}</span></span>`;
}
