// actions.js — every library write the screens make. Each one applies at
// once (optimistic, through the store's outbox), then offers Undo, which
// rolls the change back on the server (or drops it if it hasn't been sent).
import { app, today } from './app-state.js';
import { api } from '../core/api.js';
import { toast, toastError } from '../ui/toast.js';
import { statusLabel, formatMinutes, toStars, formatRating } from '../core/format.js';

// offerUndo shows message with an Undo that reverses the write.
export function offerUndo(message, sending) {
  const key = sending.key;
  const result = { value: null };
  sending.then(v => { result.value = v; }).catch(() => {});
  toast(message, {
    action: {
      label: 'Undo',
      run: async () => {
        // Not sent yet (offline or still queued): just drop it.
        if (await app.store.cancel(key)) return;
        const v = result.value ?? await sending;
        if (v?.undo_id) {
          await api.post(`/api/v1/undo/${v.undo_id}`);
          await app.store.sync();
          toast('Undone');
        }
      },
    },
  });
  return sending.catch(err => {
    toastError(err);
    throw err;
  });
}

function haptic() {
  if (app.prefs.haptics) navigator.vibrate?.(12);
}

// entryForGame builds a provisional library entry for a catalog game.
function entryForGame(game, status) {
  return {
    id: game.id,
    name: game.name,
    status,
    rating: 0,
    minutes: 0,
    platforms: app.prefs.default_platforms || [],
    tags: [],
    release: game.release_date || game.release || 0,
    ttb: game.ttb_minutes || game.ttb || 0,
    cover: game.cover || '',
    medium: '',
    notes: '',
    gamePlatforms: game.platforms || [],
    added: Math.floor(Date.now() / 1000),
  };
}

// addGame puts a catalog game on a shelf.
export function addGame(game, status = 'backlog', extra = {}) {
  haptic();
  const existing = app.store.get(game.id);
  const body = { status, played_on: today(), ...extra };
  if (!existing && (app.prefs.default_platforms || []).length && !('platforms' in body)) {
    body.platforms = app.prefs.default_platforms;
  }
  const effect = existing
    ? { type: 'patch', id: game.id, set: { status } }
    : { type: 'add', entry: entryForGame(game, status) };
  const sending = app.store.send({ method: 'PUT', path: `/api/v1/library/${game.id}`, body, effect });
  return offerUndo(existing ? `${game.name}: ${statusLabel(status)}` : `Added ${game.name} to ${statusLabel(status)}`, sending);
}

export function setStatus(id, status, extra = {}) {
  const e = app.store.get(id);
  haptic();
  const sending = app.store.send({
    method: 'PATCH', path: `/api/v1/library/${id}`,
    body: { status, played_on: today(), ...extra },
    effect: { type: 'patch', id, set: { status } },
  });
  return offerUndo(`${e?.name ?? 'Game'}: ${statusLabel(status)}`, sending);
}

export function rate(id, rating) {
  const e = app.store.get(id);
  const sending = app.store.send({
    method: 'PATCH', path: `/api/v1/library/${id}`, body: { rating },
    effect: { type: 'patch', id, set: { rating } },
  });
  const label = rating ? formatRating(rating, app.prefs.rating_display) : 'no rating';
  return offerUndo(`Rated ${e?.name ?? 'game'} ${label}`, sending);
}

// logTime records a play session (minutes on the user's local day).
export function logTime(id, minutes, { note = '', playedOn = today() } = {}) {
  const e = app.store.get(id);
  haptic();
  const sending = app.store.send({
    method: 'POST', path: '/api/v1/sessions', body: { game_id: id, minutes, played_on: playedOn, note },
    effect: { type: 'patch', id, minutesDelta: minutes },
  });
  return offerUndo(`Logged ${formatMinutes(minutes)} on ${e?.name ?? 'game'}`, sending);
}

// finish marks a game done with optional rating and completion type.
export function finish(id, { rating = null, completion = '', finishedOn = today(), notes = null } = {}) {
  const e = app.store.get(id);
  haptic();
  const body = { status: 'completed', played_on: finishedOn, completed_at: finishedOn };
  if (completion) body.completion = completion;
  if (rating !== null) body.rating = rating;
  if (notes !== null) body.notes = notes;
  const set = { status: 'completed' };
  if (rating !== null) set.rating = rating;
  const sending = app.store.send({ method: 'PATCH', path: `/api/v1/library/${id}`, body, effect: { type: 'patch', id, set } });
  const stars = rating ? ` · ${toStars(rating)} ★` : '';
  return offerUndo(`Finished ${e?.name ?? 'game'}${stars} 🎉`, sending);
}

export function editItem(id, change, message = 'Saved') {
  const set = {};
  for (const k of ['status', 'rating', 'tags', 'medium', 'platforms']) if (k in change) set[k] = change[k];
  if ('notes' in change) set.notes = String(change.notes).slice(0, 140);
  const effect = { type: 'patch', id, set };
  if ('minutes' in change) {
    const cur = app.store.get(id)?.minutes ?? 0;
    effect.minutesDelta = change.minutes - cur;
  }
  const sending = app.store.send({ method: 'PATCH', path: `/api/v1/library/${id}`, body: change, effect });
  return offerUndo(message, sending);
}

export function removeGame(id) {
  const e = app.store.get(id);
  const sending = app.store.send({ method: 'DELETE', path: `/api/v1/library/${id}`, effect: { type: 'remove', ids: [id] } });
  return offerUndo(`Removed ${e?.name ?? 'game'}`, sending);
}

export function bulk(ids, action) {
  const effect = action.remove ? { type: 'remove', ids }
    : { type: 'bulk', ids, set: action.status ? { status: action.status } : null, addTags: action.add_tags, removeTags: action.remove_tags };
  const sending = app.store.send({ method: 'POST', path: '/api/v1/library/bulk', body: { game_ids: ids, ...action }, effect });
  const what = action.remove ? 'Removed' : action.status ? `Moved to ${statusLabel(action.status)}:` : 'Updated';
  return offerUndo(`${what} ${ids.length} game${ids.length === 1 ? '' : 's'}`, sending);
}
