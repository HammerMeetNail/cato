// Cato service worker. The server stamps VERSION with the hash of every
// static file (internal/http/assets.go), so any change installs a new
// worker; there is no cache name to bump by hand. The precache list comes
// from /asset-manifest.json.
const VERSION = "__CATO_ASSET_VERSION__";
const SHELL_CACHE = "cato-shell-" + VERSION;
const COVER_CACHE = "cato-covers-v1";
const COVER_LIMIT = 2000;
const OFFLINE_URL = "/offline.html";
const SHELL_URL = "/";
const NAV_TIMEOUT_MS = 4000;
// Navigations that the single-page app handles (others get offline.html).
const APP_PATHS = new Set(["/", "/library", "/index.html"]);

function isSuccessfulCover(response) {
  if (!response || !response.ok) return false;
  const type = (response.headers?.get?.("Content-Type") || "").split(";", 1)[0].trim().toLowerCase();
  return type.startsWith("image/") && type !== "image/svg+xml";
}

function cacheResponse(cacheName, request, response) {
  let copy;
  try {
    copy = response.clone();
  } catch {
    return Promise.resolve();
  }
  return caches.open(cacheName).then(cache => cache.put(request, copy)).catch(() => {});
}

let coverWrites = 0;
// trimCovers keeps the cover cache an LRU of COVER_LIMIT entries: matches
// are re-put (moving them to the end of keys()), and the oldest go first.
async function trimCovers() {
  try {
    const cache = await caches.open(COVER_CACHE);
    const keys = await cache.keys();
    for (let i = 0; i < keys.length - COVER_LIMIT; i++) await cache.delete(keys[i]);
  } catch {
    // Trimming is best effort.
  }
}

self.addEventListener("install", event => {
  event.waitUntil((async () => {
    const res = await fetch("/asset-manifest.json", { cache: "no-store" });
    const manifest = await res.json();
    const cache = await caches.open(SHELL_CACHE);
    await cache.addAll(manifest.precache);
    // Take over at once; open pages show "Update available" on
    // controllerchange and reload when the user agrees.
    await self.skipWaiting();
  })());
});

self.addEventListener("activate", event => {
  event.waitUntil((async () => {
    const keys = await caches.keys();
    await Promise.all(keys
      .filter(k => (k.startsWith("cato-shell-") || k.startsWith("cato-static-")) && k !== SHELL_CACHE)
      .map(k => caches.delete(k)));
    await self.clients.claim();
  })());
});

// Background Sync: wake open pages to send queued writes (the outbox lives
// in the page; the CSRF token never leaves page memory).
self.addEventListener("sync", event => {
  if (event.tag !== "cato-outbox") return;
  event.waitUntil(self.clients.matchAll({ type: "window" })
    .then(list => list.forEach(c => c.postMessage({ type: "flush-outbox" }))));
});

function withTimeout(promise, ms) {
  return new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new Error("timeout")), ms);
    promise.then(v => { clearTimeout(t); resolve(v); }, e => { clearTimeout(t); reject(e); });
  });
}

async function fromShell(url) {
  try {
    const cache = await caches.open(SHELL_CACHE);
    return await cache.match(url);
  } catch {
    return undefined;
  }
}

self.addEventListener("fetch", event => {
  const { request } = event;
  if (request.method !== "GET") return;
  const url = new URL(request.url);
  if (url.origin !== self.location.origin || url.pathname.startsWith("/api/")) return;

  if (request.mode === "navigate") {
    event.respondWith((async () => {
      try {
        return await withTimeout(fetch(request), NAV_TIMEOUT_MS);
      } catch {
        // The installed app opens offline from the cached shell.
        if (APP_PATHS.has(url.pathname)) {
          const shell = await fromShell(SHELL_URL);
          if (shell) return shell;
        }
        return (await fromShell(OFFLINE_URL)) || Response.error();
      }
    })());
    return;
  }

  if (url.pathname.startsWith("/covers/")) {
    event.respondWith((async () => {
      try {
        const cache = await caches.open(COVER_CACHE);
        const cached = await cache.match(request);
        if (cached) return cached;
      } catch {
        // A cache failure must not block the network request.
      }
      const response = await fetch(request);
      if (isSuccessfulCover(response)) {
        try {
          event.waitUntil(cacheResponse(COVER_CACHE, request, response).then(() => {
            if (++coverWrites % 50 === 0) return trimCovers();
          }));
        } catch {
          // waitUntil/clone failures must not fail a good response.
        }
      }
      return response;
    })());
    return;
  }

  const isAsset = url.pathname.startsWith("/js/") || url.pathname.startsWith("/css/") ||
    url.pathname.startsWith("/icons/") || url.pathname === "/favicon.svg" ||
    url.pathname === "/manifest.webmanifest" || url.pathname === OFFLINE_URL;
  if (!isAsset) return;

  event.respondWith((async () => {
    const cached = await fromShell(request);
    // Versioned URLs never change: cache first. Unversioned ones are served
    // from cache and refreshed in the background.
    if (cached && url.searchParams.has("v")) return cached;
    const network = fetch(request).then(response => {
      if (response && response.ok) {
        try {
          event.waitUntil(cacheResponse(SHELL_CACHE, request, response));
        } catch {
          // Ignore: the response itself is fine.
        }
      }
      return response;
    });
    if (cached) {
      event.waitUntil(network.catch(() => {}));
      return cached;
    }
    return network;
  })());
});
