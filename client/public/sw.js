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
      icon: "/icons/icon-192.png",
      badge: "/icons/icon-192.png",
      tag: (data.network || "") + "/" + (data.buffer || ""),
      data: { network: data.network, buffer: data.buffer },
    }),
  );
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((cs) => {
      for (const c of cs) {
        if ("focus" in c) return c.focus();
      }
      if (self.clients.openWindow) return self.clients.openWindow("/");
    }),
  );
});
