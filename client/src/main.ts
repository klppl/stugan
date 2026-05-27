import { createApp } from "vue";
import App from "./App.vue";
import { refresh } from "./auth";
import { registerServiceWorker } from "./pwa";
import "./settings"; // applies the saved theme on load
import "./style.css";

refresh(); // resolves /api/me, then connects the socket if allowed
registerServiceWorker();
createApp(App).mount("#app");
