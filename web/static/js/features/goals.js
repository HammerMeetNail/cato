// goals.js — You → Goals (plan §10): yearly targets with progress rings.
// Gentle by design: no streak pressure, no nagging, just the ring.
import { html, nothing } from '../ui/lit.js';
import { icon } from '../ui/icons.js';
import { segmented, empty, skeletonRows } from '../ui/parts.js';
import { ring } from '../ui/ring.js';
import { openSheet } from '../ui/sheet.js';
import { toast, toastError } from '../ui/toast.js';
import { api } from '../core/api.js';
import { app } from './app-state.js';

const st = { year: new Date().getFullYear(), items: null, error: null };

export const KINDS = [
  { value: 'finish', label: 'Finish games', unit: 'games', example: 24 },
  { value: 'backlog', label: 'Clear the backlog', unit: 'games', example: 10 },
  { value: 'retro', label: 'Play older games', unit: 'games', example: 5 },
  { value: 'hours', label: 'Play hours', unit: 'hours', example: 200 },
];

export async function enterGoals(route) {
  st.year = Number(route.query.year) || new Date().getFullYear();
  try { st.items = (await api.get(`/api/v1/goals?year=${st.year}`)).items; st.error = null; } catch (err) { st.error = err; }
  app.redraw();
}

export function goalCard(g) {
  return html`<div class="goal">${ring(g.progress, g.target, { label: g.label })}
    <div class="grow"><div class="title">${g.label}</div><div class="sub num">${g.progress} of ${g.target}${g.progress >= g.target ? ' · done!' : ''}</div></div></div>`;
}

export function addGoal(year = new Date().getFullYear(), onDone = null) {
  const f = { kind: 'finish', target: 24, param: 2000 };
  openSheet({
    title: `A goal for ${year}`,
    content: s => html`<div class="form-grid">
      ${segmented({ label: 'Goal', value: f.kind, options: KINDS.map(k => ({ value: k.value, label: k.label })), cls: 'segmented-wrap',
        onChange: v => { f.kind = v; f.target = KINDS.find(k => k.value === v).example; s.update(); } })}
      <label class="field"><span>How many ${KINDS.find(k => k.value === f.kind).unit}?</span>
        <input class="input" type="number" min="1" max="100000" inputmode="numeric" .value=${String(f.target)} @input=${e => { f.target = Number(e.target.value); }}></label>
      ${f.kind === 'retro' ? html`<label class="field"><span>Released before</span>
        <input class="input" type="number" min="1970" max="2100" .value=${String(f.param)} @input=${e => { f.param = Number(e.target.value); }}></label>` : nothing}
      <p class="muted">${f.kind === 'backlog' ? 'Counts games you finish this year that were already in your library on January 1.'
        : f.kind === 'retro' ? 'Counts games you play this year that came out before that year.'
        : f.kind === 'hours' ? 'Counts the play time you log this year.' : 'Counts every finish this year, replays included.'}</p>
    </div>`,
    footer: s => html`<span class="spacer"></span><button type="button" class="btn btn-primary" @click=${async () => {
      try {
        await api.post('/api/v1/goals', { year, kind: f.kind, target: f.target, param: f.kind === 'retro' ? f.param : 0 });
        s.close();
        toast('Goal added');
        onDone?.();
      } catch (err) { toastError(err); }
    }}>Add goal</button>`,
  });
}

export function goalsView() {
  const items = st.items;
  return html`<div class="page">
    <header class="page-head"><h1>Goals</h1>
      <button type="button" class="btn btn-primary btn-sm" @click=${() => addGoal(st.year, () => enterGoals({ query: { year: st.year } }))}>${icon('plus')}Add a goal</button></header>
    <p class="muted"><a href=${`#you/goals?year=${st.year - 1}`}>${st.year - 1}</a> · <strong>${st.year}</strong>${st.year < new Date().getFullYear() ? html` · <a href=${`#you/goals?year=${st.year + 1}`}>${st.year + 1}</a>` : nothing}</p>
    ${st.error ? empty({ iconName: 'triangle-alert', title: 'Couldn’t load your goals', body: st.error.message })
      : !items ? skeletonRows(2)
      : items.length ? html`<div class="goal-list section">${items.map(g => html`<div class="card row">${goalCard(g)}
          <button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label=${`Remove ${g.label}`} @click=${async () => {
            try { await api.del(`/api/v1/goals/${g.id}`); enterGoals({ query: { year: st.year } }); } catch (err) { toastError(err); }
          }}>${icon('trash-2')}</button></div>`)}</div>`
      : empty({ iconName: 'target', title: 'No goals yet', body: 'Something to aim for this year: finish 24 games, clear 10 from the backlog, play some classics.',
        action: html`<button type="button" class="btn btn-primary" @click=${() => addGoal(st.year, () => enterGoals({ query: { year: st.year } }))}>${icon('plus')}Add a goal</button>` })}
  </div>`;
}
