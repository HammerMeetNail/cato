// reach.js — Settings sections for staying in touch (plan §8): push
// notifications on this device and what they're for, wishlist alerts, the
// weekly email, passkeys, and connected accounts.
import { html, nothing } from '../ui/lit.js';
import { icon } from '../ui/icons.js';
import { segmented } from '../ui/parts.js';
import { openSheet, confirmSheet } from '../ui/sheet.js';
import { toast, toastError } from '../ui/toast.js';
import { api } from '../core/api.js';
import { toBytes, creationOptions, credentialJSON, passkeysPossible } from '../core/b64.js';
import { formatDay, timestampDay } from '../core/format.js';
import { app, savePrefs } from './app-state.js';
import { editProfile, levelLabel } from './people.js';

const st = { push: null, sub: null, passkeys: null, imports: null, busy: false };

export async function loadReach() {
  const [push, passkeys, imports] = await Promise.allSettled([api.get('/api/v1/push'), api.get('/api/v1/passkeys'), api.get('/api/v1/import')]);
  st.push = push.status === 'fulfilled' ? push.value : null;
  st.passkeys = passkeys.status === 'fulfilled' ? passkeys.value : null;
  st.imports = imports.status === 'fulfilled' ? imports.value : null;
  try {
    const reg = await navigator.serviceWorker?.getRegistration();
    st.sub = await reg?.pushManager?.getSubscription() ?? null;
  } catch { st.sub = null; }
  app.redraw();
}

const save = patch => savePrefs(patch).then(() => toast('Saved'), err => toastError(err));

const isIOS = () => /iPhone|iPad|iPod/.test(navigator.userAgent);
const standalone = () => matchMedia('(display-mode: standalone)').matches || navigator.standalone === true;

async function enablePush() {
  st.busy = true; app.redraw();
  try {
    const perm = await Notification.requestPermission();
    if (perm !== 'granted') {
      toast(perm === 'denied' ? 'Notifications are blocked for Cato in this browser’s settings.' : 'Notifications weren’t allowed.', { kind: 'error' });
      return;
    }
    const reg = await navigator.serviceWorker.ready;
    const sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: toBytes(st.push.public_key) });
    const json = sub.toJSON();
    st.push = await api.post('/api/v1/push/subscriptions', { endpoint: json.endpoint, keys: json.keys });
    st.sub = sub;
    toast('Notifications are on for this device');
  } catch (err) {
    toastError(err, 'Couldn’t turn on notifications');
  } finally {
    st.busy = false; app.redraw();
  }
}

async function disablePush() {
  try {
    const endpoint = st.sub?.endpoint;
    await st.sub?.unsubscribe();
    if (endpoint) st.push = await api.del('/api/v1/push/subscriptions', { endpoint });
    st.sub = null;
    toast('Notifications are off for this device');
  } catch (err) { toastError(err); }
  app.redraw();
}

const KINDS = [
  ['release', 'Wishlist games out today'],
  ['release_soon', 'Wishlist games out next week'],
  ['price', 'Price drops on your wishlist'],
  ['follow', 'New followers'],
  ['follow_request', 'Follow requests'],
  ['follow_accepted', 'Accepted follow requests'],
  ['like', 'Likes on your reviews'],
  ['invite_used', 'Someone joined with your invite'],
];

function deviceSection() {
  if (!st.push) return html`<p class="muted">Unavailable offline.</p>`;
  if (!('PushManager' in window) || !('serviceWorker' in navigator)) {
    if (isIOS() && !standalone()) {
      return html`<p class="muted">${icon('info')} On iPhone and iPad, notifications work once Cato is on your Home Screen:
        tap ${icon('share-2')} Share, then “Add to Home Screen”, and open Cato from there.</p>`;
    }
    return html`<p class="muted">This browser can’t receive notifications. Everything still arrives in your Inbox.</p>`;
  }
  if (Notification.permission === 'denied') return html`<p class="muted">Notifications are blocked for Cato in this browser’s settings.</p>`;
  return st.sub
    ? html`<div class="row row-wrap"><span class="grow">${icon('bell')} Notifications are on for this device.</span>
        <button type="button" class="btn btn-sm btn-secondary" @click=${async () => { try { await api.post('/api/v1/push/test'); toast('Sent — it should arrive in a few seconds'); } catch (err) { toastError(err); } }}>Send a test</button>
        <button type="button" class="btn btn-sm btn-ghost" @click=${disablePush}>Turn off</button></div>`
    : html`<div class="row row-wrap"><span class="grow muted">Get notified on this device, even when Cato is closed.</span>
        <button type="button" class="btn btn-primary btn-sm" ?disabled=${st.busy} @click=${enablePush}>${icon('bell')}Turn on notifications</button></div>`;
}

