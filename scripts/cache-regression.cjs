const watchdog = setTimeout(() => { console.error("FAIL: asynchronous regression check did not complete"); process.exit(1); }, 5000);
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
  let networkResponse = new Response('cover', { headers: { 'Content-Type': 'image/jpeg' } });
  let writeGate = null;
  let rejectWrites = false;
  let precached = [];
  const key = request => typeof request === 'string' ? request : request.url;
  const caches = {
    async open(name) {
      if (!stores.has(name)) stores.set(name, new Map());
      const store = stores.get(name);
      return {
        async match(request) { return store.get(key(request))?.clone(); },
        async put(request, response) {
          if (writeGate) await writeGate.promise;
          if (rejectWrites) throw new Error('quota exceeded');
          store.set(key(request), response.clone());
        },
        async addAll(paths) { precached = paths; },
      };
    },
    async keys() { return [...stores.keys()]; },
    async delete(name) { return stores.delete(name); },
  };
  const context = vm.createContext({
    URL, Response, caches,
    self: { location: { origin: 'https://cato.test' }, clients: { claim: async () => {} },
      skipWaiting: async () => {}, addEventListener: (name, handler) => { handlers[name] = handler; } },
    fetch: async () => { networkCount++; return networkResponse.clone(); },
  });
  vm.runInContext(fs.readFileSync('web/static/service-worker.js', 'utf8'), context);
  const lifetime = [];
  handlers.install({ waitUntil: promise => lifetime.push(promise) });
  await Promise.all(lifetime);
  assert(precached.includes('/js/app.js') && precached.includes('/js/dates.js'));

  function dispatch(path) {
    let response;
    const pending = [];
    handlers.fetch({ request: { method: 'GET', url: 'https://cato.test' + path, mode: 'cors' },
      respondWith: promise => { response = promise; }, waitUntil: promise => pending.push(promise) });
    return { response, pending };
  }
  writeGate = deferred();
  const first = dispatch('/covers/1.jpg');
  // This deadline fails if a cache write is awaited on the response path.
  let timer;
  try {
    const response = await Promise.race([first.response, new Promise((_, reject) => {
      timer = setTimeout(() => reject(new Error('response blocked by cache write')), 1000);
    })]);
    assert.equal(await response.text(), 'cover');
  } finally { clearTimeout(timer); }
  assert.equal(stores.get('cato-covers-v1').size, 0);
  writeGate.resolve();
  await Promise.all(first.pending);
  writeGate = null;
  const beforeHit = networkCount;
  const hit = dispatch('/covers/1.jpg');
  assert.equal(await (await hit.response).text(), 'cover');
  await Promise.all(hit.pending);
  assert.equal(networkCount, beforeHit, 'cached covers never revalidate in the background');

  for (const [type, status] of [['image/svg+xml', 200], ['text/html', 200], ['image/jpeg', 404]]) {
    networkResponse = new Response('missing', { status, headers: { 'Content-Type': type } });
    const miss = dispatch('/covers/2.jpg');
    await miss.response;
    await Promise.all(miss.pending);
    assert.equal(stores.get('cato-covers-v1').size, 1, 'placeholders and errors must not enter cover cache');
  }
  networkResponse = new Response('new cover', { headers: { 'Content-Type': 'image/jpeg' } });
  const appeared = dispatch('/covers/2.jpg');
  assert.equal(await (await appeared.response).text(), 'new cover');
  await Promise.all(appeared.pending);
  rejectWrites = true;
  const quota = dispatch('/covers/3.jpg');
  assert.equal(await (await quota.response).text(), 'new cover');
  await Promise.all(quota.pending);
  rejectWrites = false;

  const staticMiss = dispatch('/js/app.js');
  await staticMiss.response;
  await Promise.all(staticMiss.pending);
  const beforeStaticHit = networkCount;
  await dispatch('/js/app.js').response;
  assert.equal(networkCount, beforeStaticHit);
  await caches.open('cato-static-v22');
  await caches.open('unrelated-cache');
  const activation = [];
  handlers.activate({ waitUntil: promise => activation.push(promise) });
  await Promise.all(activation);
  assert(!stores.has('cato-static-v22'));
  assert(stores.has('cato-covers-v1') && stores.has('unrelated-cache'));
  assert.equal(stores.get('cato-covers-v1').size, 2);
  console.log('PASS: cover/static cache hits, nonblocking writes, placeholder recovery, quota failure, cache upgrade');
}

main().catch(error => { console.error(error); process.exitCode = 1; }).finally(() => clearTimeout(watchdog));
