// sheets.js — the Finish sheet, custom time entry and the quick-actions
// menu shared by every screen that shows a game.
import { html, nothing } from '../ui/lit.js';
import { openSheet } from '../ui/sheet.js';
import { openMenu } from '../ui/menu.js';
import { icon } from '../ui/icons.js';
import { ratingInput } from '../ui/stars.js';
import { cover } from '../ui/parts.js';
import { COMPLETIONS, STATUSES, formatMinutes, localDay, statusLabel } from '../core/format.js';
import { app, ratingDisplay, savePrefs } from './app-state.js';
import * as actions from './actions.js';
import { go } from '../core/router.js';
import { celebrate } from './delight.js';

// openFinishSheet: "You finished X 🎉" → stars → completion → date → Done.
// Three taps from the Finish chip: Finish, a star, Done.
export function openFinishSheet(id) {
  const e = app.store.get(id);
  if (!e) return;
  const state = { rating: e.rating || 0, completion: '', day: localDay(), notes: null };
  const sheet = openSheet({
    title: `You finished ${e.name} 🎉`,
    label: `Finish ${e.name}`,
    content: s => html`
      <div class="form-grid">
        <div class="row">${cover(e, { cls: 'thumb' })}<div class="grow"><div class="muted">${formatMinutes(e.minutes)} played</div></div></div>
        <div class="field">
          <span id="finish-rating">How was it?</span>
          ${ratingInput(state.rating, v => { state.rating = v; s.update(); }, { display: ratingDisplay(), label: 'Your rating' })}
        </div>
        <div class="field">
          <span>How far did you get?</span>
          <div class="chips">${COMPLETIONS.map(c => html`<button type="button" class="chip" aria-pressed=${String(state.completion === c.value)}
            @click=${() => { state.completion = state.completion === c.value ? '' : c.value; s.update(); }}>${c.label}</button>`)}</div>
        </div>
        <label class="field"><span>Finished on</span>
          <input class="input" type="date" .value=${state.day} max=${localDay()} @change=${ev => { state.day = ev.target.value || localDay(); }}></label>
        <label class="field"><span>A few words (private notes)</span>
          <textarea class="textarea" placeholder="What stuck with you?" @input=${ev => { state.notes = ev.target.value; }}></textarea></label>
      </div>`,
    footer: s => html`
      <button type="button" class="btn btn-ghost" @click=${() => s.close('skip')}>Skip</button>
      <span class="spacer"></span>
      <button type="button" class="btn btn-primary" data-finish-done @click=${() => s.close('done')}>${icon('check')} Done</button>`,
  });
  return sheet.closed.then(result => {
    if (result !== 'done' && result !== 'skip') return;
    const opts = { finishedOn: state.day };
    if (result === 'done') {
      if (state.rating) opts.rating = state.rating;
      if (state.completion) opts.completion = state.completion;
      if (state.notes) opts.notes = [e.notes, state.notes].filter(Boolean).join('\n\n');
    }
    actions.finish(id, opts).catch(() => {});
    if (app.prefs.confetti) celebrate();
  });
}

