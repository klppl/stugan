import { createApp } from "vue";
import App from "./App.vue";
import { refresh } from "./auth";
import { connection } from "./connection";
import { registerServiceWorker } from "./pwa";
import "./settings"; // applies the saved theme on load
import "./style.css";

refresh(); // resolves /api/me, then connects the socket if allowed
registerServiceWorker();

// A clicked push notification routes us to its buffer. Two paths: an already
// open tab gets a postMessage from the service worker; a cold start arrives as
// ?net=&buf= query params (see sw.js notificationclick).
if ("serviceWorker" in navigator) {
  navigator.serviceWorker.addEventListener("message", (e) => {
    const d = e.data;
    if (d && d.type === "navigate" && d.network && d.buffer) {
      connection.navigateTo(d.network, d.buffer);
    }
  });
}
const params = new URLSearchParams(location.search);
const deepNet = params.get("net");
const deepBuf = params.get("buf");
if (deepNet && deepBuf) {
  connection.navigateTo(deepNet, deepBuf);
  history.replaceState(null, "", location.pathname); // drop the params from the URL
}

createApp(App).mount("#app");