function notificationsSection() {
  const p = app.prefs;
  const notify = p.notify || {};
  const setKind = (k, on) => save({ notify: { [k]: { ...(notify[k] || {}), push: on } } });
  const pa = p.price_alerts || { min_cut: 50, country: 'US' };
  return html`<section id="set-notifications" aria-labelledby="h-notifications"><h2 id="h-notifications" class="section-head">Notifications</h2>
    <div class="settings-list">
      ${deviceSection()}
      <label class="switch"><span>Tell me when a wishlist game comes out on my platforms</span>
        <input type="checkbox" .checked=${p.release_alerts !== false} @change=${e => save({ release_alerts: e.target.checked })}></label>
      <label class="switch"><span>…and a week before</span>
        <input type="checkbox" .checked=${!!p.release_week} @change=${e => save({ release_week: e.target.checked })}></label>
      ${st.push?.price_alerts ? html`
        <label class="switch"><span>Price alerts for wishlist games (from IsThereAnyDeal)</span>
          <input type="checkbox" .checked=${!!pa.enabled} @change=${e => save({ price_alerts: { ...pa, enabled: e.target.checked } })}></label>
        ${pa.enabled ? html`<div class="field"><span class="label">Alert me at</span>${segmented({ label: 'Price alert threshold', value: pa.low_only ? 'low' : String(pa.min_cut),
          onChange: v => save({ price_alerts: v === 'low' ? { ...pa, low_only: true } : { ...pa, low_only: false, min_cut: Number(v) } }),
          options: [{ value: '25', label: '25% off' }, { value: '50', label: '50% off' }, { value: '75', label: '75% off' }, { value: 'low', label: 'Historical low' }] })}</div>
          <label class="field"><span>Store region (two-letter country code)</span>
            <input class="input" maxlength="2" style="max-width:6em;text-transform:uppercase" .value=${pa.country || 'US'}
              @change=${e => save({ price_alerts: { ...pa, country: e.target.value } })}></label>` : nothing}`
        : html`<p class="muted">Price alerts need an IsThereAnyDeal API key on the server (ITAD_API_KEY).</p>`}
      ${st.push?.email ? html`<label class="switch"><span>Email me a weekly summary of what I missed</span>
        <input type="checkbox" .checked=${!!p.email_digest} @change=${e => save({ email_digest: e.target.checked })}></label>` : nothing}
      <details><summary>Which notifications to push</summary>
        <p class="muted">Your Inbox always has everything. Social ones are off until you turn them on.</p>
        ${KINDS.map(([k, label]) => html`<label class="switch"><span>${label}</span>
          <input type="checkbox" .checked=${!!notify[k]?.push} @change=${e => setKind(k, e.target.checked)}></label>`)}
      </details>
    </div></section>`;
}

async function addPasskey() {
  let name = /iPhone|iPad/.test(navigator.userAgent) ? 'iPhone or iPad' : /Android/.test(navigator.userAgent) ? 'Android' : /Mac/.test(navigator.userAgent) ? 'Mac' : 'This device';
  const ok = await openSheet({
    title: 'Add a passkey',
    content: () => html`<p class="muted">Sign in with your fingerprint, face or device PIN instead of a password. Your device keeps the key; Cato only stores its public half.</p>
      <label class="field"><span>Name</span><input class="input" maxlength="60" .value=${name} @input=${e => { name = e.target.value; }}></label>`,
    footer: s => html`<span class="spacer"></span><button type="button" class="btn btn-primary" @click=${() => s.close(true)}>${icon('key-round')}Continue</button>`,
  }).closed;
  if (!ok) return;
  try {
    const { publicKey } = await api.post('/api/v1/passkeys/begin');
    const cred = await navigator.credentials.create({ publicKey: creationOptions(publicKey) });
    st.passkeys = await api.post('/api/v1/passkeys', { ...credentialJSON(cred), name });
    toast('Passkey added');
  } catch (err) {
    if (err?.name === 'NotAllowedError') toast('Cancelled');
    else if (err?.name === 'InvalidStateError') toast('This device already has a passkey for Cato');
    else toastError(err, 'Couldn’t add the passkey');
  }
  app.redraw();
}

