// palette.js — the command palette (Cmd/Ctrl-K): jump to a game in your
// library or the catalog, run an action, or go somewhere.
import { html, nothing, live } from '../ui/lit.js';
import { icon } from '../ui/icons.js';
import { openSheet } from '../ui/sheet.js';
import { cover } from '../ui/parts.js';
import { api } from '../core/api.js';
import { query, parseSearch, normalize } from '../core/filter.js';
import { statusLabel } from '../core/format.js';
import { go } from '../core/router.js';
import { app, savePrefs } from './app-state.js';
import { openLogSheet } from './log.js';

let isOpen = false;

const COMMANDS = [
  { label: 'Log a game', icon: 'circle-plus', run: () => openLogSheet() },
  { label: 'Go to Home', icon: 'house', run: () => go('#home') },
  { label: 'Go to Library', icon: 'layout-grid', run: () => go('#library') },
  { label: 'Go to Discover', icon: 'compass', run: () => go('#discover') },
  { label: 'Go to Stats', icon: 'chart-column', run: () => go('#you/stats') },
  { label: 'Go to Diary', icon: 'calendar-days', run: () => go('#you/diary') },
  { label: 'Go to Settings', icon: 'settings', run: () => go('#you/settings') },
  { label: 'Show my backlog', icon: 'layers', run: () => go('#library?shelf=backlog') },
  { label: 'Show what I’m playing', icon: 'play', run: () => go('#library?shelf=playing') },
  { label: 'Switch to dark theme', icon: 'moon', run: () => savePrefs({ theme: 'dark' }) },
  { label: 'Switch to light theme', icon: 'sun', run: () => savePrefs({ theme: 'light' }) },
  { label: 'Use the system theme', icon: 'monitor', run: () => savePrefs({ theme: 'system' }) },
  { label: 'Keyboard shortcuts', icon: 'keyboard', run: () => go('#you/help') },
];

export function addCommand(cmd) {
  COMMANDS.push(cmd);
}

export function openPalette() {
  if (isOpen) return;
  isOpen = true;
  const st = { q: '', active: 0, catalog: [] };
  let timer = null;
  let ctl = null;
  const items = () => {
    const q = st.q.trim();
    const nq = normalize(q);
    const cmds = COMMANDS.filter(c => !nq || normalize(c.label).includes(nq)).slice(0, q ? 5 : 8)
      .map(c => ({ kind: 'cmd', ...c }));
    if (!q) return cmds;
    const mine = query(app.store.entries(), { search: parseSearch(q) }).slice(0, 6)
      .map(e => ({ kind: 'game', label: e.name, sub: statusLabel(e.status), entry: e, run: () => go(`#game/${e.id}`) }));
    const ids = new Set(mine.map(m => m.entry.id));
    const cat = st.catalog.filter(g => !ids.has(g.id)).slice(0, 6)
      .map(g => ({ kind: 'game', label: g.name, sub: g.status ? statusLabel(g.status) : 'Not in your library', entry: g, run: () => go(`#game/${g.id}`) }));
    return [...mine, ...cmds, ...cat];
  };
  const sheet = openSheet({
    label: 'Command palette',
    content: s => {
      const list = items();
      st.active = Math.min(st.active, Math.max(0, list.length - 1));
      return html`<div class="search">${icon('search', 'icon-search')}
          <input type="search" role="combobox" aria-expanded="true" aria-controls="palette-list" aria-label="Search games and commands"
            placeholder="Search games, go to, do…" autocomplete="off" .value=${live(st.q)}
            aria-activedescendant=${list.length ? `pal-${st.active}` : nothing}
            @input=${e => {
              st.q = e.target.value;
              st.active = 0;
              clearTimeout(timer);
              ctl?.abort();
              if (st.q.trim().length >= 2) {
                timer = setTimeout(async () => {
                  ctl = new AbortController();
                  try { st.catalog = (await api.get(`/api/v1/typeahead?q=${encodeURIComponent(st.q.trim())}`, { signal: ctl.signal })).catalog; } catch { st.catalog = []; }
                  s.update();
                }, 150);
              } else st.catalog = [];
              s.update();
            }}
            @keydown=${e => {
              if (e.key === 'ArrowDown') { e.preventDefault(); st.active = Math.min(list.length - 1, st.active + 1); s.update(); }
              else if (e.key === 'ArrowUp') { e.preventDefault(); st.active = Math.max(0, st.active - 1); s.update(); }
              else if (e.key === 'Enter' && list[st.active]) { e.preventDefault(); s.close(list[st.active]); }
            }}></div>
        <div class="palette-results" id="palette-list" role="listbox" aria-label="Results">
          ${list.map((it, i) => html`<button type="button" class="list-row" role="option" id=${`pal-${i}`} aria-selected=${String(i === st.active)}
              @click=${() => s.close(it)}>
            ${it.kind === 'game' ? cover(it.entry, { cls: 'thumb' }) : icon(it.icon)}
            <div class="grow"><div class="title">${it.label}</div>${it.sub ? html`<div class="sub">${it.sub}</div>` : nothing}</div>
          </button>`)}
          ${!list.length ? html`<p class="muted">No matches.</p>` : nothing}
        </div>`;
    },
    initialFocus: 'input',
    onClose: () => { isOpen = false; clearTimeout(timer); ctl?.abort(); },
  });
  sheet.closed.then(it => it?.run?.());
}
