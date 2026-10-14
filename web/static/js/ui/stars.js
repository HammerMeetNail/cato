// stars.js — rating display and input. Ratings are 0–100; the display
// preference picks 5 stars with halves (default), 10 points or 100 points.
import { html, nothing } from './lit.js';
import { starIcon } from './icons.js';
import { toStars, fromStars, formatRating, ratingAria } from '../core/format.js';

// stars shows a rating; nothing for unrated games unless showEmpty.
export function stars(rating, { display = 'stars', size = 'sm', showEmpty = false } = {}) {
  if (!rating && !showEmpty) return nothing;
  if (display !== 'stars') {
    return html`<span class="rating-num" aria-label=${ratingAria(rating, display)}>${formatRating(rating, display)}</span>`;
  }
  const s = toStars(rating);
  return html`<span class="stars stars-${size}" role="img" aria-label=${ratingAria(rating)}>
    ${[1, 2, 3, 4, 5].map(i => html`<span class="star">${starIcon(s >= i ? 1 : s >= i - 0.5 ? 0.5 : 0)}</span>`)}
  </span>`;
}

// ratingInput is a slider in half-star steps: tap or drag across the stars,
// arrow keys step by half a star, 0 or Backspace clears. onChange receives
// the 0–100 value (multiples of 10). With display "hundred" it is a range.
export function ratingInput(rating, onChange, { display = 'stars', label = 'Your rating' } = {}) {
  if (display === 'hundred') {
    return html`<label class="field"><span>${label}: ${rating || '—'}</span>
      <input class="range" type="range" min="0" max="100" step="1" .value=${String(rating || 0)}
        aria-valuetext=${ratingAria(rating, display)}
        @change=${e => onChange(Number(e.target.value))}></label>`;
  }
  const value = toStars(rating);
  let dragging = false;
  const fromPointer = (e) => {
    const box = e.currentTarget.getBoundingClientRect();
    const x = Math.min(Math.max(e.clientX - box.left, 0), box.width);
    // Left edge clears; otherwise round up to the next half star.
    if (x < box.width * 0.04) return 0;
    return Math.min(5, Math.ceil((x / box.width) * 10) / 2);
  };
  // While dragging, move the fill without a re-render.
  const preview = (el, v) => {
    el.querySelectorAll('.star').forEach((star, i) => {
      const f = v >= i + 1 ? 1 : v >= i + 0.5 ? 0.5 : 0;
      star.querySelector('clipPath rect')?.setAttribute('width', String(24 * f));
    });
  };
  return html`<div class="stars stars-input" role="slider" tabindex="0" aria-label=${label}
      aria-valuemin="0" aria-valuemax="5" aria-valuenow=${value}
      aria-valuetext=${ratingAria(rating, display)}
      @pointerdown=${e => { dragging = true; e.currentTarget.setPointerCapture(e.pointerId); }}
      @pointermove=${e => { if (dragging) preview(e.currentTarget, fromPointer(e)); }}
      @pointerup=${e => {
        if (!dragging) return;
        dragging = false;
        const v = fromPointer(e);
        // Tapping the current value again clears it.
        onChange(v === value ? 0 : fromStars(v));
      }}
      @pointercancel=${() => { dragging = false; }}
      @keydown=${e => {
        let v = value;
        if (e.key === 'ArrowRight' || e.key === 'ArrowUp') v = Math.min(5, v + 0.5);
        else if (e.key === 'ArrowLeft' || e.key === 'ArrowDown') v = Math.max(0, v - 0.5);
        else if (e.key === 'Home' || e.key === '0' || e.key === 'Backspace' || e.key === 'Delete') v = 0;
        else if (e.key === 'End') v = 5;
        else if (/^[1-5]$/.test(e.key)) v = Number(e.key);
        else return;
        e.preventDefault();
        if (v !== value) onChange(fromStars(v));
      }}>
    ${[1, 2, 3, 4, 5].map(i => html`<span class="star">${starIcon(value >= i ? 1 : value >= i - 0.5 ? 0.5 : 0)}</span>`)}
    ${display === 'ten' ? html`<span class="rating-num" style="margin-left:8px">${rating ? formatRating(rating, 'ten') : ''}</span>` : nothing}
  </div>`;
}