// openTimeSheet: custom play time with a stepper (minutes or hours),
// never a select with hundreds of options.
export function openTimeSheet(id) {
  const e = app.store.get(id);
  if (!e) return;
  const state = { minutes: 45, unit: 'min', day: localDay(), note: '' };
  const step = d => {
    const delta = state.unit === 'min' ? d * 15 : d * 60;
    state.minutes = Math.min(1440, Math.max(state.unit === 'min' ? 5 : 60, state.minutes + delta));
  };
  const sheet = openSheet({
    title: `Log time · ${e.name}`,
    content: s => html`<div class="form-grid">
      <div class="row" style="justify-content:center">
        <button type="button" class="btn btn-secondary btn-icon" aria-label="Less" @click=${() => { step(-1); s.update(); }}>${icon('minus')}</button>
        <output class="stepper" aria-live="polite"><span class="num" style="font-size:var(--fs-28);font-weight:800">${formatMinutes(state.minutes)}</span></output>
        <button type="button" class="btn btn-secondary btn-icon" aria-label="More" @click=${() => { step(1); s.update(); }}>${icon('plus')}</button>
      </div>
      <div class="segmented" role="radiogroup" aria-label="Step by">
        ${[['min', '15 minutes'], ['hour', '1 hour']].map(([v, l]) => html`<button type="button" role="radio" aria-checked=${String(state.unit === v)}
          @click=${() => { state.unit = v; s.update(); }}>Step ${l}</button>`)}
      </div>
      <label class="field"><span>Played on</span>
        <input class="input" type="date" .value=${state.day} max=${localDay()} @change=${ev => { state.day = ev.target.value || localDay(); }}></label>
      <label class="field"><span>Note (optional)</span>
        <input class="input" maxlength="500" placeholder="Beat the second boss" @input=${ev => { state.note = ev.target.value; }}></label>
    </div>`,
    footer: s => html`<button type="button" class="btn btn-primary btn-block" @click=${() => s.close(true)}>Log ${formatMinutes(state.minutes)}</button>`,
  });
  return sheet.closed.then(ok => {
    if (ok) actions.logTime(id, state.minutes, { note: state.note, playedOn: state.day }).catch(() => {});
  });
}

export function openRateSheet(id) {
  const e = app.store.get(id);
  if (!e) return;
  let rating = e.rating;
  openSheet({
    title: `Rate ${e.name}`,
    content: s => html`${ratingInput(rating, v => { rating = v; s.update(); }, { display: ratingDisplay() })}`,
    footer: s => html`<button type="button" class="btn btn-primary btn-block" @click=${() => s.close(true)}>Save</button>`,
  }).closed.then(ok => { if (ok && rating !== e.rating) actions.rate(id, rating).catch(() => {}); });
}

function toggleUpNext(id) {
  const queue = [...(app.prefs.up_next || [])];
  const i = queue.indexOf(id);
  if (i >= 0) queue.splice(i, 1);
  else {
    if (queue.length >= 5) queue.shift();
    queue.push(id);
  }
  return savePrefs({ up_next: queue });
}

// gameMenu is the long-press / right-click quick actions menu.
export function gameMenu(id, position) {
  const e = app.store.get(id);
  if (!e) return;
  const inQueue = (app.prefs.up_next || []).includes(id);
  openMenu({
    ...position,
    label: e.name,
    items: [
      { heading: e.name },
      ...STATUSES.map(s => ({ label: s.label, icon: s.icon, checked: e.status === s.value, run: () => e.status !== s.value && (s.value === 'completed' ? openFinishSheet(id) : actions.setStatus(id, s.value)) })),
      { separator: true },
      { label: 'Log time…', icon: 'timer', run: () => openTimeSheet(id) },
      { label: 'Rate…', icon: 'star', run: () => openRateSheet(id) },
      { label: inQueue ? 'Remove from Up Next' : 'Add to Up Next', icon: 'list-checks', run: () => toggleUpNext(id) },
      { label: 'Open game page', icon: 'external-link', run: () => go(`#game/${id}`) },
      { separator: true },
      { label: 'Remove from library', icon: 'trash-2', danger: true, run: () => actions.removeGame(id) },
    ],
  });
}

// timeChips: the one-tap logging row.
export function timeChips(id, { finishChip = true, more = true } = {}) {
  return html`<div class="time-chips" role="group" aria-label="Log time">
    ${[15, 30, 60, 120].map(m => html`<button type="button" class="chip chip-time" @click=${() => actions.logTime(id, m).catch(() => {})}
      aria-label=${`Log ${formatMinutes(m)}`}>+${formatMinutes(m)}</button>`)}
    ${more ? html`<button type="button" class="chip" @click=${() => openTimeSheet(id)} aria-label="Log custom time">${icon('timer')}Custom</button>` : nothing}
    ${finishChip ? html`<button type="button" class="chip" data-finish @click=${() => openFinishSheet(id)}>${icon('circle-check')}Finish</button>` : nothing}
  </div>`;
}

export { statusLabel };
