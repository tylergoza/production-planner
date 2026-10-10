// Service worker. Served from /sw.js by the Go server, which injects the
// asset version and precache list below. A new asset version means a new
// cache; old caches are removed on activate.
const VERSION = "__VERSION__"
const STATIC_CACHE = `pp-static-${VERSION}`
const PAGE_CACHE = "pp-pages"
const PRECACHE = __PRECACHE__

// Pages kept for offline viewing, so the dashboard, a production's
// overview and its mic chart still open backstage with no signal. Edit
// screens always need the network.
const OFFLINE_PAGE = /^\/(productions\/\d+(\/mics)?)?$/

// Signing in and out is left entirely to the browser: never cached, and
// the redirects to and from User Management (another origin) are followed
// by the browser itself rather than through this worker.
const AUTH_PAGE = /^\/(login|logout|signed-out|auth\/.*)$/

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches.open(STATIC_CACHE).then((cache) => cache.addAll(PRECACHE)).then(() => self.skipWaiting())
  )
})

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(
        keys.filter((key) => key.startsWith("pp-static-") && key !== STATIC_CACHE).map((key) => caches.delete(key))
      ))
      .then(() => self.clients.claim())
  )
})

self.addEventListener("fetch", (event) => {
  const request = event.request
  const url = new URL(request.url)
  if (url.origin !== self.location.origin) return

  // Signing out: forget cached pages that may show signed-in content.
  if (request.method === "POST" && url.pathname === "/logout") {
    event.waitUntil(caches.delete(PAGE_CACHE))
    return
  }
  if (request.method !== "GET" || AUTH_PAGE.test(url.pathname)) return

  if (url.pathname.startsWith("/static/")) {
    event.respondWith(cacheFirst(request))
  } else if (request.mode === "navigate") {
    event.respondWith(networkFirstPage(request, url))
  }
})

async function cacheFirst(request) {
  const cache = await caches.open(STATIC_CACHE)
  const cached = await cache.match(request)
  if (cached) return cached
  const response = await fetch(request)
  if (response.ok) cache.put(request, response.clone())
  return response
}

// Navigations are fetched with the request's own redirect mode
// ("manual"), so a redirect (e.g. to /login and on to User Management)
// goes back to the browser as-is and it follows it.
async function networkFirstPage(request, url) {
  try {
    const response = await fetch(request)
    if (response.ok && !response.redirected && OFFLINE_PAGE.test(url.pathname)) {
      const cache = await caches.open(PAGE_CACHE)
      cache.put(request, response.clone())
    }
    return response
  } catch (error) {
    const cache = await caches.open(PAGE_CACHE)
    return (
      (await cache.match(request)) ||
      (url.pathname === "/" && (await cache.match("/"))) ||
      (await caches.match("/offline")) ||
      new Response("You are offline.", { status: 503, headers: { "Content-Type": "text/plain" } })
    )
  }
}
