import { test } from 'node:test';
import assert from 'node:assert/strict';
import { Store, applyEffect, entryFrom, entryFromItem } from '../../web/static/js/core/store.js';
import { memoryKV } from '../../web/static/js/core/idb.js';
import { ApiError } from '../../web/static/js/core/api.js';

const FIELDS = ['id', 'name', 'status', 'rating', 'minutes', 'updated', 'added', 'platforms', 'tags', 'release', 'ttb', 'cover', 'medium', 'notes', 'game_platforms'];
const row = (id, name, status, extra = {}) => FIELDS.map(f => ({ id, name, status, rating: 0, minutes: 0, updated: id, added: id, platforms: [], tags: [], release: 0, ttb: 0, cover: '', medium: '', notes: '', game_platforms: [130], ...extra })[f]);

// fakeServer is a scripted API: index responses by `since`, and a queue of
// write outcomes.
function fakeServer() {
  const s = {
    rev: 2,
    rows: [row(1, 'Hades', 'playing', { minutes: 60 }), row(2, 'Celeste', 'backlog')],
    deleted: [],
    calls: [],
    offline: false,
    reject: null,
    async request(method, path, opts = {}) {
      s.calls.push({ method, path, body: opts.body });
      if (s.offline) throw new ApiError(0, 'network', 'offline');
      if (method === 'GET' && path.startsWith('/api/v1/library/index')) {
        const since = Number(new URL('http://x' + path).searchParams.get('since'));
        if (opts.etag === `rev-${s.rev}`) return { status: 304, data: null, etag: opts.etag };
        return {
          status: 200, etag: `rev-${s.rev}`,
          data: { rev: s.rev, full: since === 0, fields: FIELDS, items: since === 0 ? s.rows : s.rows.filter(r => r[5] > since), deleted: since === 0 ? [] : s.deleted, platform_names: { 130: 'Nintendo Switch' } },
        };
      }
      if (s.reject) {
        const e = s.reject;
        s.reject = null;
        throw e;
      }
      return { status: 200, data: s.respond ? s.respond(method, path, opts.body) : {} };
    },
  };
  return s;
}

test('entries convert from index rows and items', () => {
  const e = entryFrom(FIELDS, row(1, 'Hades', 'playing'), { 130: 'Nintendo Switch' });
  assert.equal(e.name, 'Hades');
  assert.deepEqual(e.gamePlatforms, ['Nintendo Switch']);
  const it = entryFromItem({ game_id: 7, status: 'paused', rating: 80, minutes: 30, tags: ['a'], platforms: [], notes: 'n', medium: '', updated_at: '2026-10-14T12:00:00Z', created_at: '2026-10-01 10:00:00', game: { name: 'X', cover: '/covers/7.jpg', release_date: 5, platforms: ['PC'] } });
  assert.equal(it.id, 7);
  assert.equal(it.updated, Date.parse('2026-10-14T12:00:00Z') / 1000);
  assert.equal(it.added, Date.parse('2026-10-01T10:00:00Z') / 1000);
  assert.deepEqual(it.gamePlatforms, ['PC']);
});

test('effects are pure and composable', () => {
  const base = new Map([[1, { id: 1, status: 'backlog', minutes: 10, tags: ['RPG'], updated: 0 }]]);
  const a = applyEffect(base, { type: 'patch', id: 1, set: { status: 'playing' }, minutesDelta: 30 }, 99);
  assert.equal(a.get(1).status, 'playing');
  assert.equal(a.get(1).minutes, 40);
  assert.equal(a.get(1).updated, 99);
  assert.equal(base.get(1).status, 'backlog', 'base untouched');
  const b = applyEffect(a, { type: 'bulk', ids: [1, 5], addTags: ['co-op'], removeTags: ['rpg'] });
  assert.deepEqual(b.get(1).tags, ['co-op']);
  const c = applyEffect(b, { type: 'remove', ids: [1] });
  assert.equal(c.size, 0);
  const d = applyEffect(c, { type: 'add', entry: { id: 3, name: 'New', status: 'wishlist', minutes: 0, tags: [] } }, 5);
  assert.equal(d.get(3).added, 5);
});

