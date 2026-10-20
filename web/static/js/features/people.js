// people.js — the people features (plan §7): profiles (#u/<name>), lists
// (#you/lists, #list/<id>), people and invites (#you/people), the inbox
// (#you/inbox), editing your profile, and the social part of game pages.
// Everything here is limited to this server and private by default; the
// server decides who sees what, the views only render what comes back.
import { html, nothing, repeat } from '../ui/lit.js';
import { icon } from '../ui/icons.js';
import { cover, empty, skeletonRows, segmented, chip } from '../ui/parts.js';
import { decodeCovers } from '../ui/decode.js';
import { stars } from '../ui/stars.js';
import { openSheet, confirmSheet } from '../ui/sheet.js';
import { openMenu } from '../ui/menu.js';
import { toast, toastError } from '../ui/toast.js';
import { api } from '../core/api.js';
import { go } from '../core/router.js';
import {
  formatMinutes, formatHoursLong, plural, initials, statusLabel, releaseYear, timestampDay, relativeDay,
} from '../core/format.js';
import { app, ratingDisplay } from './app-state.js';
import { copyText } from '../ui/clipboard.js';

// ---- shared pieces ------------------------------------------------------------

export const personName = p => p?.display_name || (p?.username ? `@${p.username}` : 'Someone');

export function avatar(p, cls = '') {
  return html`<span class=${`avatar ${cls}`} aria-hidden="true">${p?.avatar
    ? html`<img src=${p.avatar} alt="" loading="lazy" referrerpolicy="no-referrer">`
    : initials(p?.display_name || p?.username || '?')}</span>`;
}

export function personLink(p) {
  return p?.username ? html`<a href=${`#u/${encodeURIComponent(p.username)}`}>${personName(p)}</a>` : personName(p);
}

export const LEVELS = [
  { value: 'private', label: 'Only me', icon: 'lock', hint: 'Nobody else sees your profile, ratings or activity.' },
  { value: 'followers', label: 'Followers', icon: 'users', hint: 'People whose follow request you approve.' },
  { value: 'instance', label: 'Everyone here', icon: 'globe', hint: 'Everyone with an account on this server. Follows are approved automatically.' },
];
const ITEM_LEVELS = [{ value: 'inherit', label: 'Like my profile', icon: 'user' }, ...LEVELS];
export const levelLabel = v => (ITEM_LEVELS.find(l => l.value === v) || LEVELS[0]).label;

const ago = ts => (ts ? relativeDay(timestampDay(ts)) : '');

function report(kind, target) {
  let reason = '';
  openSheet({
    title: 'Report to the admins',
    content: () => html`<p class="muted">Admins on this server will see your report and can hide what you reported.</p>
      <label class="field"><span>What's wrong? (optional)</span>
        <textarea class="textarea" maxlength="500" rows="3" @input=${e => { reason = e.target.value; }}></textarea></label>`,
    footer: s => html`<button type="button" class="btn btn-secondary" @click=${() => s.close()}>Cancel</button><span class="spacer"></span>
      <button type="button" class="btn btn-danger" @click=${async () => {
        try {
          await api.post('/api/v1/reports', { kind, target: String(target), reason });
          toast('Reported. Thanks for letting us know.');
          s.close();
        } catch (err) { toastError(err); }
      }}>${icon('flag')}Report</button>`,
  });
}

// ---- profile ----------------------------------------------------------------

const prof = { name: '', data: null, error: null };

export async function enterProfile(route) {
  if (prof.name !== route.id) Object.assign(prof, { name: route.id, data: null, error: null });
  try {
    prof.data = await api.get(`/api/v1/users/${encodeURIComponent(route.id)}`);
    prof.error = null;
  } catch (err) {
    prof.error = err;
  }
  app.redraw();
  // Drawn with its first covers (the shell holds the previous screen).
  const p = prof.data;
  if (p) await decodeCovers([...(p.favourites || []).slice(0, 6), ...(p.playing || []).slice(0, 4)].map(g => g.cover));
}

async function relate(method, path, done) {
  try {
    const res = await api[method](path);
    if (done) toast(done(res));
    await enterProfile({ id: prof.name });
  } catch (err) { toastError(err); }
}

function followButton(p) {
  const r = p.relationship;
  const u = encodeURIComponent(p.person.username);
  if (r.self) return html`<button type="button" class="btn btn-secondary btn-sm" @click=${editProfile}>${icon('pencil')}Edit profile</button>`;
  if (r.blocked) return html`<button type="button" class="btn btn-secondary btn-sm" @click=${() => relate('del', `/api/v1/users/${u}/block`, () => 'Unblocked')}>Unblock</button>`;
  if (r.following === 'accepted') return html`<button type="button" class="btn btn-secondary btn-sm" @click=${() => relate('del', `/api/v1/users/${u}/follow`, () => 'Unfollowed')}>${icon('check')}Following</button>`;
  if (r.following === 'pending') return html`<button type="button" class="btn btn-secondary btn-sm" @click=${() => relate('del', `/api/v1/users/${u}/follow`, () => 'Request withdrawn')}>Requested</button>`;
  return html`<button type="button" class="btn btn-primary btn-sm" @click=${() => relate('post', `/api/v1/users/${u}/follow`,
    res => (res.following === 'accepted' ? `You're following ${personName(p.person)}` : 'Follow request sent'))}>${icon('user-plus')}${r.follows_you === 'accepted' ? 'Follow back' : 'Follow'}</button>`;
}

function profileMenu(e, p) {
  const u = encodeURIComponent(p.person.username);
  openMenu({
    anchor: e.currentTarget,
    label: 'More',
    items: [
      p.relationship.follows_you ? { label: 'Remove as follower', icon: 'x', run: () => relate('del', `/api/v1/followers/${u}`, () => 'Removed') } : null,
      p.relationship.blocked
        ? { label: 'Unblock', icon: 'shield', run: () => relate('del', `/api/v1/users/${u}/block`, () => 'Unblocked') }
        : { label: 'Block', icon: 'shield', danger: true, run: async () => {
          if (await confirmSheet({ title: `Block ${personName(p.person)}?`, message: 'You won’t see each other’s profiles, reviews, lists or activity, and any follows between you end.', confirm: 'Block', danger: true })) {
            relate('post', `/api/v1/users/${u}/block`, () => 'Blocked');
          }
        } },
      { label: 'Report', icon: 'flag', run: () => report('user', p.person.username) },
    ].filter(Boolean),
  });
}

