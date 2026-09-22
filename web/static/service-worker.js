// Bump on any release that changes shipped static assets so the new SW purges
// the previous static cache on activate. Covers have their own stable cache.
const CACHE_NAME = "cato-static-v23";
const COVER_CACHE_NAME = "cato-covers-v1";
const STATIC_CACHE_PREFIX = "cato-static-";
const OFFLINE_URL = "/offline.html";
const STATIC_ASSETS = [
  "/css/app.css",
  "/js/head-init.js",
  "/js/api.js",
  "/js/app.js",
  "/js/dates.js",
  "/js/library.js",
  "/js/search.js",
  "/js/stats.js",
  "/js/settings.js",
  "/js/playing.js",
  "/manifest.webmanifest",
  "/favicon.svg",
  "/icons/icon-192.png",
  "/icons/icon-512.png",
  "/icons/apple-touch-icon.png",
  OFFLINE_URL,
];

function isSuccessfulCover(response) {
  if (!response || !response.ok) {
    return false;
  }
  const contentType = response.headers?.get?.("Content-Type") || "";
  const mediaType = contentType.split(";", 1)[0].trim().toLowerCase();
  return mediaType.startsWith("image/") && mediaType !== "image/svg+xml";
}

function cacheResponse(cacheName, request, response) {
  let responseCopy;
  try {
    responseCopy = response.clone();
  } catch {
    return Promise.resolve();
  }
  return caches.open(cacheName)
    .then((cache) => cache.put(request, responseCopy))
    .catch(() => {});
}

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
    await Promise.all(keys
      .filter((key) => key.startsWith(STATIC_CACHE_PREFIX) && key !== CACHE_NAME)
      .map((key) => caches.delete(key)));
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
        try {
          const cache = await caches.open(CACHE_NAME);
          return await cache.match(OFFLINE_URL) || Response.error();
        } catch {
          return Response.error();
        }
      }
    })());
    return;
  }

  if (requestURL.pathname.startsWith("/covers/")) {
    event.respondWith((async () => {
      try {
        const cache = await caches.open(COVER_CACHE_NAME);
        const cached = await cache.match(event.request);
        if (cached) {
          return cached;
        }
      } catch {
        // A cache failure should not prevent the normal cover request.
      }

      const response = await fetch(event.request);
      if (isSuccessfulCover(response)) {
        try {
          // Keep cache I/O off the response path. Quota and cache failures are
          // swallowed by cacheResponse while the network response is returned.
          event.waitUntil(cacheResponse(COVER_CACHE_NAME, event.request, response));
        } catch {
          // waitUntil/clone failures must not turn a successful fetch into an error.
        }
      }
      return response;
    })());
    return;
  }

  if (!requestURL.pathname.startsWith("/css/") &&
      !requestURL.pathname.startsWith("/js/") &&
      !requestURL.pathname.startsWith("/icons/") &&
      requestURL.pathname !== "/manifest.webmanifest" &&
      requestURL.pathname !== OFFLINE_URL &&
      requestURL.pathname !== "/favicon.svg") {
    return;
  }

  event.respondWith((async () => {
    let cached;
    try {
      const cache = await caches.open(CACHE_NAME);
      cached = await cache.match(event.request);
    } catch {
      // Fall through to the network when the cache is unavailable.
    }
    if (cached) {
      return cached;
    }

    const response = await fetch(event.request);
    if (response && response.ok) {
      try {
        // A cache write must not delay the response or make it fail.
        event.waitUntil(cacheResponse(CACHE_NAME, event.request, response));
      } catch {
        // waitUntil/clone failures are safe to ignore for a network response.
      }
    }
    return response;
  })());
});
