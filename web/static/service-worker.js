// Bump on any release that changes shipped static assets so the new SW purges
// the previous cache on activate. Prevents stale JS/CSS after deploys.
const CACHE_NAME = "cato-static-v2";
const OFFLINE_URL = "/offline.html";
const STATIC_ASSETS = [
  "/css/app.css",
  "/js/head-init.js",
  "/js/api.js",
  "/js/library.js",
  "/js/search.js",
  "/js/stats.js",
  "/js/settings.js",
  "/manifest.webmanifest",
  "/favicon.svg",
  "/icons/icon-192.png",
  "/icons/icon-512.png",
  "/icons/apple-touch-icon.png",
  OFFLINE_URL,
];

self.addEventListener("install", (event) => {
  event.waitUntil((async () => {
    const cache = await caches.open(CACHE_NAME);
    await cache.addAll(STATIC_ASSETS);
    await self.skipWaiting();
  })());
});

self.addEventListener("activate", (event) => {
  event.waitUntil((async () => {
    const keys = await caches.keys();
    await Promise.all(keys.filter((key) => key !== CACHE_NAME).map((key) => caches.delete(key)));
    await self.clients.claim();
  })());
});

self.addEventListener("fetch", (event) => {
  if (event.request.method !== "GET") {
    return;
  }

  const requestURL = new URL(event.request.url);
  if (requestURL.origin !== self.location.origin) {
    return;
  }

  if (event.request.mode === "navigate") {
    event.respondWith((async () => {
      try {
        return await fetch(event.request);
      } catch {
        const cache = await caches.open(CACHE_NAME);
        return await cache.match(OFFLINE_URL) || Response.error();
      }
    })());
    return;
  }

  if (!requestURL.pathname.startsWith("/css/") &&
      !requestURL.pathname.startsWith("/js/") &&
      !requestURL.pathname.startsWith("/icons/") &&
      !requestURL.pathname.startsWith("/covers/") &&
      requestURL.pathname !== "/manifest.webmanifest" &&
      requestURL.pathname !== "/offline.html" &&
      requestURL.pathname !== "/favicon.svg") {
    return;
  }

  event.respondWith((async () => {
    const cache = await caches.open(CACHE_NAME);
    const cached = await cache.match(event.request);
    if (cached) {
      void fetch(event.request).then((response) => {
        if (response && response.ok) {
          void cache.put(event.request, response.clone());
        }
      }).catch(() => {});
      return cached;
    }
    const response = await fetch(event.request);
    if (response && response.ok) {
      await cache.put(event.request, response.clone());
    }
    return response;
  })());
});
