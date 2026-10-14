// gallery.js — the living style guide (#dev/ui, only when CATO_DEV=1):
// every component in each state, for interaction, axe and visual tests.
import { html } from '../ui/lit.js';
import { icon } from '../ui/icons.js';
import { cover, statusPill, empty, skeletonTiles, segmented, statusControl, chip } from '../ui/parts.js';
import { stars, ratingInput } from '../ui/stars.js';
import { openSheet, confirmSheet } from '../ui/sheet.js';
import { openMenu } from '../ui/menu.js';
import { toast } from '../ui/toast.js';
import { STATUSES } from '../core/format.js';
import { app } from '../features/app-state.js';

const st = { status: 'playing', rating: 70, seg: 'a', pressed: true };

const TOKENS = ['--bg', '--surface-1', '--surface-2', '--surface-3', '--text', '--text-muted', '--accent', '--accent-soft',
  '--success', '--warning', '--danger', '--star'];

export function galleryView() {
  const sample = { id: 0, name: 'Hollow Knight', cover: '', status: 'playing' };
  const rerender = () => app.redraw();
  return html`<div class="page gallery">
    <header class="page-head"><h1>Component gallery</h1></header>
    <p class="muted">Every component in each state. Only served with CATO_DEV=1.</p>

    <section aria-labelledby="g-tokens"><h2 id="g-tokens">Colour tokens</h2>
      <div class="swatches">${TOKENS.map(t => html`<div class="swatch" style=${`background:var(${t});color:${t.includes('text') || t === '--accent' || t === '--danger' || t === '--success' || t === '--warning' ? 'var(--bg)' : 'var(--text)'}`}>${t}</div>`)}</div></section>

    <section aria-labelledby="g-buttons"><h2 id="g-buttons">Buttons</h2>
      <div class="row row-wrap">
        <button class="btn btn-primary">Primary</button><button class="btn btn-secondary">Secondary</button>
        <button class="btn btn-ghost">Ghost</button><button class="btn btn-danger">Danger</button>
        <button class="btn btn-primary" disabled>Disabled</button>
        <button class="btn btn-sm btn-primary">Small</button><button class="btn btn-secondary btn-icon" aria-label="Settings">${icon('settings')}</button>
        <button class="link">Link button</button>
      </div></section>

    <section aria-labelledby="g-chips"><h2 id="g-chips">Chips</h2>
      <div class="chips">${chip('Toggle', { pressed: st.pressed, onClick: () => { st.pressed = !st.pressed; rerender(); } })}
        <button class="chip chip-time">+30m</button><button class="chip is-on chip-remove">RPG ${icon('x')}</button>
        <button class="chip">${icon('bookmark')}Want</button></div></section>

    <section aria-labelledby="g-status"><h2 id="g-status">Status</h2>
      <div class="row row-wrap">${STATUSES.map(s => statusPill(s.value))}</div>
      <div style="margin-top:var(--sp-3)">${statusControl(st.status, v => { st.status = v; rerender(); })}</div></section>

    <section aria-labelledby="g-seg"><h2 id="g-seg">Segmented control</h2>
      ${segmented({ label: 'Example', value: st.seg, onChange: v => { st.seg = v; rerender(); }, options: [{ value: 'a', label: 'One' }, { value: 'b', label: 'Two' }, { value: 'c', label: 'Three' }] })}</section>

    <section aria-labelledby="g-stars"><h2 id="g-stars">Stars</h2>
      <div class="row row-wrap">${[0, 10, 50, 70, 90, 100].map(r => html`<span>${stars(r, { showEmpty: true })}</span>`)}</div>
      <div style="margin-top:var(--sp-3)">${ratingInput(st.rating, v => { st.rating = v; rerender(); })}</div>
      <div class="row">${stars(85, { display: 'ten' })} ${stars(85, { display: 'hundred' })}</div></section>

    <section aria-labelledby="g-fields"><h2 id="g-fields">Fields</h2>
      <div class="form-grid" style="max-width:420px">
        <label class="field"><span>Text</span><input class="input" placeholder="Placeholder"></label>
        <label class="field"><span>Select</span><select class="select"><option>One</option><option>Two</option></select></label>
        <label class="field"><span>Notes</span><textarea class="textarea"></textarea></label>
        <label class="switch"><span>Switch</span><input type="checkbox" checked></label>
        <div class="search">${icon('search', 'icon-search')}<input type="search" aria-label="Search" placeholder="Search"></div>
      </div></section>

    <section aria-labelledby="g-cards"><h2 id="g-cards">Covers and cards</h2>
      <div class="lib-grid" style="max-width:480px">${[sample, { ...sample, name: 'A very long game title that wraps onto two lines', status: 'completed' }, { ...sample, status: 'wishlist' }]
        .map(e => html`<a class="game-tile" href="#dev/ui"><span class="tile-status">${statusPill(e.status)}</span>${cover(e)}<span class="tile-name">${e.name}</span></a>`)}</div></section>

    <section aria-labelledby="g-overlays"><h2 id="g-overlays">Sheets, menus and toasts</h2>
      <div class="row row-wrap">
        <button class="btn btn-secondary" @click=${() => openSheet({ title: 'Example sheet', content: () => html`<p>Sheet body.</p>` })}>Open sheet</button>
        <button class="btn btn-secondary" @click=${() => confirmSheet({ title: 'Confirm?', message: 'For rare destructive actions only.', confirm: 'Delete', danger: true })}>Confirm sheet</button>
        <button class="btn btn-secondary" @click=${e => openMenu({ anchor: e.currentTarget, items: [{ label: 'First', icon: 'play' }, { label: 'Checked', checked: true }, { separator: true }, { label: 'Danger', icon: 'trash-2', danger: true }] })}>Open menu</button>
        <button class="btn btn-secondary" @click=${() => toast('Logged 30m on Hades', { action: { label: 'Undo', run: () => toast('Undone') } })}>Toast with undo</button>
        <button class="btn btn-secondary" @click=${() => toast('Something went wrong', { kind: 'error' })}>Error toast</button>
      </div></section>

    <section aria-labelledby="g-empty"><h2 id="g-empty">Empty state and skeleton</h2>
      ${empty({ iconName: 'gamepad-2', title: 'Nothing here yet', body: 'Empty states suggest the next action.', action: html`<button class="btn btn-primary">Do it</button>` })}
      ${skeletonTiles(3)}</section>

    <section aria-labelledby="g-ring"><h2 id="g-ring">Progress</h2>
      <div class="row"><div class="ring" style="--p:62"><span>62%</span></div><div class="bar" style="width:200px"><span style="width:40%"></span></div></div></section>
  </div>`;
}
