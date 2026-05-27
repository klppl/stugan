import { createApp } from "vue";
import App from "./App.vue";
import { connection } from "./connection";
import { registerServiceWorker } from "./pwa";
import "./settings"; // applies the saved theme on load
import "./style.css";

connection.connect();
registerServiceWorker();
createApp(App).mount("#app");
