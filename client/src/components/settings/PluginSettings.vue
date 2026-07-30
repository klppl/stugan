<script setup lang="ts">
import { ref, computed, onMounted } from "vue";
import { connection } from "../../connection";
import type { PluginInfo, CuratedPluginInfo, PluginSetting } from "../../proto/events";

const hasPlugins = connection.hasCap("plugins");
const plugins = computed<PluginInfo[]>(() => connection.store.plugins);
const curated = computed<CuratedPluginInfo[]>(() => connection.store.curatedPlugins);

const activeTab = ref<"installed" | "curated" | "import">("installed");
const searchFilter = ref("");
const isCheckingUpdates = ref(false);

const importUrl = ref("");
const importName = ref("");
const importStatus = ref<{ type: "success" | "error"; msg: string } | null>(null);

onMounted(() => {
  if (hasPlugins) {
    connection.listPlugins();
  }
});

const filteredPlugins = computed(() => {
  if (!searchFilter.value.trim()) return plugins.value;
  const q = searchFilter.value.toLowerCase();
  return plugins.value.filter(
    (p) => p.name.toLowerCase().includes(q) || (p.description && p.description.toLowerCase().includes(q))
  );
});

const filteredCurated = computed(() => {
  if (!searchFilter.value.trim()) return curated.value;
  const q = searchFilter.value.toLowerCase();
  return curated.value.filter(
    (c) => c.name.toLowerCase().includes(q) || c.description.toLowerCase().includes(q)
  );
});

