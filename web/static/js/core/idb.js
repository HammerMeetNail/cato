// idb.js — a small key-value facade over IndexedDB, with an in-memory
// stand-in for tests and for browsers that refuse IndexedDB (some private
// modes). Stores: meta (sync state), items (index entries), outbox.

const STORES = ['meta', 'items', 'outbox'];

function promisify(req) {
  return new Promise((resolve, reject) => {
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

export async function openKV(name = 'cato') {
  if (typeof indexedDB === 'undefined') return memoryKV();
  let db;
  try {
    db = await new Promise((resolve, reject) => {
      const req = indexedDB.open(name, 1);
      req.onupgradeneeded = () => {
        for (const s of STORES) if (!req.result.objectStoreNames.contains(s)) req.result.createObjectStore(s);
      };
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
      req.onblocked = () => reject(new Error('indexedDB blocked'));
    });
  } catch {
    return memoryKV();
  }
  const tx = (store, mode, fn) => new Promise((resolve, reject) => {
    const t = db.transaction(store, mode);
    const result = fn(t.objectStore(store));
    t.oncomplete = () => resolve(result instanceof IDBRequest ? result.result : result);
    t.onerror = () => reject(t.error);
    t.onabort = () => reject(t.error);
  });
  return {
    get: (store, key) => tx(store, 'readonly', s => s.get(key)),
    put: (store, key, value) => tx(store, 'readwrite', s => { s.put(value, key); }),
    del: (store, key) => tx(store, 'readwrite', s => { s.delete(key); }),
    async all(store) {
      return new Promise((resolve, reject) => {
        const t = db.transaction(store, 'readonly');
        const out = [];
        const req = t.objectStore(store).openCursor();
        req.onsuccess = () => {
          const c = req.result;
          if (!c) return;
          out.push([c.key, c.value]);
          c.continue();
        };
        t.oncomplete = () => resolve(out);
        t.onerror = () => reject(t.error);
      });
    },
    // putMany replaces a whole store in one transaction.
    replace: (store, entries) => tx(store, 'readwrite', s => {
      s.clear();
      for (const [k, v] of entries) s.put(v, k);
    }),
    putMany: (store, entries, deletes = []) => tx(store, 'readwrite', s => {
      for (const [k, v] of entries) s.put(v, k);
      for (const k of deletes) s.delete(k);
    }),
    clear: store => tx(store, 'readwrite', s => { s.clear(); }),
    async clearAll() { for (const s of STORES) await this.clear(s); },
    persistent: true,
  };
}

export function memoryKV() {
  const data = Object.fromEntries(STORES.map(s => [s, new Map()]));
  const clone = v => (v === undefined ? v : structuredClone(v));
  return {
    async get(store, key) { return clone(data[store].get(key)); },
    async put(store, key, value) { data[store].set(key, clone(value)); },
    async del(store, key) { data[store].delete(key); },
    async all(store) { return [...data[store].entries()].map(([k, v]) => [k, clone(v)]); },
    async replace(store, entries) { data[store].clear(); for (const [k, v] of entries) data[store].set(k, clone(v)); },
    async putMany(store, entries, deletes = []) {
      for (const [k, v] of entries) data[store].set(k, clone(v));
      for (const k of deletes) data[store].delete(k);
    },
    async clear(store) { data[store].clear(); },
    async clearAll() { for (const s of STORES) data[s].clear(); },
    persistent: false,
  };
}
