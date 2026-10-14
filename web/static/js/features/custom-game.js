// custom-game.js — add a game the catalog doesn't have (a demo, a
// regional release, homebrew): name, platform, year, optional cover.
import { html, nothing } from '../ui/lit.js';
import { icon } from '../ui/icons.js';
import { openSheet } from '../ui/sheet.js';
import { toastError } from '../ui/toast.js';
import { api } from '../core/api.js';
import { STATUSES } from '../core/format.js';
import { app } from './app-state.js';
import * as actions from './actions.js';
import { go } from '../core/router.js';

const MAX_COVER = 700 * 1024; // the API body limit is 1 MiB of JSON

function readAsDataURL(file) {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(r.result);
    r.onerror = () => reject(r.error);
    r.readAsDataURL(file);
  });
}

export function openCustomGameSheet(name = '') {
  const f = { name, platform: (app.prefs.default_platforms || [])[0] || '', year: '', status: 'backlog', cover: '', error: '' };
  openSheet({
    title: 'Add a game yourself',
    content: s => html`<form class="form-grid" id="custom-game" @submit=${e => { e.preventDefault(); s.close(true); }}>
      <p class="muted">For games the catalog doesn't have. Only you see it until someone else adds it too.</p>
      <label class="field"><span>Name</span><input class="input" required maxlength="200" .value=${f.name} @input=${e => { f.name = e.target.value; }}></label>
      <div class="row">
        <label class="field" style="flex:2"><span>Platform</span><input class="input" maxlength="64" .value=${f.platform} @input=${e => { f.platform = e.target.value; }}></label>
        <label class="field" style="flex:1"><span>Year</span><input class="input" type="number" min="1950" max=${new Date().getFullYear() + 10} .value=${f.year} @input=${e => { f.year = e.target.value; }}></label>
      </div>
      <label class="field"><span>Cover (optional)</span>
        <input class="input" type="file" accept="image/jpeg,image/png,image/gif" @change=${async e => {
          const file = e.target.files[0];
          f.error = '';
          if (file && file.size > MAX_COVER) { f.error = 'Pick an image under 700 KB.'; f.cover = ''; s.update(); return; }
          f.cover = file ? await readAsDataURL(file) : '';
          s.update();
        }}></label>
      ${f.error ? html`<p class="field-error">${f.error}</p>` : nothing}
      ${f.cover ? html`<img src=${f.cover} alt="Cover preview" style="width:96px;border-radius:var(--r-sm)">` : nothing}
      <label class="field"><span>Shelf</span><select class="select" @change=${e => { f.status = e.target.value; }}>
        ${STATUSES.map(st => html`<option value=${st.value} ?selected=${st.value === f.status}>${st.label}</option>`)}</select></label>
    </form>`,
    footer: s => html`<button type="submit" form="custom-game" class="btn btn-primary btn-block">${icon('plus')}Add game</button>`,
  }).closed.then(async ok => {
    if (!ok || !f.name.trim()) return;
    try {
      const res = await api.post('/api/v1/games', { name: f.name.trim(), platform: f.platform.trim(), year: Number(f.year) || 0, cover: f.cover });
      const g = res.game;
      await actions.addGame({ id: g.id, name: g.name, cover: g.cover, release_date: g.release_date, platforms: g.platforms }, f.status,
        f.platform.trim() ? { platforms: [f.platform.trim()] } : {});
      go(`#game/${g.id}`);
    } catch (err) {
      toastError(err);
    }
  });
}