test('init, full sync, delta sync and 304', async () => {
  const server = fakeServer();
  const kv = memoryKV();
  const store = new Store({ api: server, kv });
  assert.equal(await store.init('u1'), false);
  await store.sync();
  assert.equal(store.entries().length, 2);
  assert.equal(store.get(1).gamePlatforms[0], 'Nintendo Switch');
  assert.equal(store.rev, 2);
  assert.equal(await store.sync(), false, 'unchanged library answers 304');

  server.rev = 4;
  server.rows = [row(1, 'Hades', 'completed', { updated: 4 }), row(3, 'Balatro', 'wishlist', { updated: 3 })];
  server.deleted = [2];
  await store.sync();
  assert.equal(store.get(1).status, 'completed');
  assert.equal(store.get(3).name, 'Balatro');
  assert.equal(store.get(2), undefined);

  // A reload renders from disk before any network.
  const again = new Store({ api: server, kv });
  assert.equal(await again.init('u1'), true);
  assert.equal(again.entries().length, 2);
  assert.equal(again.rev, 4);

  // Another account on the same device starts clean.
  const other = new Store({ api: server, kv });
  await other.init('u2');
  assert.equal(other.entries().length, 0);
});

test('optimistic writes, confirmation and rejection', async () => {
  const server = fakeServer();
  const store = new Store({ api: server, kv: memoryKV() });
  await store.init('u1');
  await store.sync();
  server.respond = () => ({ item: { game_id: 1, status: 'playing', rating: 0, minutes: 90, tags: [], platforms: [], notes: '', updated_at: '2026-10-14T00:00:00Z', created_at: '2026-10-01T00:00:00Z', game: { name: 'Hades' } }, undo_id: 'u-1' });
  const sending = store.send({ method: 'POST', path: '/api/v1/sessions', body: { game_id: 1, minutes: 30 }, effect: { type: 'patch', id: 1, minutesDelta: 30 } });
  assert.equal(store.get(1).minutes, 90, 'applied before the server answers');
  const res = await sending;
  assert.equal(res.undo_id, 'u-1');
  await store.flushing;
  assert.equal(store.pending, 0);
  assert.equal(store.get(1).minutes, 90);

  server.reject = new ApiError(400, 'invalid_rating', 'Rating must be between 0 and 100', { rating: 'x' });
  const rejected = [];
  store.addEventListener('rejected', e => rejected.push(e.detail.error.code));
  await assert.rejects(store.send({ method: 'PATCH', path: '/api/v1/library/2', body: { rating: 500 }, effect: { type: 'patch', id: 2, set: { rating: 500 } } }), /Rating/);
  assert.equal(store.get(2).rating, 0, 'a rejected write reverts');
  assert.deepEqual(rejected, ['invalid_rating']);
});

test('offline writes queue, survive a reload and replay in order', async () => {
  const server = fakeServer();
  const kv = memoryKV();
  const store = new Store({ api: server, kv });
  await store.init('u1');
  await store.sync();
  server.offline = true;
  const r1 = await store.send({ method: 'POST', path: '/api/v1/sessions', body: { game_id: 1, minutes: 60 }, effect: { type: 'patch', id: 1, minutesDelta: 60 } });
  assert.deepEqual(r1, { queued: true });
  await store.send({ method: 'PATCH', path: '/api/v1/library/2', body: { status: 'playing' }, effect: { type: 'patch', id: 2, set: { status: 'playing' } } });
  assert.equal(store.pending, 2);
  assert.equal(store.get(1).minutes, 120);
  assert.equal(store.get(2).status, 'playing');
  clearTimeout(store.retryTimer);

  // Reload while offline: the queued effects are still visible.
  const reloaded = new Store({ api: server, kv });
  await reloaded.init('u1');
  assert.equal(reloaded.pending, 2);
  assert.equal(reloaded.get(1).minutes, 120);

  server.offline = false;
  server.calls = [];
  await reloaded.retryNow();
  const writes = server.calls.filter(c => c.method !== 'GET').map(c => c.method + ' ' + c.path);
  assert.deepEqual(writes, ['POST /api/v1/sessions', 'PATCH /api/v1/library/2']);
  assert.equal(reloaded.pending, 0);
  clearTimeout(reloaded.retryTimer);
});

test('an unsent write can be cancelled (offline undo)', async () => {
  const server = fakeServer();
  const store = new Store({ api: server, kv: memoryKV() });
  await store.init('u1');
  await store.sync();
  server.offline = true;
  await store.send({ method: 'PATCH', path: '/api/v1/library/1', body: { status: 'completed' }, effect: { type: 'patch', id: 1, set: { status: 'completed' } } });
  clearTimeout(store.retryTimer);
  const key = store.outbox[0].key;
  assert.equal(await store.cancel(key), true);
  assert.equal(store.get(1).status, 'playing');
  assert.equal(store.pending, 0);
});
