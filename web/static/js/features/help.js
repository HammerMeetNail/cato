// help.js — You → Help: how Cato works, shortcuts, attribution.
import { html } from '../ui/lit.js';
import { icon } from '../ui/icons.js';

export const SHORTCUTS = [
  ['1', 'Home'], ['2', 'Library'], ['3', 'Discover'], ['4', 'You'],
  ['L', 'Log a game (the ⊕ sheet)'], ['/', 'Search'], ['⌘K / Ctrl K', 'Command palette'],
  ['j / k or arrows', 'Move between games in the Library'], ['Enter', 'Open the focused game'],
  ['s', 'Status of the focused game'], ['r', 'Rate the focused game'], ['l', 'Log time on the focused game'],
  ['Esc', 'Close a sheet or menu'],
];

export function helpView() {
  return html`<div class="page"><div class="settings-grid">
    <header class="page-head"><h1>Help</h1></header>
    <section class="card form-grid">
      <h2 style="font-size:var(--fs-18)">${icon('lightbulb')} The basics</h2>
      <p><strong>Shelves.</strong> Every game is on one shelf: Want, Backlog, Playing, Paused, Finished or Dropped. Change it from the game page or with a long press (right-click on a computer) anywhere a game appears.</p>
      <p><strong>Logging time.</strong> Tap +15m, +30m, +1h or +2h on Home, in the ⊕ sheet or on the game page. Each tap is a session in your Diary. “Start session” runs a timer you can stop from any device.</p>
      <p><strong>Finishing.</strong> Finish asks for stars and how far you got (main story, extras, 100%). Playing a finished game again starts a new playthrough; the old one stays in your history.</p>
      <p><strong>Undo.</strong> Cato never asks “are you sure?” for everyday actions. It does them and offers Undo for a few seconds.</p>
      <p><strong>Search.</strong> The Library search filters your own games instantly. Type <kbd>$</kbd> for a tag and <kbd>@</kbd> for a platform, for example <kbd>$co-op @switch</kbd>. “Search all games” looks through the whole catalog.</p>
      <p><strong>Offline.</strong> Installed to your home screen, Cato opens without a connection. Your library is stored on the device, and changes you make offline are sent when you reconnect.</p>
      <p><strong>Privacy.</strong> Your library is private. Notes are only ever visible to you.</p>
    </section>
    <section class="card">
      <h2 style="font-size:var(--fs-18);margin-bottom:var(--sp-3)">${icon('keyboard')} Keyboard shortcuts</h2>
      <dl class="kv">${SHORTCUTS.map(([k, v]) => html`<dt><kbd>${k}</kbd></dt><dd>${v}</dd>`)}</dl>
    </section>
    <section class="card">
      <h2 style="font-size:var(--fs-18);margin-bottom:var(--sp-3)">${icon('info')} About</h2>
      <p>Game data <a href="https://www.igdb.com" rel="noopener noreferrer" target="_blank">powered by IGDB</a>. Icons by Lucide (ISC). Rendering by lit-html (BSD-3-Clause).</p>
    </section>
  </div></div>`;
}
