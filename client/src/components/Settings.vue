<script setup lang="ts">
import { ref, computed, onMounted, watch } from "vue";
import { settings, themeNames, installTheme, uninstallTheme, TEMPLATE } from "../settings";
import { connection } from "../connection";
import type { PluginInfo, PluginSetting } from "../proto/events";
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

// Which plugin's settings form is expanded (one at a time). Settings are
// declared by the script via stugan.setting() and arrive on each PluginInfo.
const openPlugin = ref<string | null>(null);
function setSetting(p: PluginInfo, st: PluginSetting, value: string) {
  if (st.secret && value === "") return; // blank password field = leave unchanged
  connection.setPluginSetting(p.name, st.name, value);
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

// Highlight keywords: one regex per line. The server validates and persists
// them, then echoes the normalized rules back (which updates store.highlight).
const hlPatterns = ref(connection.store.highlight.patterns.join("\n"));
const hlExceptions = ref(connection.store.highlight.exceptions.join("\n"));
const hlSaved = ref(false);
let hlPending = false;

function saveHighlight() {
  const toList = (s: string) =>
    s
      .split("\n")
      .map((l) => l.trim())
      .filter(Boolean);
  hlPending = true;
  connection.setHighlight(toList(hlPatterns.value), toList(hlExceptions.value));
}

// Reflect the server's normalized echo back into the fields, and flash "Saved"
// — but only for a save we initiated (not an init/reconnect refresh).
watch(
  () => connection.store.highlight,
  (h) => {
    hlPatterns.value = h.patterns.join("\n");
    hlExceptions.value = h.exceptions.join("\n");
    if (hlPending) {
      hlPending = false;
      hlSaved.value = true;
      setTimeout(() => (hlSaved.value = false), 2000);
    }
  },
);

// Command aliases: one "name = expansion" per line. /name runs the expansion,
// with $1..$9, $* and $N- substituting the args. The server normalizes names
// and drops blank/invalid lines, then echoes the table back.
function formatAliases(m: Record<string, string>): string {
  return Object.keys(m)
    .sort()
    .map((k) => `${k} = ${m[k]}`)
    .join("\n");
}
const aliasText = ref(formatAliases(connection.store.aliases));
const aliasSaved = ref(false);
let aliasPending = false;

function saveAliases() {
  const map: Record<string, string> = {};
  for (const line of aliasText.value.split("\n")) {
    const eq = line.indexOf("=");
    if (eq < 0) continue;
    const name = line.slice(0, eq).trim();
    const expansion = line.slice(eq + 1).trim();
    if (name && expansion) map[name] = expansion;
  }
  aliasPending = true;
  connection.setAliases(map);
}

watch(
  () => connection.store.aliases,
  (m) => {
    aliasText.value = formatAliases(m);
    if (aliasPending) {
      aliasPending = false;
      aliasSaved.value = true;
      setTimeout(() => (aliasSaved.value = false), 2000);
    }
  },
);

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

      <label class="row">
        <span>Reactions</span>
        <input v-model="settings.reactions" type="checkbox" />
      </label>

      <label class="row">
        <span>Send typing notifications</span>
        <input v-model="settings.sendTyping" type="checkbox" />
      </label>
      <p class="hint">When on, others in the channel can see when you're typing.</p>

      <label class="row">
        <span>Show others' typing</span>
        <input v-model="settings.showTyping" type="checkbox" />
      </label>

      <div v-if="notifSupported" class="row">
        <span>Notifications</span>
        <button @click="enableNotifications">Enable</button>
      </div>
      <p v-if="pushMsg" class="hint">{{ pushMsg }}</p>
      <p class="hint">Mute a channel by right-clicking it in the sidebar.</p>

      <!-- Highlight keywords: extra words/patterns (beyond your nick) that mark
           a message as a highlight and fire a notification. One regex per line;
           the server validates them and rejects a bad pattern with an error. -->
      <h3 class="section">Highlights</h3>
      <p class="hint">
        Words that highlight you (in addition to your nick). One regex per line,
        case-insensitive.
      </p>
      <label class="hl-field">
        <span>Keywords</span>
        <textarea
          v-model="hlPatterns"
          rows="4"
          spellcheck="false"
          placeholder="release&#10;\bdeploy\b"
        />
      </label>
      <label class="hl-field">
        <span>Exceptions</span>
        <textarea
          v-model="hlExceptions"
          rows="3"
          spellcheck="false"
          placeholder="patterns here never highlight, even if a keyword matches"
        />
      </label>
      <div class="row">
        <span class="hint">{{ hlSaved ? "Saved ✓" : "" }}</span>
        <button @click="saveHighlight">Save highlights</button>
      </div>

      <!-- Command aliases: one "name = expansion" per line. Typing /name runs
           the expansion; $1..$9, $* and $N- substitute the arguments. -->
      <h3 class="section">Aliases</h3>
      <p class="hint">
        Slash-command shortcuts, one <code>name = /expansion</code> per line.
        Typing <code>/name</code> runs the expansion (start it with
        <code>/</code>); <code>$1</code>..<code>$9</code>, <code>$*</code> (all
        args) and <code>$2-</code> (from arg 2 on) fill in what you typed.
      </p>
      <label class="hl-field">
        <span>Aliases</span>
        <textarea
          v-model="aliasText"
          rows="4"
          spellcheck="false"
          placeholder="j = /join $*&#10;wii = /whois $1"
        />
      </label>
      <div class="row">
        <span class="hint">{{ aliasSaved ? "Saved ✓" : "" }}</span>
        <button @click="saveAliases">Save aliases</button>
      </div>

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
            <button
              v-if="p.loaded && p.settings?.length"
              class="link"
              @click="openPlugin = openPlugin === p.name ? null : p.name"
            >
              {{ openPlugin === p.name ? "close" : "configure" }}
            </button>
            <button v-if="p.loaded" class="link" @click="connection.pluginAction(p.name, 'reload')">reload</button>
            <button v-if="p.loaded" class="link" @click="connection.pluginAction(p.name, 'unload')">unload</button>
            <button v-else class="link" @click="connection.pluginAction(p.name, 'load')">load</button>
          </div>
          <p class="plugin-desc">{{ summary(p) }}</p>

          <!-- Per-plugin settings form, populated from stugan.setting()
               declarations. A change is sent immediately; the server replies
               with a refreshed list so values stay in sync. -->
          <div v-if="openPlugin === p.name && p.settings?.length" class="plugin-settings">
            <div v-for="st in p.settings" :key="st.name" class="plugin-setting">
              <label :for="`set-${p.name}-${st.name}`">{{ st.label || st.name }}</label>
              <select
                v-if="st.type === 'select'"
                :id="`set-${p.name}-${st.name}`"
                :value="st.value"
                @change="setSetting(p, st, ($event.target as HTMLSelectElement).value)"
              >
                <option v-for="opt in st.options" :key="opt" :value="opt">{{ opt }}</option>
              </select>
              <input
                v-else
                :id="`set-${p.name}-${st.name}`"
                :type="st.secret ? 'password' : st.type === 'number' ? 'number' : 'text'"
                :value="st.value"
                :placeholder="st.secret ? 'unchanged' : (st.default ?? '')"
                @change="setSetting(p, st, ($event.target as HTMLInputElement).value)"
              />
              <span v-if="st.help" class="setting-help">{{ st.help }}</span>
            </div>
          </div>
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
