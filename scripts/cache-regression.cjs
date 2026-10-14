// Service worker regression checks (run by make test-js): precache from
// /asset-manifest.json, offline navigation to the cached shell, versioned
// assets cache-first, cover caching rules (non-blocking writes, no
// placeholders, quota failures, LRU), cache upgrades, Background Sync.
const watchdog = setTimeout(() => { console.error('FAIL: asynchronous regression check did not complete'); process.exit(1); }, 8000);
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const deferred = () => {
  let resolve;
  const promise = new Promise(r => { resolve = r; });
  return { promise, resolve };
};

async function main() {
  const handlers = {};
  const stores = new Map();
  let networkCount = 0;
  let offline = false;
  let networkResponse = new Response('cover', { headers: { 'Content-Type': 'image/jpeg' } });
  let writeGate = null;
  let rejectWrites = false;
  const posted = [];
  const key = request => typeof request === 'string' ? request : request.url;
  const norm = u => u.startsWith('http') ? u : 'https://cato.test' + u;
  const caches = {
    async open(name) {
      if (!stores.has(name)) stores.set(name, new Map());
      const store = stores.get(name);
      return {
        async match(request) { return store.get(norm(key(request)))?.clone(); },
        async put(request, response) {
          if (writeGate) await writeGate.promise;
          if (rejectWrites) throw new Error('quota exceeded');
          store.delete(norm(key(request)));
          store.set(norm(key(request)), response.clone());
        },
        async addAll(paths) { for (const p of paths) store.set(norm(p), new Response('precached ' + p)); },
        async keys() { return [...store.keys()].map(url => ({ url })); },
        async delete(request) { return store.delete(norm(key(request))); },
      };
    },
    async keys() { return [...stores.keys()]; },
    async delete(name) { return stores.delete(name); },
  };
  const manifest = { version: 'abc123', precache: ['/', '/offline.html', '/css/tokens.css?v=1', '/js/app.js?v=2'] };
  const context = vm.createContext({
    URL, Response, caches, setTimeout, clearTimeout,
    self: {
      location: { origin: 'https://cato.test' },
      clients: { claim: async () => {}, matchAll: async () => [{ postMessage: m => posted.push(m) }] },
      skipWaiting: async () => {},
      addEventListener: (name, handler) => { handlers[name] = handler; },
    },
    fetch: async (request) => {
      const url = key(request);
      if (url === '/asset-manifest.json') return new Response(JSON.stringify(manifest));
      networkCount++;
      if (offline) throw new TypeError('Failed to fetch');
      return networkResponse.clone();
    },
  });
  const source = fs.readFileSync('web/static/service-worker.js', 'utf8').replace('__CATO_ASSET_VERSION__', 'abc123');
  assert.match(fs.readFileSync('web/static/service-worker.js', 'utf8'), /const VERSION = "__CATO_ASSET_VERSION__"/, 'the server stamps the version');
  vm.runInContext(source, context);

  const lifetime = [];
  handlers.install({ waitUntil: promise => lifetime.push(promise) });
  await Promise.all(lifetime);
  const shell = stores.get('cato-shell-abc123');
  assert.ok(shell.has('https://cato.test/') && shell.has('https://cato.test/js/app.js?v=2'), 'precaches the manifest');

  function dispatch(path, mode = 'cors') {
    let response;
    const pending = [];
    handlers.fetch({ request: { method: 'GET', url: 'https://cato.test' + path, mode },
      respondWith: promise => { response = promise; }, waitUntil: promise => pending.push(promise) });
    return { response, pending };
  }

  // API calls are never intercepted.
  assert.equal(dispatch('/api/v1/library').response, undefined);

  // Offline navigation: app routes get the cached shell, others offline.html.
  offline = true;
  assert.equal(await (await dispatch('/', 'navigate').response).text(), 'precached /');
  assert.equal(await (await dispatch('/library', 'navigate').response).text(), 'precached /');
  assert.equal(await (await dispatch('/login', 'navigate').response).text(), 'precached /offline.html');
  offline = false;

  // Versioned assets come from the cache without the network.
  const before = networkCount;
  assert.equal(await (await dispatch('/js/app.js?v=2').response).text(), 'precached /js/app.js?v=2');
  assert.equal(networkCount, before);

  // Covers: the response doesn't wait for the cache write.
  writeGate = deferred();
  const first = dispatch('/covers/1.jpg');
  let timer;
  try {
    const response = await Promise.race([first.response, new Promise((_, reject) => {
      timer = setTimeout(() => reject(new Error('response blocked by cache write')), 1000);
    })]);
    assert.equal(await response.text(), 'cover');
  } finally { clearTimeout(timer); }
  writeGate.resolve();
  await Promise.all(first.pending);
  writeGate = null;
  const beforeHit = networkCount;
  const hit = dispatch('/covers/1.jpg');
  assert.equal(await (await hit.response).text(), 'cover');
  assert.equal(networkCount, beforeHit, 'cached covers never revalidate');

  for (const [type, status] of [['image/svg+xml', 200], ['text/html', 200], ['image/jpeg', 404]]) {
    networkResponse = new Response('missing', { status, headers: { 'Content-Type': type } });
    const miss = dispatch('/covers/2.jpg');
    await miss.response;
    await Promise.all(miss.pending);
    assert.equal(stores.get('cato-covers-v1').size, 1, 'placeholders and errors must not enter the cover cache');
  }
  networkResponse = new Response('new cover', { headers: { 'Content-Type': 'image/jpeg' } });
  rejectWrites = true;
  const quota = dispatch('/covers/3.jpg');
  assert.equal(await (await quota.response).text(), 'new cover', 'a failed cache write still answers');
  await Promise.all(quota.pending);
  rejectWrites = false;

  // LRU: the cover cache is trimmed to 2000 entries.
  const covers = await caches.open('cato-covers-v1');
  for (let i = 0; i < 2100; i++) await covers.put(`/covers/x${i}.jpg`, new Response('c', { headers: { 'Content-Type': 'image/jpeg' } }));
  for (let i = 0; i < 50; i++) {
    const r = dispatch(`/covers/y${i}.jpg`);
    await r.response;
    await Promise.all(r.pending);
  }
  // Trimming runs every 50 writes, so the cache stays within 2000 + 50.
  assert.ok(stores.get('cato-covers-v1').size < 2050, `cover cache has ${stores.get('cato-covers-v1').size} entries`);
  assert.ok(!stores.get('cato-covers-v1').has('https://cato.test/covers/x0.jpg'), 'oldest covers are evicted first');
  assert.ok(stores.get('cato-covers-v1').has('https://cato.test/covers/y49.jpg'), 'newest covers survive trimming');

  // Activation removes older shells and the legacy static cache only.
  await caches.open('cato-shell-old');
  await caches.open('cato-static-v27');
  await caches.open('unrelated-cache');
  const activation = [];
  handlers.activate({ waitUntil: promise => activation.push(promise) });
  await Promise.all(activation);
  assert(!stores.has('cato-shell-old') && !stores.has('cato-static-v27'));
  assert(stores.has('cato-shell-abc123') && stores.has('cato-covers-v1') && stores.has('unrelated-cache'));

  // Background Sync wakes open pages to flush the outbox.
  const sync = [];
  handlers.sync({ tag: 'cato-outbox', waitUntil: p => sync.push(p) });
  await Promise.all(sync);
  assert.equal(JSON.stringify(posted), JSON.stringify([{ type: 'flush-outbox' }]));
  console.log('PASS: manifest precache, offline shell, versioned assets, cover cache rules and LRU, upgrades, background sync');
}

main().catch(error => { console.error(error); process.exitCode = 1; }).finally(() => clearTimeout(watchdog));
