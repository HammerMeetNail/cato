const watchdog = setTimeout(() => { console.error("FAIL: asynchronous regression check did not complete"); process.exit(1); }, 5000);
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const deferred = () => {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};
const source = name => fs.readFileSync(`web/static/js/${name}.js`, 'utf8')
  .replace(/^import .*;\n/gm, '').replace(/export /g, '');

async function stats() {
  let revision = 0, requests = 0;
  const first = deferred(), second = deferred(), secondStarted = deferred();
  const container = { innerHTML: '', querySelectorAll: () => [], querySelector: () => null };
  const context = vm.createContext({ getLibraryRevision: () => revision,
    api: { get: () => {
      requests++;
      if (requests === 1) return first.promise;
      if (requests === 2) { secondStarted.resolve(); return second.promise; }
      if (requests === 3) return Promise.reject(new Error('offline'));
      return Promise.resolve({ label: 'retried' });
    } }, window: { location: {} } });
  vm.runInContext(source('stats'), context);
  vm.runInContext('buildStatsHTML = stats => stats.label', context);
  const render = vm.runInContext('renderStatsView', context);
  const pending = render(container);
  assert.equal(render(container), pending, 'tab revisits share the pending stats request');
  revision++;
  first.resolve({ label: 'obsolete' });
  await secondStarted.promise;
  assert(!container.innerHTML.includes('obsolete'));
  second.resolve({ label: 'current' });
  await pending;
  assert.equal(container.innerHTML, 'current');
  await render(container);
  assert.equal(requests, 2);
  revision++;
  await render(container);
  assert(container.innerHTML.includes('Failed to load stats'));
  await render(container);
  assert.equal(container.innerHTML, 'retried', 'failed requests remain retryable');
}

async function settings() {
  let revision = 0, authRequests = 0, countRequests = 0;
  const auth = deferred();
  const summary = { textContent: '' };
  const container = { innerHTML: '', querySelector: selector => selector === '#librarySummary' ? summary : null };
  const context = vm.createContext({ getLibraryRevision: () => revision,
    api: { get: () => { authRequests++; return auth.promise; } },
    library: { counts: async () => { countRequests++; return { all: countRequests, completed_count: 0 }; } },
    window: { location: {} } });
  vm.runInContext(source('settings'), context);
  vm.runInContext("buildSettingsHTML = () => 'settings form'; wireSettings = () => {}", context);
  const render = vm.runInContext('renderSettingsView', context);
  const pending = render(container);
  assert.equal(render(container), pending);
  auth.resolve({ authenticated: true });
  await pending;
  assert.equal(authRequests, 1);
  assert.equal(countRequests, 1);
  container.innerHTML = 'unsaved name and password fields';
  await render(container);
  assert.equal(countRequests, 1);
  revision++;
  await render(container);
  assert.equal(authRequests, 1);
  assert.equal(countRequests, 2);
  assert.equal(container.innerHTML, 'unsaved name and password fields');
  assert(summary.textContent.includes('2 games'));
}

Promise.all([stats(), settings()]).then(() => {
  console.log('PASS: coalesced tab requests, mutation during stats load, failure retry, Settings draft preservation');
}).catch(error => { console.error(error); process.exitCode = 1; }).finally(() => clearTimeout(watchdog));
