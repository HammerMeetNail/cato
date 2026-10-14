// store.js — the on-device library (docs/cato_smooth.md §4.2).
//
// The server is the source of truth. The store keeps the compact library
// index (GET /api/v1/library/index) in IndexedDB keyed by library_rev, so
// the app renders straight from disk and then syncs only what changed
// (?since=rev). Writes are optimistic: each one is an outbox operation whose
// effect is applied on top of the synced data ("base") until the server
// confirms it. Operations survive reloads and offline periods and are sent
// in order; a rejected one simply drops out, which reverts its effect.

export const OUTBOX_RETRY_MAX = 60_000;

// entryFrom turns an index row (an array, see IndexFields) into an entry.
export function entryFrom(fields, row, platformNames = {}) {
  const e = {};
  fields.forEach((f, i) => { e[f] = row[i]; });
  return {
    id: e.id,
    name: e.name,
    status: e.status,
    rating: e.rating || 0,
    minutes: e.minutes || 0,
    updated: e.updated || 0,
    added: e.added || 0,
    platforms: e.platforms || [],
    tags: e.tags || [],
    release: e.release || 0,
    ttb: e.ttb || 0,
    cover: e.cover || '',
    medium: e.medium || '',
    notes: e.notes || '',
    gamePlatforms: (e.game_platforms || []).map(id => platformNames[id]).filter(Boolean),
  };
}

const unix = ts => {
  if (!ts) return 0;
  const t = Date.parse(String(ts).includes('T') ? ts : String(ts).replace(' ', 'T') + 'Z');
  return Number.isNaN(t) ? 0 : Math.floor(t / 1000);
};

// entryFromItem converts a full library item (API v1 Item) to an entry.
export function entryFromItem(item) {
  return {
    id: item.game_id,
    name: item.game?.name ?? '',
    status: item.status,
    rating: item.rating || 0,
    minutes: item.minutes || 0,
    updated: unix(item.updated_at),
    added: unix(item.created_at),
    platforms: item.platforms || [],
    tags: item.tags || [],
    release: item.game?.release_date || 0,
    ttb: item.game?.ttb_minutes || 0,
    cover: item.game?.cover || '',
    medium: item.medium || '',
    notes: (item.notes || '').slice(0, 140),
    gamePlatforms: item.game?.platforms || [],
  };
}

// applyEffect returns entries (a Map) with one operation's effect applied.
// Effects are plain data so queued operations survive a reload.
export function applyEffect(entries, effect, now = Math.floor(Date.now() / 1000)) {
  const next = new Map(entries);
  const touch = (id, fn) => {
    const cur = next.get(id);
    if (cur) next.set(id, { ...fn({ ...cur }), updated: now });
  };
  switch (effect.type) {
    case 'add':
      next.set(effect.entry.id, { ...effect.entry, updated: now, added: next.get(effect.entry.id)?.added ?? now });
      break;
    case 'patch':
      touch(effect.id, e => {
        Object.assign(e, effect.set || {});
        if (effect.minutesDelta) e.minutes = Math.max(0, e.minutes + effect.minutesDelta);
        return e;
      });
      break;
    case 'remove':
      for (const id of effect.ids) next.delete(id);
      break;
    case 'bulk':
      for (const id of effect.ids) {
        touch(id, e => {
          if (effect.set) Object.assign(e, effect.set);
          if (effect.addTags || effect.removeTags) {
            const drop = new Set((effect.removeTags || []).map(t => t.toLowerCase()));
            const seen = new Set();
            e.tags = [...e.tags, ...(effect.addTags || [])].filter(t => {
              const k = t.toLowerCase();
              if (drop.has(k) || seen.has(k)) return false;
              seen.add(k);
              return true;
            });
          }
          return e;
        });
      }
      break;
  }
  return next;
}

export class Store extends EventTarget {
  // api: {request(method, path, opts)}; kv: idb.js facade.
  constructor({ api, kv, lock = null, now = () => Date.now() }) {
    super();
    this.api = api;
    this.kv = kv;
    this.lock = lock;
    this.now = now;
    this.base = new Map();
    this.view = new Map();
    this.rev = 0;
    this.etag = null;
    this.platformNames = {};
    this.outbox = [];
    this.waiters = new Map();
    this.synced = false;
    this.flushing = null;
    this.retryDelay = 2000;
    this.retryTimer = null;
    this.seq = 0;
  }

  // init loads the local copy for userId (a different account's data is
  // discarded) and queued operations. Call sync() afterwards.
  async init(userId) {
    const meta = await this.kv.get('meta', 'library');
    if (!meta || meta.userId !== userId) {
      await this.kv.clearAll();
      await this.kv.put('meta', 'library', { userId, rev: 0, platformNames: {} });
      this.rev = 0;
    } else {
      this.rev = meta.rev || 0;
      this.platformNames = meta.platformNames || {};
      for (const [id, e] of await this.kv.all('items')) this.base.set(Number(id), e);
      this.outbox = (await this.kv.all('outbox')).map(([, op]) => ({ ...op, persisted: true })).sort((a, b) => a.seq - b.seq);
      this.seq = this.outbox.reduce((m, op) => Math.max(m, op.seq), 0);
    }
    this.userId = userId;
    this.recompute();
    return this.view.size > 0 || this.rev > 0;
  }

