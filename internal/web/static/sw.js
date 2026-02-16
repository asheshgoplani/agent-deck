const CACHE_VERSION = "agentdeck-shell-v2"
const SHELL_CACHE = CACHE_VERSION
const APP_SHELL_URLS = [
  "/",
  "/manifest.webmanifest",
  "/static/index.html",
  "/static/styles.css",
  "/static/app.js",
  "/static/icons/logo.svg",
]

const DEFAULT_NOTIFICATION_ICON = "/static/icons/logo.svg"

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches.open(SHELL_CACHE).then((cache) => cache.addAll(APP_SHELL_URLS)),
  )
  self.skipWaiting()
})

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) =>
        Promise.all(
          keys
            .filter((key) => key !== SHELL_CACHE)
            .map((key) => caches.delete(key)),
        ),
      )
      .then(() => self.clients.claim()),
  )
})

self.addEventListener("fetch", (event) => {
  const req = event.request
  if (req.method !== "GET") {
    return
  }

  const url = new URL(req.url)
  if (url.origin !== self.location.origin) {
    return
  }

  if (url.pathname === "/sw.js") {
    event.respondWith(fetch(req))
    return
  }

  if (req.mode === "navigate") {
    event.respondWith(handleNavigate(req))
    return
  }

  if (
    url.pathname.startsWith("/api/") ||
    url.pathname.startsWith("/events/") ||
    url.pathname.startsWith("/ws/")
  ) {
    event.respondWith(handleRuntimeRequest(req, url.pathname))
    return
  }

  event.respondWith(handleShellAsset(req))
})

self.addEventListener("push", (event) => {
  let payload = {}
  try {
    payload = event.data ? event.data.json() : {}
  } catch (_err) {
    payload = {
      title: "Agent Deck",
      body: event.data ? event.data.text() : "Session update received.",
    }
  }

  const title = payload.title || "Agent Deck"
  const options = {
    body: payload.body || "Session update received.",
    icon: payload.icon || DEFAULT_NOTIFICATION_ICON,
    badge: payload.badge || DEFAULT_NOTIFICATION_ICON,
    tag: payload.tag || `agentdeck-${payload.sessionId || "session"}`,
    renotify: !!payload.renotify,
    requireInteraction: !!payload.requireInteraction,
    data: {
      path: payload.path || "/",
      sessionId: payload.sessionId || "",
      profile: payload.profile || "",
      status: payload.status || "",
    },
  }

  event.waitUntil(self.registration.showNotification(title, options))
})

self.addEventListener("notificationclick", (event) => {
  event.notification.close()
  const data = event.notification.data || {}
  const relativePath = typeof data.path === "string" && data.path ? data.path : "/"
  const targetURL = new URL(relativePath, self.location.origin).toString()

  event.waitUntil(
    clients.matchAll({ type: "window", includeUncontrolled: true }).then((allClients) => {
      for (const client of allClients) {
        const clientURL = new URL(client.url)
        if (clientURL.origin !== self.location.origin) {
          continue
        }
        if ("focus" in client) {
          if ("navigate" in client) {
            client.navigate(targetURL)
          }
          return client.focus()
        }
      }
      if (clients.openWindow) {
        return clients.openWindow(targetURL)
      }
      return undefined
    }),
  )
})

async function handleNavigate(req) {
  try {
    return await fetch(req)
  } catch (_err) {
    const cache = await caches.open(SHELL_CACHE)
    const cached = (await cache.match("/")) || (await cache.match("/static/index.html"))
    if (cached) {
      return cached
    }
    return new Response(
      "<!doctype html><title>Agent Deck Offline</title><p>Agent Deck server unavailable.</p>",
      {
        status: 503,
        headers: { "Content-Type": "text/html; charset=utf-8" },
      },
    )
  }
}

async function handleRuntimeRequest(req, pathname) {
  try {
    return await fetch(req)
  } catch (_err) {
    if (pathname.startsWith("/events/")) {
      return new Response(
        'event: error\ndata: {"error":{"code":"SERVER_UNAVAILABLE","message":"Agent Deck server unavailable"}}\n\n',
        {
          status: 503,
          headers: {
            "Content-Type": "text/event-stream; charset=utf-8",
            "Cache-Control": "no-cache",
          },
        },
      )
    }

    if (pathname.startsWith("/api/")) {
      return new Response(
        JSON.stringify({
          error: {
            code: "SERVER_UNAVAILABLE",
            message: "Agent Deck server unavailable",
          },
        }),
        {
          status: 503,
          headers: { "Content-Type": "application/json; charset=utf-8" },
        },
      )
    }

    return new Response("Agent Deck server unavailable", { status: 503 })
  }
}

async function handleShellAsset(req) {
  const cache = await caches.open(SHELL_CACHE)
  const cached = await cache.match(req)
  if (cached) {
    return cached
  }

  try {
    const fresh = await fetch(req)
    if (fresh && fresh.ok) {
      cache.put(req, fresh.clone())
    }
    return fresh
  } catch (_err) {
    const fallback = await cache.match("/")
    if (fallback) {
      return fallback
    }
    return new Response("offline", { status: 503 })
  }
}