const updatesCount = computed(() => {
  return plugins.value.filter((p) => p.update_available).length;
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

function handleCheckUpdates() {
  isCheckingUpdates.value = true;
  connection.checkPluginUpdates();
  setTimeout(() => {
    isCheckingUpdates.value = false;
  }, 2000);
}

function handleImport() {
  if (!importUrl.value.trim()) {
    importStatus.value = { type: "error", msg: "Please enter a valid script URL." };
    return;
  }
  importStatus.value = null;
  connection.importPlugin(importUrl.value.trim(), importName.value.trim());
  importUrl.value = "";
  importName.value = "";
  activeTab.value = "installed";
}

function handleUpdate(name: string) {
  connection.updatePlugin(name);
}

function handleInstallCurated(name: string) {
  connection.pluginAction(name, "load");
}
</script>

<template>
  <div class="settings-section">
    <div class="section-header-row">
      <div class="section-header">
        <h3 class="section-title">🔌 Lua Plugins</h3>
        <p class="section-desc">Manage Lua scripts, official curated extensions, and remote script imports.</p>
      </div>

      <div v-if="hasPlugins" class="header-actions">
        <button
          class="btn btn-sm btn-outline"
          :disabled="isCheckingUpdates"
          @click="handleCheckUpdates"
          title="Check remote sources for script updates"
        >
          <span v-if="isCheckingUpdates" class="spin">🔄</span>
          <span v-else>🔍</span>
          {{ isCheckingUpdates ? "Checking..." : "Check for Updates" }}
        </button>
      </div>
    </div>

    <div v-if="!hasPlugins" class="settings-card">
      <p class="hint">The connected server does not advertise plugin management capabilities.</p>
    </div>

    <div v-else class="plugin-manager">
      <!-- Navigation Tabs & Search -->
      <div class="plugin-nav-bar">
        <div class="plugin-tabs">
          <button
            class="plugin-tab"
            :class="{ active: activeTab === 'installed' }"
            @click="activeTab = 'installed'"
          >
            Installed Scripts ({{ plugins.length }})
            <span v-if="updatesCount > 0" class="tab-update-badge">{{ updatesCount }} update{{ updatesCount > 1 ? 's' : '' }}</span>
          </button>
          <button
            class="plugin-tab"
            :class="{ active: activeTab === 'curated' }"
            @click="activeTab = 'curated'"
          >
            Curated Library ({{ curated.length }})
          </button>
          <button
            class="plugin-tab"
            :class="{ active: activeTab === 'import' }"
            @click="activeTab = 'import'"
          >
            📥 Import URL
          </button>
        </div>

        <div v-if="activeTab !== 'import'" class="plugin-search">
          <input
            v-model="searchFilter"
            type="text"
            placeholder="Search scripts..."
            class="plugin-search-input"
          />
        </div>
      </div>

      <!-- Tab 1: Installed Scripts -->
      <div v-if="activeTab === 'installed'" class="settings-card">
        <p v-if="!filteredPlugins.length" class="hint">
          {{ searchFilter ? "No matching plugins found." : "No plugins found in your scripts/ directory." }}
        </p>

        <div v-for="p in filteredPlugins" :key="p.name" class="plugin-card" :class="{ 'has-update': p.update_available }">
          <div class="plugin-head">
            <div class="plugin-title">
              <span class="plugin-name">{{ p.name }}</span>
              
              <!-- Source Category Badge -->
              <span v-if="p.source_type === 'curated'" class="plugin-badge category-curated" title="Official curated plugin">curated</span>
              <span v-else-if="p.source_type === 'remote'" class="plugin-badge category-remote" title="Imported from URL">remote</span>
              <span v-else class="plugin-badge category-manual" title="Local file in scripts/ directory">manual</span>

              <!-- Status Badge -->
              <span v-if="p.disabled" class="plugin-badge disabled" title="auto-disabled after repeated errors">disabled</span>
              <span v-else-if="p.loaded" class="plugin-badge on">loaded</span>
              <span v-else class="plugin-badge off">off</span>

              <!-- Update Available Badge -->
              <span v-if="p.update_available" class="plugin-badge update-badge">⚡ Update available</span>
            </div>

            <div class="plugin-actions">
              <button
                v-if="p.update_available || (p.source_url && p.source_type !== 'manual')"
                class="btn btn-sm btn-accent-sm"
                @click="handleUpdate(p.name)"
                title="Re-download latest script version from source URL"
              >
                Update
              </button>
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
          <p v-if="p.source_url" class="plugin-source-url">
            <span class="url-label">Source:</span>
            <a :href="p.source_url" target="_blank" rel="noopener noreferrer" class="url-link">{{ p.source_url }}</a>
          </p>

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

      <!-- Tab 2: Curated Library -->
      <div v-else-if="activeTab === 'curated'" class="settings-card">
        <p v-if="!filteredCurated.length" class="hint">No matching curated plugins found.</p>

        <div class="curated-grid">
          <div v-for="c in filteredCurated" :key="c.name" class="curated-card" :class="{ installed: c.installed }">
            <div class="curated-head">
              <div class="curated-title">
                <span class="plugin-name">{{ c.name }}</span>
                <span v-if="c.installed && c.loaded" class="plugin-badge on">loaded</span>
                <span v-else-if="c.installed" class="plugin-badge off">installed</span>
                <span v-else class="plugin-badge category-curated">official</span>
                <span v-if="c.update_available" class="plugin-badge update-badge">⚡ Update</span>
              </div>
            </div>

            <p class="curated-desc">{{ c.description }}</p>

            <div class="curated-footer">
              <a :href="c.source_url" target="_blank" rel="noopener noreferrer" class="url-link font-sm">View Source</a>
              <button
                v-if="!c.installed"
                class="btn btn-sm btn-primary"
                @click="handleInstallCurated(c.name)"
              >
                Install & Load
              </button>
              <button
                v-else-if="c.update_available"
                class="btn btn-sm btn-accent-sm"
                @click="handleUpdate(c.name)"
              >
                Update
              </button>
              <button
                v-else-if="!c.loaded"
                class="btn btn-sm btn-ghost"
                @click="handleInstallCurated(c.name)"
              >
                Load
              </button>
              <span v-else class="installed-tag">Installed</span>
            </div>
          </div>
        </div>
      </div>

      <!-- Tab 3: Import Script from URL -->
      <div v-else-if="activeTab === 'import'" class="settings-card">
        <div class="import-panel">
          <h4>Import Lua Script from Remote URL</h4>
          <p class="hint">
            Download and load a script file directly from GitHub, Gist, or any raw HTTP/HTTPS URL. The script will be saved to your <code>scripts/</code> directory and monitored for updates.
          </p>

          <div v-if="importStatus" class="import-alert" :class="importStatus.type">
            {{ importStatus.msg }}
          </div>

          <div class="form-group">
            <label for="import-url-input" class="form-label">Script Raw URL</label>
            <input
              id="import-url-input"
              v-model="importUrl"
              type="text"
              class="setting-text-input"
              placeholder="https://raw.githubusercontent.com/user/repository/main/my_plugin.lua"
              @keyup.enter="handleImport"
            />
          </div>

          <div class="form-group">
            <label for="import-name-input" class="form-label">
              <span>Custom Name (Optional)</span>
              <span class="setting-hint">Leave blank to use the filename from the URL</span>
            </label>
            <input
              id="import-name-input"
              v-model="importName"
              type="text"
              class="setting-text-input"
              placeholder="e.g. my_plugin"
              @keyup.enter="handleImport"
            />
          </div>

          <div class="import-actions">
            <button class="btn btn-primary" @click="handleImport">
              Download, Save & Load
            </button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.section-header-row {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 16px;
}

.header-actions {
  flex-shrink: 0;
}

.plugin-manager {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.plugin-nav-bar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  border-bottom: 1px solid var(--border);
  padding-bottom: 8px;
}

.plugin-tabs {
  display: flex;
  gap: 6px;
}

.plugin-tab {
  background: transparent;
  border: 1px solid transparent;
  color: var(--fg-dim);
  padding: 6px 12px;
  border-radius: 6px;
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  display: flex;
  align-items: center;
  gap: 6px;
  transition: all 0.15s ease;
}

.plugin-tab:hover {
  color: var(--fg);
  background: var(--bg-alt);
}

.plugin-tab.active {
  color: var(--fg);
  background: var(--bg-alt);
  border-color: var(--border);
  font-weight: 600;
}

.tab-update-badge {
  background: #f59e0b;
  color: #000;
  font-size: 10px;
  font-weight: 700;
  padding: 1px 5px;
  border-radius: 10px;
}

.plugin-search-input {
  background: var(--bg);
  border: 1px solid var(--border);
  color: var(--fg);
  padding: 5px 10px;
  border-radius: 6px;
  font-size: 13px;
  width: 180px;
}

.plugin-card {
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 12px;
  margin-bottom: 12px;
  background: var(--bg);
  transition: border-color 0.2s ease;
}

.plugin-card.has-update {
  border-color: #f59e0b;
}

.category-curated {
  background: color-mix(in srgb, #3b82f6 20%, transparent);
  color: #60a5fa;
}

.category-remote {
  background: color-mix(in srgb, #8b5cf6 20%, transparent);
  color: #c084fc;
}

.category-manual {
  background: color-mix(in srgb, var(--fg-dim) 15%, transparent);
  color: var(--fg-dim);
}

.update-badge {
  background: #f59e0b;
  color: #000;
  font-weight: 700;
}

.btn-accent-sm {
  background: #f59e0b;
  color: #000;
  font-weight: 600;
  border: none;
}

.btn-accent-sm:hover {
  background: #d97706;
}

.plugin-source-url {
  font-size: 12px;
  color: var(--fg-dim);
  margin-top: 4px;
}

.url-link {
  color: var(--accent);
  text-decoration: none;
  word-break: break-all;
}

.url-link:hover {
  text-decoration: underline;
}

.font-sm {
  font-size: 12px;
}

.curated-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 12px;
}

.curated-card {
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 12px;
  background: var(--bg);
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  gap: 8px;
}

.curated-card.installed {
  border-color: color-mix(in srgb, var(--accent) 40%, var(--border));
}

.curated-desc {
  font-size: 13px;
  color: var(--fg-dim);
  line-height: 1.4;
}

.curated-footer {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid color-mix(in srgb, var(--border) 50%, transparent);
}

.installed-tag {
  font-size: 12px;
  color: #10b981;
  font-weight: 600;
}

.import-panel {
  display: flex;
  flex-direction: column;
  gap: 12px;
  max-width: 600px;
}

.import-panel h4 {
  margin: 0;
  font-size: 16px;
}

.import-actions {
  margin-top: 8px;
}

.import-alert {
  padding: 8px 12px;
  border-radius: 6px;
  font-size: 13px;
}

.import-alert.error {
  background: color-mix(in srgb, #ef4444 20%, transparent);
  color: #f87171;
  border: 1px solid #ef4444;
}

.spin {
  display: inline-block;
  animation: spin 1s linear infinite;
}

@keyframes spin {
  100% {
    transform: rotate(360deg);
  }
}
</style>