  recompute() {
    let v = this.base;
    for (const op of this.outbox) if (op.effect) v = applyEffect(v, op.effect, op.at);
    this.view = v;
    this.dispatchEvent(new Event('change'));
  }

  get(id) { return this.view.get(Number(id)); }
  entries() { return [...this.view.values()]; }
  get pending() { return this.outbox.length; }

  // sync fetches what changed since the local revision.
  async sync() {
    const path = `/api/v1/library/index?since=${this.rev}`;
    let res;
    try {
      res = await this.api.request('GET', path, { etag: this.etag });
    } catch (err) {
      this.dispatchEvent(new CustomEvent('syncerror', { detail: err }));
      throw err;
    }
    this.synced = true;
    if (res.status === 304) return false;
    const idx = res.data;
    this.etag = res.etag;
    const names = { ...this.platformNames, ...idx.platform_names };
    const rows = idx.items.map(row => entryFrom(idx.fields, row, names));
    if (idx.full) {
      this.base = new Map(rows.map(e => [e.id, e]));
      await this.kv.replace('items', rows.map(e => [e.id, e]));
    } else {
      for (const e of rows) this.base.set(e.id, e);
      for (const id of idx.deleted) this.base.delete(id);
      await this.kv.putMany('items', rows.map(e => [e.id, e]), idx.deleted);
    }
    this.rev = idx.rev;
    this.platformNames = names;
    await this.kv.put('meta', 'library', { userId: this.userId, rev: this.rev, platformNames: names });
    this.recompute();
    return true;
  }

  // send queues a write. op: {method, path, body, effect, label}. Resolves
  // with the server response once sent, or {queued: true} when offline.
  // Rejects (and reverts) when the server refuses it.
  // The returned promise carries the operation's outbox key (for cancel).
  send(op) {
    const queued = { ...op, seq: ++this.seq, at: Math.floor(this.now() / 1000), key: `${Date.now()}-${this.seq}` };
    const done = new Promise((resolve, reject) => this.waiters.set(queued.key, { resolve, reject }));
    this.outbox.push(queued);
    // Show the effect at once; persist, then send.
    this.recompute();
    const persisted = () => { queued.persisted = true; this.flush(); };
    this.kv.put('outbox', queued.key, queued).then(persisted, persisted);
    done.key = queued.key;
    return done;
  }

  settle(key, how, value) {
    const w = this.waiters.get(key);
    if (!w) return;
    this.waiters.delete(key);
    w[how](value);
  }

  // cancel drops a queued operation that hasn't been sent (offline undo).
  async cancel(key) {
    const i = this.outbox.findIndex(op => op.key === key);
    if (i < 0 || this.outbox[i].sending) return false;
    const [op] = this.outbox.splice(i, 1);
    await this.kv.del('outbox', key);
    this.recompute();
    this.settle(key, 'resolve', { cancelled: true });
    return true;
  }

  flush() {
    if (this.flushing) return this.flushing;
    const run = async () => {
      try {
        while (this.outbox.length) {
          const op = this.outbox[0];
          // Not on disk yet: sending now could let the late write
          // resurrect it after it is removed. Its persist callback flushes.
          if (!op.persisted) return;
          op.sending = true;
          let res;
          try {
            res = await this.api.request(op.method, op.path, { body: op.body });
          } catch (err) {
            op.sending = false;
            if (err.retryable) {
              this.scheduleRetry();
              // Everything waiting is now queued for later.
              for (const q of this.outbox) this.settle(q.key, 'resolve', { queued: true });
              this.dispatchEvent(new CustomEvent('offline', { detail: err }));
              return;
            }
            this.outbox.shift();
            await this.kv.del('outbox', op.key);
            this.recompute();
            this.dispatchEvent(new CustomEvent('rejected', { detail: { op, error: err } }));
            this.settle(op.key, 'reject', err);
            continue;
          }
          this.outbox.shift();
          await this.kv.del('outbox', op.key);
          // The confirmed item replaces the base copy straight away; the
          // revision only moves with sync(), so nothing is skipped.
          const item = res.data?.item;
          if (item && item.game_id) {
            const e = entryFromItem(item);
            this.base.set(e.id, e);
            await this.kv.put('items', e.id, e);
          } else if (op.effect?.type === 'remove') {
            for (const id of op.effect.ids) this.base.delete(id);
            await this.kv.putMany('items', [], op.effect.ids);
          }
          this.recompute();
          this.retryDelay = 2000;
          this.settle(op.key, 'resolve', res.data ?? {});
          this.dispatchEvent(new CustomEvent('sent', { detail: { op, data: res.data } }));
        }
        await this.sync().catch(() => {});
      } finally {
        this.flushing = null;
      }
    };
    this.flushing = this.lock ? this.lock('cato-outbox', run) : run();
    return this.flushing;
  }

  scheduleRetry() {
    clearTimeout(this.retryTimer);
    this.retryTimer = setTimeout(() => this.flush(), this.retryDelay);
    this.retryDelay = Math.min(this.retryDelay * 2, OUTBOX_RETRY_MAX);
  }

  // retryNow is called when the device reconnects.
  retryNow() {
    clearTimeout(this.retryTimer);
    this.retryDelay = 2000;
    return this.flush();
  }

  async clear() {
    clearTimeout(this.retryTimer);
    this.base = new Map();
    this.outbox = [];
    this.rev = 0;
    await this.kv.clearAll();
    this.recompute();
  }
}
