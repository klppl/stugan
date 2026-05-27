<script setup lang="ts">
import { ref } from "vue";
import { settings, themeNames, installTheme, uninstallTheme, TEMPLATE } from "../settings";
import { connection } from "../connection";
import { enablePush } from "../pwa";
import { authState, logout } from "../auth";

const emit = defineEmits<{ close: [] }>();
const pushMsg = ref("");

const notifSupported = typeof Notification !== "undefined";

// Theme installer state.
const showInstall = ref(false);
const themeName = ref("");
const themeCss = ref(TEMPLATE);
const themeError = ref("");

function doInstall() {
  themeError.value = installTheme(themeName.value, themeCss.value) ?? "";
  if (!themeError.value) {
    showInstall.value = false;
    themeName.value = "";
    themeCss.value = TEMPLATE;
  }
}

async function enableNotifications() {
  pushMsg.value = "requesting…";
  const perm = await Notification.requestPermission();
  if (perm !== "granted") {
    pushMsg.value = "Not enabled (permission denied)";
    return;
  }
  if (connection.hasCap("push")) {
    const ok = await enablePush();
    pushMsg.value = ok ? "Notifications + push enabled ✓" : "Desktop notifications enabled (push failed)";
  } else {
    pushMsg.value = "Desktop notifications enabled ✓";
  }
}
</script>

<template>
  <div class="settings-overlay" @click.self="emit('close')">
    <div class="settings">
      <h2>Settings</h2>

      <label class="row">
        <span>Theme</span>
        <select v-model="settings.theme">
          <option v-for="t in themeNames()" :key="t" :value="t">{{ t }}</option>
        </select>
      </label>

      <!-- Installed custom themes -->
      <div v-for="t in settings.customThemes" :key="t.name" class="row theme-row">
        <span>· {{ t.name }}</span>
        <button class="link" @click="uninstallTheme(t.name)">remove</button>
      </div>

      <div class="row">
        <span></span>
        <button @click="showInstall = !showInstall">{{ showInstall ? "Cancel" : "Install theme…" }}</button>
      </div>

      <div v-if="showInstall" class="install-theme">
        <input v-model="themeName" placeholder="Theme name (e.g. Solarized)" />
        <textarea
          v-model="themeCss"
          rows="9"
          spellcheck="false"
          placeholder="Paste CSS variables, e.g. --bg: #002b36;"
        />
        <p class="hint">
          Paste <code>--var: value;</code> lines. Unset variables inherit the
          dark theme. Themes are stored in this browser.
        </p>
        <p v-if="themeError" class="login-error">{{ themeError }}</p>
        <button @click="doInstall">Install</button>
      </div>

      <div v-if="notifSupported" class="row">
        <span>Notifications</span>
        <button @click="enableNotifications">Enable</button>
      </div>
      <p v-if="pushMsg" class="hint">{{ pushMsg }}</p>
      <p class="hint">Mute a channel by right-clicking it in the sidebar.</p>

      <div v-if="authState.authEnabled" class="row">
        <span>Signed in as {{ authState.user }}</span>
        <button @click="logout">Log out</button>
      </div>

      <button class="close" @click="emit('close')">Close</button>
    </div>
  </div>
</template>