function passkeysSection() {
  const pk = st.passkeys;
  return html`<section id="set-passkeys" aria-labelledby="h-passkeys"><h2 id="h-passkeys" class="section-head">Passkeys</h2>
    <div class="settings-list">
      ${!pk ? html`<p class="muted">Unavailable offline.</p>`
        : !pk.available || !passkeysPossible() ? html`<p class="muted">${pk.available ? 'This browser doesn’t support passkeys.' : pk.reason}</p>`
        : html`${pk.items.map(k => html`<div class="row">${icon('key-round')}<div class="grow"><div class="title">${k.name}</div>
            <div class="sub muted">Added ${formatDay(timestampDay(k.created_at))}${k.last_used_at ? ` · last used ${formatDay(timestampDay(k.last_used_at))}` : ''}</div></div>
            <button type="button" class="btn btn-sm btn-ghost" aria-label=${`Remove ${k.name}`} @click=${async () => {
              if (!await confirmSheet({ title: `Remove “${k.name}”?`, message: 'You won’t be able to sign in with it any more. Remove it from the device’s password manager too.', confirm: 'Remove', danger: true })) return;
              try { st.passkeys = await api.del(`/api/v1/passkeys/${encodeURIComponent(k.id)}`); app.redraw(); } catch (err) { toastError(err); }
            }}>${icon('trash-2')}</button></div>`)}
          <div><button type="button" class="btn btn-secondary" @click=${addPasskey}>${icon('plus')}Add a passkey</button></div>`}
    </div></section>`;
}

function accountsSection() {
  const im = st.imports;
  const steam = im?.steam;
  return html`<section id="set-accounts" aria-labelledby="h-accounts"><h2 id="h-accounts" class="section-head">Profile and connected accounts</h2>
    <div class="settings-list">
      <button type="button" class="list-row" @click=${editProfile}>${icon('user')}<div class="grow"><div class="title">Profile and privacy</div>
        <div class="sub">@${app.me?.username || '…'} · ${levelLabel(app.me?.visibility || 'private')} can see your games</div></div>${icon('chevron-right')}</button>
      ${steam ? html`<div class="row row-wrap">${icon('gamepad-2')}<div class="grow"><div class="title">Steam: ${steam.persona || steam.steam_id}</div>
          <div class="sub muted">${steam.sync ? `Play time syncs daily${steam.last_sync_at ? `, last ${formatDay(timestampDay(steam.last_sync_at))}` : ''}` : 'Play time sync is off'}${steam.last_error ? ` · ${steam.last_error}` : ''}</div></div>
        <label class="switch" style="border:0;padding:0"><span class="visually-hidden">Sync Steam play time daily</span>
          <input type="checkbox" .checked=${steam.sync} @change=${async e => { try { st.imports = await api.patch('/api/v1/import/steam', { sync: e.target.checked }); app.redraw(); } catch (err) { toastError(err); } }}></label>
        <button type="button" class="btn btn-sm btn-ghost" @click=${async () => {
          if (!await confirmSheet({ title: 'Disconnect Steam?', message: 'Games you imported stay in your library; daily play time sync stops.', confirm: 'Disconnect' })) return;
          try { st.imports = await api.del('/api/v1/import/steam'); app.redraw(); } catch (err) { toastError(err); }
        }}>Disconnect</button></div>`
        : html`<a class="list-row" href="#you/import?source=steam">${icon('gamepad-2')}<div class="grow"><div class="title">Connect Steam</div>
          <div class="sub">${im?.steam_configured ? 'Import your games and keep play time in sync' : 'Needs a Steam Web API key on the server'}</div></div>${icon('chevron-right')}</a>`}
    </div></section>`;
}

export function reachSettings() {
  return html`${accountsSection()}${notificationsSection()}${passkeysSection()}`;
}