function gameStrip(title, games, { sub = g => releaseYear(g.release_date) || '' } = {}) {
  if (!games?.length) return nothing;
  return html`<section class="section"><div class="section-head"><h2>${title}</h2></div>
    <div class="shelf-row">${games.map(g => html`<a class="upnext-card" href=${`#game/${g.id}`}>${cover(g)}<span class="title">${g.name}</span>
      <span class="reason">${sub(g)}</span></a>`)}</div></section>`;
}

export function profileView() {
  if (prof.error) {
    return html`<div class="page">${empty({ iconName: 'user', title: prof.error.status === 404 ? 'No one here by that name' : 'Couldn’t load this profile', body: prof.error.status === 404 ? 'They may have changed their username, or you can’t see each other.' : prof.error.message })}</div>`;
  }
  const p = prof.data;
  if (!p) return html`<div class="page">${skeletonRows(4)}</div>`;
  const r = p.relationship;
  const visible = r.can_see && !r.blocked;
  return html`<div class="page">
    <header class="profile-head">
      ${avatar(p.person, 'avatar-xl')}
      <div class="grow">
        <h1>${personName(p.person)}</h1>
        <p class="muted">@${p.person.username}${r.follows_you === 'accepted' && !r.self ? ' · follows you' : ''}</p>
        <p class="muted num"><a href=${r.self ? '#you/people' : nothing}>${plural(p.followers, 'follower')}</a> · ${p.following} following</p>
        ${visible && p.bio ? html`<p class="bio">${p.bio}</p>` : nothing}
      </div>
      <div class="row">${followButton(p)}
        ${r.self ? nothing : html`<button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label="More" @click=${e => profileMenu(e, p)}>${icon('ellipsis')}</button>`}</div>
    </header>
    ${!visible ? html`<div class="section">${empty({ iconName: 'lock', title: r.blocked ? 'You blocked this person' : 'This profile is private',
      body: r.blocked ? 'Unblock them to see their profile.' : r.visibility === 'followers' ? 'Follow them; once they approve, you’ll see their games, reviews and lists.' : 'They share their games with no one else for now.' })}</div>`
    : html`
      ${p.stats ? html`<div class="stat-tiles section">
        <div class="stat-tile"><div class="value num">${p.stats.games}</div><div class="label">Games</div></div>
        <div class="stat-tile"><div class="value num">${p.stats.finished}</div><div class="label">Finished</div></div>
        <div class="stat-tile"><div class="value num">${formatHoursLong(p.stats.minutes)}</div><div class="label">Played</div></div>
        <div class="stat-tile"><div class="value num">${p.stats.finished_this_year}</div><div class="label">Finished this year</div></div>
      </div>` : nothing}
      ${p.favourites.length ? html`<section class="section"><div class="section-head"><h2>Favourites</h2></div>
        <div class="fav-grid">${p.favourites.map(g => html`<a href=${`#game/${g.id}`} title=${g.name}>${cover(g)}</a>`)}</div></section>` : nothing}
      ${gameStrip('Playing', p.playing, { sub: g => (g.minutes ? `${formatMinutes(g.minutes)} played` : '') })}
      ${gameStrip('Recently finished', p.recent_finishes, { sub: g => (g.rating ? '★'.repeat(Math.round(g.rating / 20)) : releaseYear(g.release_date) || '') })}
      ${p.lists.length ? html`<section class="section"><div class="section-head"><h2>Lists</h2></div>${listCards(p.lists)}</section>` : nothing}
      ${p.reviews.length ? html`<section class="section"><div class="section-head"><h2>Reviews</h2></div>
        <div class="review-list">${p.reviews.map(rv => reviewCard(rv, { showGame: true }))}</div></section>` : nothing}
      ${!p.favourites.length && !p.playing.length && !p.recent_finishes.length && !p.lists.length && !p.reviews.length
        ? empty({ iconName: 'gamepad-2', title: 'Nothing to show yet', body: r.self ? 'Pick favourites, write a review or make a list.' : '' }) : nothing}
    `}
  </div>`;
}

// ---- editing your profile ----------------------------------------------------

