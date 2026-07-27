<script setup lang="ts">
import { ref, computed, onMounted } from "vue";
import { connection } from "../../connection";
import type { PluginInfo, PluginSetting } from "../../proto/events";

const hasPlugins = connection.hasCap("plugins");
const plugins = computed(() => connection.store.plugins);

onMounted(() => {
  if (hasPlugins) connection.listPlugins();
});

function summary(p: PluginInfo): string {
  if (p.description) return p.description;
  if (!p.loaded) return "not loaded";
  const parts: string[] = [];
  if (p.commands?.length) parts.push(p.commands.map((c) => "/" + c).join(" "));
  if (p.hooks) parts.push(`${p.hooks} hook${p.hooks === 1 ? "" : "s"}`);
  return parts.join(" · ") || "no commands or hooks";
}

const openPlugin = ref<string | null>(null);

function setSetting(p: PluginInfo, st: PluginSetting, value: string) {
  if (st.secret && value === "") return;
  connection.setPluginSetting(p.name, st.name, value);
}
</script>

<template>
  <div class="settings-section">
    <div class="section-header">
      <h3 class="section-title">🔌 Lua Plugins</h3>
      <p class="section-desc">Manage installed Lua scripts, hot-reload plugins, and configure script settings.</p>
    </div>

    <div v-if="!hasPlugins" class="settings-card">
      <p class="hint">The connected server does not advertise plugin management capabilities.</p>
    </div>

    <div v-else class="settings-card">
      <p v-if="!plugins.length" class="hint">No plugins found in your <code>scripts/</code> directory.</p>
      
      <div v-for="p in plugins" :key="p.name" class="plugin-card">
        <div class="plugin-head">
          <div class="plugin-title">
            <span class="plugin-name">{{ p.name }}</span>
            <span v-if="p.disabled" class="plugin-badge disabled" title="auto-disabled after repeated errors">disabled</span>
            <span v-else-if="p.loaded" class="plugin-badge on">loaded</span>
            <span v-else class="plugin-badge off">off</span>
          </div>

          <div class="plugin-actions">
            <button
              v-if="p.loaded && p.settings?.length"
              class="btn btn-sm btn-ghost"
              @click="openPlugin = openPlugin === p.name ? null : p.name"
            >
              {{ openPlugin === p.name ? "Close Config" : "Configure" }}
            </button>
            <button v-if="p.loaded" class="btn btn-sm btn-ghost" @click="connection.pluginAction(p.name, 'reload')">Reload</button>
            <button v-if="p.loaded" class="btn btn-sm btn-ghost btn-danger-ghost" @click="connection.pluginAction(p.name, 'unload')">Unload</button>
            <button v-else class="btn btn-sm btn-primary" @click="connection.pluginAction(p.name, 'load')">Load</button>
          </div>
        </div>

        <p class="plugin-desc">{{ summary(p) }}</p>

        <div v-if="openPlugin === p.name && p.settings?.length" class="plugin-settings-panel">
          <div v-for="st in p.settings" :key="st.name" class="form-group">
            <label :for="`set-${p.name}-${st.name}`" class="form-label">
              <span>{{ st.label || st.name }}</span>
              <span v-if="st.help" class="setting-hint">{{ st.help }}</span>
            </label>
            <select
              v-if="st.type === 'select'"
              :id="`set-${p.name}-${st.name}`"
              class="setting-input"
              :value="st.value"
              @change="setSetting(p, st, ($event.target as HTMLSelectElement).value)"
            >
              <option v-for="opt in st.options" :key="opt" :value="opt">{{ opt }}</option>
            </select>
            <input
              v-else
              :id="`set-${p.name}-${st.name}`"
              class="setting-text-input"
              :type="st.secret ? 'password' : st.type === 'number' ? 'number' : 'text'"
              :value="st.value"
              :placeholder="st.secret ? 'unchanged' : (st.default ?? '')"
              @change="setSetting(p, st, ($event.target as HTMLInputElement).value)"
            />
          </div>
        </div>
      </div>

      <p class="hint footer-hint">
        Scripts live in your <code>scripts/</code> directory. Plugins can declare metadata with <code>stugan.describe("…")</code> and settings with <code>stugan.setting(…)</code>.
      </p>
    </div>
  </div>
</template>
