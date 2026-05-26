import { createApp } from "vue";
import App from "./App.vue";
import { connection } from "./connection";
import "./style.css";

connection.connect();
createApp(App).mount("#app");