export async function editProfile() {
  let me;
  try { me = (await api.get('/api/v1/me/profile')).profile; } catch (err) { toastError(err); return; }
  const form = { username: me.username, display_name: me.display_name, bio: me.bio, visibility: me.visibility, use_gravatar: me.use_gravatar };
  let favs = me.favourites.map(g => ({ id: g.id, name: g.name, cover: g.cover }));
  let filter = '';
  let errors = {};
  let busy = false;
  const save = async s => {
    busy = true; errors = {}; s.update();
    try {
      const res = await api.patch('/api/v1/me/profile', { ...form, favourites: favs.map(g => g.id) });
      Object.assign(app.me, { username: res.profile.username, display_name: res.profile.display_name, avatar: res.profile.avatar, visibility: res.profile.visibility });
      app.redraw();
      toast('Profile saved');
      s.close(true);
      if (location.hash.startsWith('#u/')) go(`#u/${encodeURIComponent(res.profile.username)}`, { replace: true });
    } catch (err) {
      errors = err.fields || {};
      if (!err.fields) toastError(err);
      busy = false; s.update();
    }
  };
  const upload = async (e, s) => {
    const file = e.target.files?.[0];
    if (!file) return;
    if (file.size > 8 * 1024 * 1024) { toast('Choose an image under 8 MB', { kind: 'error' }); return; }
    const data = await new Promise((res, rej) => { const r = new FileReader(); r.onload = () => res(r.result); r.onerror = rej; r.readAsDataURL(file); });
    try {
      const res = await api.put('/api/v1/me/avatar', { avatar: data });
      me = res.profile; app.me.avatar = me.avatar; app.redraw(); s.update();
    } catch (err) { toastError(err); }
  };
  const removeAvatar = async s => {
    try { me = (await api.del('/api/v1/me/avatar')).profile; app.me.avatar = me.avatar; app.redraw(); s.update(); } catch (err) { toastError(err); }
  };
  const matches = () => {
    const q = filter.trim().toLowerCase();
    if (!q) return [];
    return app.store.entries().filter(e => e.name.toLowerCase().includes(q) && !favs.some(f => f.id === e.id)).slice(0, 6);
  };
  openSheet({
    title: 'Edit profile',
    content: s => html`
      <div class="row" style="gap:var(--sp-4);align-items:center">
        ${avatar({ ...me, avatar: me.avatar }, 'avatar-lg')}
        <div class="row row-wrap">
          <label class="btn btn-secondary btn-sm">${icon('upload')}Upload photo<input type="file" accept="image/jpeg,image/png,image/gif" hidden @change=${e => upload(e, s)}></label>
          ${me.has_avatar_upload ? html`<button type="button" class="btn btn-ghost btn-sm" @click=${() => removeAvatar(s)}>Remove photo</button>` : nothing}
        </div>
      </div>
      <label class="row" style="margin-top:var(--sp-3)"><input type="checkbox" .checked=${form.use_gravatar} @change=${e => { form.use_gravatar = e.target.checked; }}>
        <span>Use my Gravatar when I haven't uploaded a photo <span class="muted">(loads an image from gravatar.com)</span></span></label>
      <div class="form-grid" style="margin-top:var(--sp-4)">
        <label class="field"><span>Name</span><input class="input" maxlength="64" .value=${form.display_name} @input=${e => { form.display_name = e.target.value; }}></label>
        <label class="field"><span>Username</span><input class="input" maxlength="32" autocapitalize="none" spellcheck="false" .value=${form.username}
          aria-invalid=${errors.username ? 'true' : 'false'} @input=${e => { form.username = e.target.value; }}>
          ${errors.username ? html`<span class="field-error">${errors.username}</span>` : html`<span class="muted">Your profile is at #u/${form.username || '…'}</span>`}</label>
        <label class="field"><span>Bio</span><textarea class="textarea" rows="3" maxlength="500" .value=${form.bio} @input=${e => { form.bio = e.target.value; s.update(); }}></textarea>
          <span class="muted num">${500 - [...(form.bio || '')].length} left</span></label>
      </div>
      <h3 class="sheet-sub">Who can see your games</h3>
      ${segmented({ label: 'Profile visibility', value: form.visibility, options: LEVELS, onChange: v => { form.visibility = v; s.update(); } })}
      <p class="muted" style="margin-top:var(--sp-2)">${LEVELS.find(l => l.value === form.visibility)?.hint}</p>
      <h3 class="sheet-sub">Favourites <span class="muted">(up to four)</span></h3>
      <div class="fav-grid fav-edit">${favs.map(g => html`<div class="fav-slot">${cover(g)}
        <button type="button" class="btn btn-icon btn-sm btn-secondary" aria-label=${`Remove ${g.name}`} @click=${() => { favs = favs.filter(f => f.id !== g.id); s.update(); }}>${icon('x')}</button></div>`)}</div>
      ${favs.length < 4 ? html`<label class="field" style="margin-top:var(--sp-2)"><span>Add from your library</span>
        <input class="input" type="search" placeholder="Search your games" .value=${filter} @input=${e => { filter = e.target.value; s.update(); }}></label>
        <div class="list">${matches().map(e => html`<button type="button" class="list-row" @click=${() => { favs = [...favs, { id: e.id, name: e.name, cover: e.cover }]; filter = ''; s.update(); }}>
          ${cover(e, { cls: 'thumb' })}<div class="grow"><div class="title">${e.name}</div></div>${icon('plus')}</button>`)}</div>` : nothing}
      ${errors.favourites ? html`<p class="field-error">${errors.favourites}</p>` : nothing}`,
    footer: s => html`<button type="button" class="btn btn-secondary" @click=${() => s.close()}>Cancel</button><span class="spacer"></span>
      <button type="button" class="btn btn-primary" ?disabled=${busy} @click=${() => save(s)}>Save</button>`,
  });
}

// ---- reviews ----------------------------------------------------------------

// Spoilers someone chose to reveal (this visit only).
const revealed = new Set();

function reviewCard(rv, { showGame = false, onChange = null } = {}) {
  const body = () => (rv.spoiler && !revealed.has(rv.id) && !rv.mine
    ? html`<button type="button" class="spoiler" @click=${() => { revealed.add(rv.id); app.redraw(); }}>
        ${icon('eye-off')}Contains spoilers — show</button>`
    : html`<p class="review-body">${rv.body}</p>`);
  const like = async () => {
    try {
      const res = rv.liked_by_me ? await api.del(`/api/v1/reviews/${rv.id}/like`) : await api.post(`/api/v1/reviews/${rv.id}/like`);
      Object.assign(rv, res.review);
      app.redraw();
    } catch (err) { toastError(err); }
  };
  const menu = e => openMenu({
    anchor: e.currentTarget,
    label: 'Review',
    items: rv.mine
      ? [{ label: 'Edit', icon: 'pencil', run: () => writeReview({ id: rv.game.id, name: rv.game.name }, rv, onChange) },
        { label: 'Delete', icon: 'trash-2', danger: true, run: async () => {
          if (!await confirmSheet({ title: 'Delete this review?', message: 'This can’t be undone.', confirm: 'Delete', danger: true })) return;
          try { await api.del(`/api/v1/reviews/${rv.id}`); toast('Review deleted'); onChange?.(); } catch (err) { toastError(err); }
        } }]
      : [{ label: 'Report', icon: 'flag', run: () => report('review', rv.id) }],
  });
  return html`<article class="review card" id=${`review-${rv.id}`}>
    <header class="row">
      ${showGame ? html`<a href=${`#game/${rv.game.id}`}>${cover(rv.game, { cls: 'thumb' })}</a>` : avatar(rv.author)}
      <div class="grow">
        <div class="title">${showGame ? html`<a href=${`#game/${rv.game.id}`}>${rv.game.name}</a>` : personLink(rv.author)}
          ${rv.friend && !showGame ? html`<span class="status status-playing">Friend</span>` : nothing}${rv.hidden ? html`<span class="status status-abandoned">Hidden by an admin</span>` : nothing}</div>
        <div class="sub">${rv.rating ? stars(rv.rating, { display: ratingDisplay() }) : nothing} ${ago(rv.created_at)}${rv.mine && rv.visibility !== 'inherit' ? ` · ${levelLabel(rv.visibility)}` : ''}</div>
      </div>
      <button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label="Review actions" @click=${menu}>${icon('ellipsis')}</button>
    </header>
    ${body()}
    <footer class="row">
      <button type="button" class="chip" aria-pressed=${String(!!rv.liked_by_me)} ?disabled=${rv.mine} @click=${like}
        aria-label=${`${rv.liked_by_me ? 'Unlike' : 'Like'} (${rv.likes})`}>${icon('heart')}<span class="num">${rv.likes}</span></button>
    </footer>
  </article>`;
}

// writeReview opens the composer for a new review or an edit.
export function writeReview(game, existing = null, onSaved = null) {
  const form = { body: existing?.body || '', spoiler: !!existing?.spoiler, visibility: existing?.visibility || 'inherit' };
  let busy = false;
  let error = '';
  const item = app.store.get(game.id);
  openSheet({
    title: existing ? 'Edit review' : `Review ${game.name}`,
    content: s => html`
      <label class="field"><span>Your review</span>
        <textarea class="textarea" rows="7" maxlength="10000" autofocus placeholder="What did you think?" .value=${form.body}
          @input=${e => { form.body = e.target.value; }}></textarea></label>
      ${error ? html`<p class="field-error">${error}</p>` : nothing}
      <label class="row" style="margin-top:var(--sp-2)"><input type="checkbox" .checked=${form.spoiler} @change=${e => { form.spoiler = e.target.checked; }}>
        <span>Contains spoilers (hidden until someone taps it)</span></label>
      <h3 class="sheet-sub">Who can see it</h3>
      ${segmented({ label: 'Review visibility', value: form.visibility, options: ITEM_LEVELS, onChange: v => { form.visibility = v; s.update(); } })}
      ${!existing && item?.rating ? html`<p class="muted" style="margin-top:var(--sp-2)">Shown with your rating: ${stars(item.rating, { display: ratingDisplay() })}</p>` : nothing}`,
    footer: s => html`<button type="button" class="btn btn-secondary" @click=${() => s.close()}>Cancel</button><span class="spacer"></span>
      <button type="button" class="btn btn-primary" ?disabled=${busy} @click=${async () => {
        busy = true; error = ''; s.update();
        try {
          if (existing) await api.patch(`/api/v1/reviews/${existing.id}`, form);
          else await api.post('/api/v1/reviews', { ...form, game_id: game.id, playthrough_id: item?.current_playthrough_id || 0 });
          toast(existing ? 'Review updated' : 'Review posted');
          s.close(true);
          onSaved?.();
          if (gs.id === game.id) loadGameSocial(game.id);
        } catch (err) {
          error = err.fields?.body || err.message;
          busy = false; s.update();
        }
      }}>${existing ? 'Save' : 'Post review'}</button>`,
  });
}

// ---- game page --------------------------------------------------------------

const gs = { id: null, data: null, loading: false };

export async function loadGameSocial(id) {
  if (gs.id !== id) Object.assign(gs, { id, data: null });
  gs.loading = true;
  try {
    const data = await api.get(`/api/v1/games/${id}/social`);
    if (gs.id === id) gs.data = data;
  } catch { /* the section just stays empty */ }
  gs.loading = false;
  app.redraw();
}

function histogram(c) {
  const max = Math.max(1, ...c.histogram);
  const labels = ['½', '1', '1½', '2', '2½', '3', '3½', '4', '4½', '5'];
  return html`<div class="bars bars-mini" role="img" aria-label=${`Ratings on this server: ${c.ratings} ratings, average ${(c.average / 20).toFixed(1)} stars`}>
    ${c.histogram.map((n, i) => html`<div class="bar-col" title=${`${labels[i]} stars: ${n}`}><div class="bar-fill" style=${`height:${Math.round((n / max) * 100)}%`}></div><span class="bar-label">${i % 2 ? labels[i] : ''}</span></div>`)}
  </div>`;
}

export function gameSocialSection(game) {
  const d = gs.id === game.id ? gs.data : null;
  if (!d) return nothing;
  const c = d.community;
  const mine = d.reviews.find(r => r.mine);
  return html`
    ${d.friends.length ? html`<section class="section" aria-labelledby="friends-title">
      <div class="section-head"><h2 id="friends-title">${plural(d.friends.length, 'friend')} ${d.friends.length === 1 ? 'has' : 'have'} this</h2></div>
      <div class="friend-row">${d.friends.map(f => html`<a class="friend" href=${f.review_id ? `#game/${game.id}` : `#u/${encodeURIComponent(f.username)}`}
        @click=${f.review_id ? e => { e.preventDefault(); document.getElementById(`review-${f.review_id}`)?.scrollIntoView({ behavior: 'smooth', block: 'center' }); } : null}
        title=${f.review_id ? `Read ${personName(f)}’s review` : personName(f)}>
        ${avatar(f)}<span class="name">${personName(f)}</span>
        <span class="sub">${statusLabel(f.status)}</span>
        ${f.rating ? html`<span class="sub">${stars(f.rating, { display: ratingDisplay() })}</span>` : nothing}
        ${f.review_id ? html`<span class="sub">${icon('message-square')}Ask a friend</span>` : nothing}</a>`)}</div>
    </section>` : nothing}
    ${c.ratings >= 2 ? html`<section class="section" aria-labelledby="community-title">
      <div class="section-head"><h2 id="community-title">On this server</h2><span class="muted">${plural(c.players, 'player')}</span></div>
      <div class="community">
        <div><div class="value num">${(c.average / 20).toFixed(1)}</div><div class="muted">average from ${plural(c.ratings, 'rating')}</div></div>
        ${histogram(c)}
      </div>
    </section>` : nothing}
    <section class="section" aria-labelledby="reviews-title">
      <div class="section-head"><h2 id="reviews-title">Reviews</h2>
        ${mine ? nothing : html`<button type="button" class="btn btn-sm btn-secondary" @click=${() => writeReview(game)}>${icon('pencil')}Write a review</button>`}</div>
      ${d.reviews.length ? html`<div class="review-list">${repeat(d.reviews, r => r.id, r => reviewCard(r, { onChange: () => loadGameSocial(game.id) }))}</div>`
        : html`<p class="muted">No reviews you can see yet.</p>`}
    </section>`;
}

// addToList shows your lists with a checkbox for this game.
export async function addToList(game) {
  let lists;
  try { lists = (await api.get(`/api/v1/games/${game.id}/lists`)).items; } catch (err) { toastError(err); return; }
  let title = '';
  const toggle = async (l, s) => {
    try {
      if (l.has_game) await api.del(`/api/v1/lists/${l.id}/items/${game.id}`);
      else await api.put(`/api/v1/lists/${l.id}/items/${game.id}`);
      l.has_game = !l.has_game; s.update();
    } catch (err) { toastError(err); }
  };
  const create = async s => {
    if (!title.trim()) return;
    try {
      const l = (await api.post('/api/v1/lists', { title })).list;
      await api.put(`/api/v1/lists/${l.id}/items/${game.id}`);
      lists.unshift({ id: l.id, title: l.title, has_game: true });
      title = ''; s.update();
    } catch (err) { toastError(err); }
  };
  openSheet({
    title: `Add ${game.name} to a list`,
    content: s => html`
      ${lists.length ? html`<ul class="settings-list">${lists.map(l => html`<li><label class="row" style="width:100%">
        <input type="checkbox" .checked=${l.has_game} @change=${() => toggle(l, s)}><span class="grow">${l.title}</span></label></li>`)}</ul>`
        : html`<p class="muted">You have no lists yet.</p>`}
      <form class="row" style="margin-top:var(--sp-3)" @submit=${e => { e.preventDefault(); create(s); }}>
        <input class="input grow" maxlength="100" placeholder="New list" aria-label="New list title" .value=${title} @input=${e => { title = e.target.value; }}>
        <button class="btn btn-secondary" type="submit">${icon('plus')}Create</button></form>`,
  });
}

// ---- lists ------------------------------------------------------------------

const STARTERS = ['All-time favourites', 'Games to play with my partner', 'Comfort games'];

function listCards(lists) {
  return html`<div class="list-cards">${lists.map(l => html`<a class="list-card card" href=${`#list/${l.id}`}>
    <div class="list-covers">${l.preview.length ? l.preview.map(g => cover(g, { cls: 'thumb' })) : html`<span class="muted">Empty</span>`}</div>
    <div class="title">${l.title}${l.hidden ? html` <span class="status status-abandoned">Hidden</span>` : nothing}</div>
    <div class="sub">${plural(l.count, 'game')}${l.ranked ? ' · ranked' : ''}${l.mine ? ` · ${levelLabel(l.visibility)}` : ''}</div>
  </a>`)}</div>`;
}

const mylists = { data: null, error: null };

export async function enterMyLists() {
  try { mylists.data = (await api.get('/api/v1/lists')).items; mylists.error = null; } catch (err) { mylists.error = err; }
  app.redraw();
  if (mylists.data) await decodeCovers(mylists.data.slice(0, 5).flatMap(l => l.preview.map(g => g.cover)), true);
}

async function createList(title = '') {
  const form = { title, description: '', ranked: false, visibility: 'inherit' };
  openSheet({
    title: 'New list',
    content: s => html`<div class="form-grid">
      <label class="field"><span>Title</span><input class="input" maxlength="100" autofocus .value=${form.title} @input=${e => { form.title = e.target.value; }}></label>
      <label class="field"><span>Description (optional)</span><textarea class="textarea" rows="3" maxlength="2000" @input=${e => { form.description = e.target.value; }}></textarea></label>
      <label class="row"><input type="checkbox" @change=${e => { form.ranked = e.target.checked; }}><span>Ranked (numbered, in your order)</span></label>
      <div><h3 class="sheet-sub">Who can see it</h3>${segmented({ label: 'List visibility', value: form.visibility, options: ITEM_LEVELS, onChange: v => { form.visibility = v; s.update(); } })}</div>
    </div>`,
    footer: s => html`<button type="button" class="btn btn-secondary" @click=${() => s.close()}>Cancel</button><span class="spacer"></span>
      <button type="button" class="btn btn-primary" @click=${async () => {
        try { const l = (await api.post('/api/v1/lists', form)).list; s.close(); go(`#list/${l.id}`); } catch (err) { toastError(err); }
      }}>Create</button>`,
  });
}

export function myListsView() {
  const ls = mylists.data;
  return html`<div class="page">
    <header class="page-head"><h1>Lists</h1><button type="button" class="btn btn-primary btn-sm" @click=${() => createList()}>${icon('plus')}New list</button></header>
    ${mylists.error ? empty({ iconName: 'triangle-alert', title: 'Couldn’t load your lists', body: mylists.error.message })
      : !ls ? skeletonRows(3)
      : ls.length ? listCards(ls)
      : empty({ iconName: 'list', title: 'Make your first list', body: 'Ranked or not, with notes on each game. Share them with followers or everyone on this server.',
        action: html`<div class="chips" style="justify-content:center">${STARTERS.map(t => chip(t, { iconName: 'plus', onClick: () => createList(t) }))}</div>` })}
  </div>`;
}

const lst = { id: null, data: null, error: null, editing: false };

export async function enterList(route) {
  if (lst.id !== route.id) Object.assign(lst, { id: route.id, data: null, error: null, editing: false });
  try { lst.data = (await api.get(`/api/v1/lists/${route.id}`)).list; lst.error = null; } catch (err) { lst.error = err; }
  app.redraw();
  if (lst.data) await decodeCovers(lst.data.items.slice(0, 10).map(it => it.game.cover), true);
}

async function listCall(method, path, body) {
  try { lst.data = (await api[method](path, body)).list; app.redraw(); } catch (err) { toastError(err); }
}

function editListSheet(l) {
  const form = { title: l.title, description: l.description, ranked: l.ranked, visibility: l.visibility };
  openSheet({
    title: 'Edit list',
    content: s => html`<div class="form-grid">
      <label class="field"><span>Title</span><input class="input" maxlength="100" .value=${form.title} @input=${e => { form.title = e.target.value; }}></label>
      <label class="field"><span>Description</span><textarea class="textarea" rows="3" maxlength="2000" .value=${form.description} @input=${e => { form.description = e.target.value; }}></textarea></label>
      <label class="row"><input type="checkbox" .checked=${form.ranked} @change=${e => { form.ranked = e.target.checked; }}><span>Ranked</span></label>
      <div><h3 class="sheet-sub">Who can see it</h3>${segmented({ label: 'List visibility', value: form.visibility, options: ITEM_LEVELS, onChange: v => { form.visibility = v; s.update(); } })}</div>
    </div>`,
    footer: s => html`<button type="button" class="btn btn-danger" @click=${async () => {
        if (!await confirmSheet({ title: 'Delete this list?', message: 'The games stay in your library.', confirm: 'Delete', danger: true })) return;
        try { await api.del(`/api/v1/lists/${l.id}`); s.close(); toast('List deleted'); go('#you/lists'); } catch (err) { toastError(err); }
      }}>${icon('trash-2')}Delete</button><span class="spacer"></span>
      <button type="button" class="btn btn-primary" @click=${async () => { await listCall('patch', `/api/v1/lists/${l.id}`, form); s.close(); }}>Save</button>`,
  });
}

function editNote(l, it) {
  let note = it.note;
  openSheet({
    title: `Note on ${it.game.name}`,
    content: () => html`<textarea class="textarea" rows="4" maxlength="500" autofocus .value=${note} @input=${e => { note = e.target.value; }}></textarea>`,
    footer: s => html`<span class="spacer"></span><button type="button" class="btn btn-primary" @click=${async () => {
      await listCall('put', `/api/v1/lists/${l.id}/items/${it.game.id}`, { note }); s.close();
    }}>Save</button>`,
  });
}

function move(l, index, delta) {
  const ids = l.items.map(it => it.game.id);
  const j = index + delta;
  if (j < 0 || j >= ids.length) return;
  [ids[index], ids[j]] = [ids[j], ids[index]];
  listCall('post', `/api/v1/lists/${l.id}/order`, { game_ids: ids });
}

async function addGamesToList(l) {
  let q = '';
  openSheet({
    title: `Add to ${l.title}`,
    content: s => {
      const ql = q.trim().toLowerCase();
      const have = new Set(l.items.map(it => it.game.id));
      const found = app.store.entries().filter(e => !have.has(e.id) && (!ql || e.name.toLowerCase().includes(ql))).slice(0, 12);
      return html`<input class="input" type="search" autofocus placeholder="Search your library" .value=${q} @input=${e => { q = e.target.value; s.update(); }}>
        <div class="list" style="margin-top:var(--sp-3)">${found.map(e => html`<button type="button" class="list-row" @click=${async () => {
          await listCall('put', `/api/v1/lists/${l.id}/items/${e.id}`); s.update();
        }}>${cover(e, { cls: 'thumb' })}<div class="grow"><div class="title">${e.name}</div><div class="sub">${statusLabel(e.status)}</div></div>${icon('plus')}</button>`)}</div>
        <p class="muted" style="margin-top:var(--sp-2)">To add a game you don't have, open its page and choose “Add to list”.</p>`;
    },
  });
}

export function listView() {
  if (lst.error) return html`<div class="page">${empty({ iconName: 'list', title: lst.error.status === 404 ? 'List not found' : 'Couldn’t load this list', body: lst.error.status === 404 ? 'It may be private, or it was deleted.' : lst.error.message })}</div>`;
  const l = lst.data;
  if (!l) return html`<div class="page">${skeletonRows(5)}</div>`;
  return html`<div class="page">
    <header class="page-head">
      <div class="grow"><h1>${l.title}</h1>
        <p class="muted">${l.mine ? html`Your list · ${levelLabel(l.visibility)}` : html`By ${personLink(l.owner)}`} · ${plural(l.count, 'game')}${l.ranked ? ' · ranked' : ''}</p></div>
      ${l.mine ? html`<button type="button" class="btn btn-secondary btn-sm" @click=${() => addGamesToList(l)}>${icon('plus')}Add games</button>
        <button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label="Edit list" @click=${() => editListSheet(l)}>${icon('pencil')}</button>`
        : html`<button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label="Report list" @click=${() => report('list', l.id)}>${icon('flag')}</button>`}
    </header>
    ${l.hidden ? html`<p class="notice">${icon('shield')}An admin hid this list; only you can see it.</p>` : nothing}
    ${l.description ? html`<p class="bio section">${l.description}</p>` : nothing}
    ${l.items.length ? html`<ol class=${`list list-items ${l.ranked ? 'ranked' : ''}`}>${repeat(l.items, it => it.game.id, (it, i) => html`<li class="list-row">
      ${l.ranked ? html`<span class="rank num">${i + 1}</span>` : nothing}
      <a href=${`#game/${it.game.id}`}>${cover(it.game, { cls: 'thumb' })}</a>
      <div class="grow"><a class="title" href=${`#game/${it.game.id}`}>${it.game.name}</a>
        <div class="sub">${releaseYear(it.release_date) || ''}${it.status && !l.mine ? ` · ${statusLabel(it.status)} for you` : ''}</div>
        ${it.note ? html`<p class="list-note">${it.note}</p>` : nothing}</div>
      ${l.mine ? html`<div class="row">
        ${l.ranked ? html`<button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label=${`Move ${it.game.name} up`} ?disabled=${i === 0} @click=${() => move(l, i, -1)}>${icon('chevron-up')}</button>
          <button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label=${`Move ${it.game.name} down`} ?disabled=${i === l.items.length - 1} @click=${() => move(l, i, 1)}>${icon('chevron-down')}</button>` : nothing}
        <button type="button" class="btn btn-ghost btn-icon btn-sm" aria-label=${`Actions for ${it.game.name}`} @click=${e => openMenu({ anchor: e.currentTarget, label: it.game.name, items: [
          { label: it.note ? 'Edit note' : 'Add a note', icon: 'pencil', run: () => editNote(l, it) },
          { label: 'Remove from list', icon: 'x', danger: true, run: () => listCall('del', `/api/v1/lists/${l.id}/items/${it.game.id}`) },
        ] })}>${icon('ellipsis')}</button></div>` : nothing}
    </li>`)}</ol>`
    : empty({ iconName: 'list', title: 'No games yet', body: l.mine ? 'Add games from your library, or from any game page.' : '' })}
  </div>`;
}

// ---- people and invites --------------------------------------------------------

const ppl = { data: null, invites: null, error: null, q: '', results: null, seq: 0 };

export async function enterPeople() {
  try {
    const [data, invites] = await Promise.all([api.get('/api/v1/people'), api.get('/api/v1/invites')]);
    Object.assign(ppl, { data, invites, error: null });
  } catch (err) { ppl.error = err; }
  app.redraw();
}

async function searchPeople(q) {
  ppl.q = q;
  const seq = ++ppl.seq;
  if (!q.trim()) { ppl.results = null; app.redraw(); return; }
  try {
    const res = await api.get(`/api/v1/users?q=${encodeURIComponent(q)}`);
    if (seq === ppl.seq) { ppl.results = res.items; app.redraw(); }
  } catch { /* keep the old results */ }
}

async function peopleCall(method, path, msg) {
  try { await api[method](path); if (msg) toast(msg); await enterPeople(); } catch (err) { toastError(err); }
}

function personRow(p, actions = nothing, sub = '') {
  return html`<li class="list-row">${avatar(p)}<div class="grow"><a class="title" href=${`#u/${encodeURIComponent(p.username)}`}>${personName(p)}</a>
    <div class="sub">@${p.username}${sub ? ` · ${sub}` : ''}</div></div>${actions}</li>`;
}

async function createInvite() {
  let note = '';
  openSheet({
    title: 'Invite someone',
    content: () => html`<p class="muted">You’ll get a link that works once, for 7 days. Whoever signs up with it follows you, and you follow them.</p>
      <label class="field"><span>Who's it for? (only you see this)</span><input class="input" maxlength="100" autofocus @input=${e => { note = e.target.value; }}></label>`,
    footer: s => html`<span class="spacer"></span><button type="button" class="btn btn-primary" @click=${async () => {
      try {
        const res = await api.post('/api/v1/invites', { note });
        s.close();
        showLink('Invite link', location.origin + res.path, 'Send this link to the person you’re inviting. It works once, for 7 days.');
        enterPeople();
      } catch (err) { toastError(err); }
    }}>${icon('link')}Create link</button>`,
  });
}

export function showLink(title, url, message) {
  const copy = async e => {
    const field = e.currentTarget.closest('dialog')?.querySelector('input[aria-label="Link"]');
    toast(await copyText(url, field) ? 'Link copied' : 'Select the link and copy it');
  };
  openSheet({
    title,
    content: () => html`<p>${message}</p><input class="input" readonly .value=${url} @focus=${e => e.target.select()} aria-label="Link">`,
    footer: s => html`${navigator.share ? html`<button type="button" class="btn btn-secondary" @click=${() => navigator.share({ url }).catch(() => {})}>${icon('share-2')}Share</button>` : nothing}
      <span class="spacer"></span><button type="button" class="btn btn-primary" @click=${copy}>${icon('copy')}Copy link</button>`,
  });
}

export function peopleView() {
  const d = ppl.data;
  const inv = ppl.invites;
  return html`<div class="page">
    <header class="page-head"><h1>People</h1>
      ${inv?.can_invite ? html`<button type="button" class="btn btn-primary btn-sm" @click=${createInvite}>${icon('user-plus')}Invite</button>` : nothing}</header>
    <label class="field section"><span class="visually-hidden">Find people</span>
      <input class="input" type="search" placeholder="Find people on this server" .value=${ppl.q} @input=${e => searchPeople(e.target.value)}></label>
    ${ppl.results ? html`<section class="section"><ul class="list">${ppl.results.length
      ? ppl.results.map(p => personRow(p)) : html`<li class="muted">No one found. Private profiles only turn up for their exact username.</li>`}</ul></section>` : nothing}
    ${ppl.error ? empty({ iconName: 'triangle-alert', title: 'Couldn’t load people', body: ppl.error.message }) : !d ? skeletonRows(3) : html`
      ${d.requests.length ? html`<section class="section"><div class="section-head"><h2>Follow requests</h2></div><ul class="list">
        ${d.requests.map(p => personRow(p, html`<div class="row">
          <button type="button" class="btn btn-sm btn-primary" @click=${() => peopleCall('post', `/api/v1/followers/${encodeURIComponent(p.username)}/accept`, 'Accepted')}>Accept</button>
          <button type="button" class="btn btn-sm btn-secondary" @click=${() => peopleCall('del', `/api/v1/followers/${encodeURIComponent(p.username)}`)}>Decline</button></div>`))}</ul></section>` : nothing}
      <section class="section"><div class="section-head"><h2>Following</h2><span class="muted num">${d.following.length}</span></div>
        ${d.following.length ? html`<ul class="list">${d.following.map(p => personRow(p, html`<button type="button" class="btn btn-sm btn-secondary"
          @click=${() => peopleCall('del', `/api/v1/users/${encodeURIComponent(p.username)}/follow`)}>${p.status === 'pending' ? 'Cancel request' : 'Unfollow'}</button>`, p.status === 'pending' ? 'request sent' : ''))}</ul>`
        : html`<p class="muted">Follow people to see what they play on Home and their reviews first on game pages.</p>`}</section>
      <section class="section"><div class="section-head"><h2>Followers</h2><span class="muted num">${d.followers.length}</span></div>
        ${d.followers.length ? html`<ul class="list">${d.followers.map(p => personRow(p, html`<button type="button" class="btn btn-sm btn-ghost"
          @click=${() => peopleCall('del', `/api/v1/followers/${encodeURIComponent(p.username)}`, 'Removed')}>Remove</button>`))}</ul>`
        : html`<p class="muted">No followers yet.${app.me?.visibility === 'private' ? html` Your profile is private: <button type="button" class="link" @click=${editProfile}>change who can see it</button>.` : ''}</p>`}</section>
      ${inv ? html`<section class="section"><div class="section-head"><h2>Your invites</h2></div>
        ${!inv.can_invite ? html`<p class="muted">Sign-up is closed on this server. Ask an admin to invite people.</p>` : nothing}
        ${inv.items.length ? html`<ul class="list">${inv.items.map(i => html`<li class="list-row">${icon(i.state === 'used' ? 'circle-check' : 'ticket')}
          <div class="grow"><div class="title">${i.note || 'Invite'}</div><div class="sub">${i.state === 'used' && i.used_by ? html`Used by ${personLink(i.used_by)}` : i.state === 'open' ? `Expires ${ago(i.expires_at)}` : i.state}</div></div>
          ${i.state === 'open' ? html`<button type="button" class="btn btn-sm btn-ghost" @click=${() => peopleCall('del', `/api/v1/invites/${i.id}`, 'Invite revoked')}>Revoke</button>` : nothing}</li>`)}</ul>`
          : inv.can_invite ? html`<p class="muted">Invite friends and family: each link works once, for 7 days.</p>` : nothing}</section>` : nothing}
      ${d.blocked.length ? html`<section class="section"><div class="section-head"><h2>Blocked</h2></div><ul class="list">
        ${d.blocked.map(p => personRow(p, html`<button type="button" class="btn btn-sm btn-secondary" @click=${() => peopleCall('del', `/api/v1/users/${encodeURIComponent(p.username)}/block`, 'Unblocked')}>Unblock</button>`))}</ul></section>` : nothing}
    `}
  </div>`;
}

// ---- inbox ------------------------------------------------------------------

const inbox = { items: null, error: null };

export async function enterInbox() {
  try {
    const res = await api.get('/api/v1/notifications');
    inbox.items = res.items;
    inbox.error = null;
    if (res.unread) {
      const r = await api.post('/api/v1/notifications/read', { up_to: res.items[0]?.id || 0 });
      setUnread(r.unread);
    }
  } catch (err) { inbox.error = err; }
  app.redraw();
}

// setUnread updates the badge on You and the app icon (Badging API).
export function setUnread(n) {
  app.unread = n;
  try {
    if (n > 0) navigator.setAppBadge?.(n)?.catch(() => {});
    else navigator.clearAppBadge?.()?.catch(() => {});
  } catch { /* unsupported */ }
  app.redraw();
}

function notificationText(n) {
  const who = n.actor ? html`<strong>${personName(n.actor)}</strong>` : 'Someone';
  const game = n.game ? html`<strong>${n.game.name}</strong>` : '';
  switch (n.kind) {
    case 'follow': return html`${who} followed you`;
    case 'follow_request': return html`${who} asked to follow you`;
    case 'follow_accepted': return html`${who} accepted your follow request`;
    case 'like': return html`${who} liked your review of ${game}`;
    case 'invite_used': return html`${who} joined with your invite`;
    case 'release': return html`${game} is out today${n.data?.platform ? ` on ${n.data.platform}` : ''}`;
    case 'release_soon': return html`${game} comes out ${n.data?.when || 'soon'}`;
    case 'price': return html`${game} is ${n.data?.cut ? `${n.data.cut}% off` : 'on sale'}${n.data?.shop ? ` at ${n.data.shop}` : ''}${n.data?.price ? ` (${n.data.price})` : ''}${n.data?.low ? ' — historical low' : ''} · via IsThereAnyDeal`;
    default: return html`${n.kind.replaceAll('_', ' ')}${game ? html` · ${game}` : ''}`;
  }
}

function notificationHref(n) {
  if (n.kind === 'like' && n.game) return `#game/${n.game.id}`;
  if (n.game) return `#game/${n.game.id}`;
  if (n.kind === 'follow_request') return '#you/people';
  if (n.actor) return `#u/${encodeURIComponent(n.actor.username)}`;
  return '#you/inbox';
}

export function inboxView() {
  const items = inbox.items;
  return html`<div class="page">
    <header class="page-head"><h1>Inbox</h1><a class="btn btn-ghost btn-sm" href="#you/settings?section=notifications">${icon('settings')}Notifications</a></header>
    ${inbox.error ? empty({ iconName: 'triangle-alert', title: 'Couldn’t load your inbox', body: inbox.error.message })
      : !items ? skeletonRows(4)
      : items.length ? html`<ul class="list">${items.map(n => html`<li><a class=${`list-row ${n.read ? '' : 'unread'}`} href=${notificationHref(n)}>
          ${n.actor ? avatar(n.actor) : n.game ? cover(n.game, { cls: 'thumb' }) : icon('bell')}
          <div class="grow"><div>${notificationText(n)}</div><div class="sub">${ago(n.created_at)}</div></div>
          ${n.read ? nothing : html`<span class="dot" aria-label="New"></span>`}</a></li>`)}</ul>`
      : empty({ iconName: 'inbox', title: 'Nothing new', body: 'Follows, likes, invites and alerts for your wishlist show up here.' })}
  </div>`;
}
