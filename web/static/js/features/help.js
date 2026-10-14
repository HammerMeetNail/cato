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
      <p><strong>Privacy.</strong> Your profile starts private: only you see it. In Settings → Profile and privacy you can share it with followers you approve or with everyone on this server. Reviews and lists can each have their own setting. Notes are only ever visible to you.</p>
    </section>
    <section class="card form-grid">
      <h2 style="font-size:var(--fs-18)">${icon('users')} People</h2>
      <p><strong>Following.</strong> Find people in You → People, or open an invite link someone sent you (you'll follow each other). Their starts, finishes, reviews and lists appear under Friends on Home, and their reviews come first on game pages.</p>
      <p><strong>Reviews.</strong> Write one from a game page. Mark it as containing spoilers and it stays hidden until someone taps it.</p>
      <p><strong>Blocking and reporting.</strong> Blocking someone hides you from each other completely. Report a review, list or profile from its ⋯ menu; the server's admins will see it.</p>
    </section>
    <section class="card form-grid">
      <h2 style="font-size:var(--fs-18)">${icon('bell')} Notifications and alerts</h2>
      <p>Your Inbox always has follows, likes, invites and wishlist alerts. To get them on your phone too, turn on notifications in Settings → Notifications. On iPhone and iPad, add Cato to your Home Screen first (Share → Add to Home Screen) and turn them on from there.</p>
      <p>Wishlist alerts tell you when a game comes out on your platforms. With a price-alerts key on the server, Cato also watches IsThereAnyDeal for discounts on your wishlist.</p>
    </section>
    <section class="card form-grid">
      <h2 style="font-size:var(--fs-18)">${icon('share-2')} Adding games from anywhere</h2>
      <p><strong>Android:</strong> install Cato, then use Share → Cato on a Steam, eShop, PlayStation Store, IGDB or YouTube page. Cato opens the ⊕ sheet already searched for that game.</p>
      <p><strong>iPhone, iPad and computers:</strong> copy the page's link and paste it into the ⊕ search; Cato reads the game from it. With Apple Shortcuts, a share-sheet shortcut whose one action is “Open URLs” with <span class="mono">${location.origin}/share?url=</span> followed by the Shortcut Input does the same in one tap.</p>
      <p><strong>Barcodes:</strong> the barcode button in the ⊕ sheet reads the box of a physical game with your camera (Android and desktop Chrome) or lets you type the number.</p>
      <p><strong>Imports:</strong> You → Import brings in Steam, Grouvee, HowLongToBeat, RetroAchievements, any spreadsheet, or another Cato's export. You check the matches before anything is saved, and can undo an import later.</p>
    </section>
    <section class="card form-grid">
      <h2 style="font-size:var(--fs-18)">${icon('key-round')} Signing in</h2>
      <p>Cato keeps you signed in on a device as long as you use it at least every 90 days. Add a passkey in Settings to sign in with your fingerprint, face or device PIN. Forgot your password? An admin can make you a one-time reset link.</p>
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
