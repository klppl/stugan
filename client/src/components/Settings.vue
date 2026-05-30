<script setup lang="ts">
import { ref, computed, onMounted } from "vue";
import { settings, themeNames, installTheme, uninstallTheme, TEMPLATE } from "../settings";
import { connection } from "../connection";
import type { PluginInfo } from "../proto/events";
import { enablePush } from "../pwa";
import { authState, logout } from "../auth";

const emit = defineEmits<{ close: [] }>();
const pushMsg = ref("");

const notifSupported = typeof Notification !== "undefined";

// Plugins: the server exposes a manager when it advertises the "plugins" cap.
// Ask for the current list when the panel opens; the reply lands in
// connection.store.plugins, and every load/unload/reload refreshes it.
const hasPlugins = connection.hasCap("plugins");
const plugins = computed(() => connection.store.plugins);
onMounted(() => {
  if (hasPlugins) connection.listPlugins();
});

// summary is the "what it does" line: the script's own description if it set
// one via stugan.describe(), otherwise the commands/hooks it registered.
function summary(p: PluginInfo): string {
  if (p.description) return p.description;
  if (!p.loaded) return "not loaded";
  const parts: string[] = [];
  if (p.commands?.length) parts.push(p.commands.map((c) => "/" + c).join(" "));
  if (p.hooks) parts.push(`${p.hooks} hook${p.hooks === 1 ? "" : "s"}`);
  return parts.join(" · ") || "no commands or hooks";
}

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

      <label class="row">
        <span>Fold join/part</span>
        <input v-model="settings.foldEvents" type="checkbox" />
      </label>

      <label class="row">
        <span>Colored nicks</span>
        <input v-model="settings.coloredNicks" type="checkbox" />
      </label>

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

      <!-- Plugin manager: list loaded + available Lua scripts, with controls
           to load/unload/reload each without restarting the daemon. -->
      <template v-if="hasPlugins">
        <h3 class="section">Plugins</h3>
        <p v-if="!plugins.length" class="hint">No plugins found in the scripts directory.</p>
        <div v-for="p in plugins" :key="p.name" class="plugin">
          <div class="plugin-head">
            <span class="plugin-name">{{ p.name }}</span>
            <span v-if="p.disabled" class="plugin-badge disabled" title="auto-disabled after repeated errors">disabled</span>
            <span v-else-if="p.loaded" class="plugin-badge on">loaded</span>
            <span v-else class="plugin-badge off">off</span>
            <span class="spacer" />
            <button v-if="p.loaded" class="link" @click="connection.pluginAction(p.name, 'reload')">reload</button>
            <button v-if="p.loaded" class="link" @click="connection.pluginAction(p.name, 'unload')">unload</button>
            <button v-else class="link" @click="connection.pluginAction(p.name, 'load')">load</button>
          </div>
          <p class="plugin-desc">{{ summary(p) }}</p>
        </div>
        <p class="hint">
          Scripts live in your <code>scripts/</code> directory. A plugin can
          describe itself with <code>stugan.describe("…")</code>.
        </p>
      </template>

      <button class="close" @click="emit('close')">Close</button>
    </div>
  </div>
</template>
