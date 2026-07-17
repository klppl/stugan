// stugan service worker: enables installability and Web Push notifications.
// It deliberately does NOT cache app assets (an IRC client should always be
// fresh); its job is the push lifecycle.

self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (e) => e.waitUntil(self.clients.claim()));

self.addEventListener("push", (event) => {
  let data = {};
  try {
    data = event.data ? event.data.json() : {};
  } catch (_) {
    data = { title: "stugan", body: event.data ? event.data.text() : "" };
  }
  const title = data.title || "stugan";
  event.waitUntil(
    self.registration.showNotification(title, {
      body: data.body || "",
      icon: "/icons/icon-192.png?v=2",
      badge: "/icons/icon-192.png?v=2",
      tag: (data.network || "") + "/" + (data.buffer || ""),
      data: { network: data.network, buffer: data.buffer },
    }),
  );
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const d = event.notification.data || {};
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((cs) => {
      // An open tab: tell it which buffer to switch to, then focus it.
      for (const c of cs) {
        if ("focus" in c) {
          if (d.network && d.buffer) {
            c.postMessage({ type: "navigate", network: d.network, buffer: d.buffer });
          }
          return c.focus();
        }
      }
      // No tab open: cold-start with the target as a deep link the app reads.
      if (self.clients.openWindow) {
        const q =
          d.network && d.buffer
            ? "/?net=" + encodeURIComponent(d.network) + "&buf=" + encodeURIComponent(d.buffer)
            : "/";
        return self.clients.openWindow(q);
      }
    }),
  );
});

// The push service can rotate a subscription (key expiry, browser policy). The
// old endpoint then goes dead silently, so re-subscribe with the current VAPID
// key and re-register with the server.
self.addEventListener("pushsubscriptionchange", (event) => {
  event.waitUntil(
    (async () => {
      const res = await fetch("/api/push/vapid");
      if (!res.ok) return;
      const { key } = await res.json();
      const sub = await self.registration.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: urlBase64ToUint8Array(key),
      });
      await fetch("/api/push/subscribe", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(sub),
      });
    })(),
  );
});

// urlBase64ToUint8Array mirrors the helper in pwa.ts; the service worker is a
// separate context and can't import it.
function urlBase64ToUint8Array(base64) {
  const padding = "=".repeat((4 - (base64.length % 4)) % 4);
  const b64 = (base64 + padding).replace(/-/g, "+").replace(/_/g, "/");
  const raw = atob(b64);
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}
