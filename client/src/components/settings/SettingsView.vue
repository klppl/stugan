<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from "vue";
import AppearanceSettings from "./AppearanceSettings.vue";
import ChatBehaviorSettings from "./ChatBehaviorSettings.vue";
import HighlightSettings from "./HighlightSettings.vue";
import AliasSettings from "./AliasSettings.vue";
import PluginSettings from "./PluginSettings.vue";
import UploadSettings from "./UploadSettings.vue";
import BackupSettings from "./BackupSettings.vue";
import NotificationAccountSettings from "./NotificationAccountSettings.vue";
import { connection } from "../../connection";
import Icon, { type IconName } from "../Icon.vue";

const emit = defineEmits<{ close: [] }>();

export type SettingTabId =
  | "appearance"
  | "chat"
  | "highlights"
  | "aliases"
  | "plugins"
  | "uploads"
  | "backup"
  | "account";

interface SettingCategory {
  id: SettingTabId;
  label: string;
  icon: IconName;
  desc: string;
  component: unknown;
  cap?: string;
}

interface SettingSearchItem {
  tab: SettingTabId;
  label: string;
  desc: string;
  keywords?: string;
}

const categories: SettingCategory[] = [
  {
    id: "appearance",
    label: "Appearance",
    icon: "palette",
    desc: "Themes, custom CSS, text size",
    component: AppearanceSettings,
  },
  {
    id: "chat",
    label: "Chat & Formatting",
    icon: "message",
    desc: "Events, previews, nicks, reactions",
    component: ChatBehaviorSettings,
  },
  {
    id: "highlights",
    label: "Highlights",
    icon: "bell",
    desc: "Keyword & exception regexes",
    component: HighlightSettings,
  },
  {
    id: "aliases",
    label: "Aliases",
    icon: "zap",
    desc: "Slash command shortcuts",
    component: AliasSettings,
  },
  {
    id: "plugins",
    label: "Plugins",
    icon: "plug",
    desc: "Lua scripts & extension settings",
    component: PluginSettings,
    cap: "plugins",
  },
  {
    id: "uploads",
    label: "Uploads",
    icon: "folder",
    desc: "Stored file listing & expiry",
    component: UploadSettings,
    cap: "uploads",
  },
  {
    id: "backup",
    label: "Backup & Data",
    icon: "database",
    desc: "Export & import portable backups",
    component: BackupSettings,
  },
  {
    id: "account",
    label: "Account & Notifications",
    icon: "user",
    desc: "WebPush notifications, user session",
    component: NotificationAccountSettings,
  },
];

const activeTab = ref<SettingTabId>("appearance");
const searchQuery = ref("");

const settingSearchItems: SettingSearchItem[] = [
  { tab: "appearance", label: "Theme", desc: "Choose your preferred visual theme", keywords: "colors dark light" },
  { tab: "appearance", label: "Install Custom Theme", desc: "Create or paste CSS theme variables", keywords: "preset custom css remove" },
  { tab: "appearance", label: "Text Size", desc: "Change the font size for messages and buffers", keywords: "font" },
  { tab: "appearance", label: "Show Top Bar", desc: "Show or hide the bar above the chat (desktop)", keywords: "header toolbar search mentions channels hide minimal" },
  { tab: "chat", label: "Fold Join / Part Events", desc: "Group join, part, and quit messages", keywords: "collapse events" },
  { tab: "chat", label: "Expand Link Previews", desc: "Show rich previews for HTTP links", keywords: "url cards" },
  { tab: "chat", label: "Colored Nicks", desc: "Assign consistent colors to user nicknames", keywords: "names users" },
  { tab: "chat", label: "Reactions", desc: "Add and view emoji reactions on messages", keywords: "emoji ircv3" },
  { tab: "chat", label: "Send Typing Notifications", desc: "Broadcast a typing indicator while you type", keywords: "status" },
  { tab: "chat", label: "Show Others' Typing", desc: "Display typing indicators from other users", keywords: "status" },
  { tab: "chat", label: "What You Missed on Return", desc: "Open the catch-up summary after being away", keywords: "digest away unread mentions" },
  { tab: "highlights", label: "Highlight Keywords", desc: "Regular expressions that trigger highlights and notifications", keywords: "mentions regex patterns" },
  { tab: "highlights", label: "Highlight Exceptions", desc: "Patterns that prevent a highlight", keywords: "ignore regex exclusions" },
  { tab: "aliases", label: "Alias Rules", desc: "Configure slash-command shortcuts", keywords: "commands expansion" },
  { tab: "plugins", label: "Installed Plugins", desc: "Load, unload, configure, update, or uninstall Lua scripts", keywords: "extensions scripts" },
  { tab: "plugins", label: "Curated Plugin Library", desc: "Browse and install official plugins", keywords: "extensions scripts" },
  { tab: "plugins", label: "Import Plugin from URL", desc: "Download a Lua script from a remote URL", keywords: "extensions scripts remote" },
  { tab: "uploads", label: "Stored Uploads", desc: "View uploaded files and retention expiry", keywords: "media storage files" },
  { tab: "backup", label: "Export Backup", desc: "Download portable archive of database and plugins", keywords: "backup export database download tar zip" },
  { tab: "backup", label: "Import / Restore Backup", desc: "Restore database and plugins from archive", keywords: "restore import upload database" },
  { tab: "account", label: "Desktop & Push Notifications", desc: "Receive notifications for mentions and highlights", keywords: "webpush browser alerts" },
  { tab: "account", label: "User Session", desc: "View authentication status or log out", keywords: "account login sign out" },
];

const availableCategories = computed(() =>
  categories.filter((cat) => !cat.cap || connection.hasCap(cat.cap))
);

const searchResults = computed(() => {
  const q = searchQuery.value.trim().toLowerCase();
  if (!q) return [];
  const available = new Set(availableCategories.value.map((cat) => cat.id));
  const declaredPluginSettings: SettingSearchItem[] = connection.store.plugins.flatMap((plugin) =>
    (plugin.settings ?? []).map((setting) => ({
      tab: "plugins" as const,
      label: setting.label || setting.name,
      desc: setting.help || `Configure the ${plugin.name} plugin`,
      keywords: `${plugin.name} ${setting.name} plugin`,
    }))
  );

  return [...settingSearchItems, ...declaredPluginSettings]
    .filter((item) => available.has(item.tab))
    .filter((item) => {
      const category = categories.find((cat) => cat.id === item.tab);
      return `${item.label} ${item.desc} ${item.keywords ?? ""} ${category?.label ?? ""}`.toLowerCase().includes(q);
    });
});

const currentCategory = computed(
  () =>
    availableCategories.value.find((c) => c.id === activeTab.value) ??
    availableCategories.value[0]
);

function closeSettings() {
  connection.store.view = "chat";
  emit("close");
}

function selectSearchResult(result: SettingSearchItem) {
  activeTab.value = result.tab;
  searchQuery.value = "";
}

function openFirstSearchResult() {
  if (searchResults.value[0]) selectSearchResult(searchResults.value[0]);
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === "Escape") {
    closeSettings();
  }
}

onMounted(() => window.addEventListener("keydown", onKeydown));
onUnmounted(() => window.removeEventListener("keydown", onKeydown));
</script>

<template>
  <div class="settings-view">
    <!-- Header bar -->
    <header class="settings-header">
      <div class="settings-header-left">
        <button
          class="btn back-btn"
          aria-label="Back to Chat"
          title="Back to Chat (Esc)"
          @click="closeSettings"
        >
          <Icon name="arrow-left" class="back-arrow" />
          <span class="back-text">Back to Chat</span>
        </button>
        <h2 class="settings-title">Settings</h2>
      </div>

      <div class="settings-header-center">
        <input
          v-model="searchQuery"
          type="search"
          class="settings-search-input"
          placeholder="Search all settings…"
          aria-label="Search all settings"
          @keydown.enter.prevent="openFirstSearchResult"
        />
      </div>

      <div class="settings-header-right">
        <span class="esc-badge" title="Press Escape to exit settings">Esc</span>
      </div>
    </header>

    <!-- Main settings split body -->
    <div class="settings-body">
      <!-- Left sidebar navigation -->
      <nav class="settings-sidebar" :class="{ 'search-active': searchQuery.trim() }" aria-label="Settings categories">
        <template v-if="!searchQuery.trim()">
          <button
            v-for="cat in availableCategories"
            :key="cat.id"
            class="settings-nav-item"
            :class="{ active: activeTab === cat.id }"
            @click="activeTab = cat.id"
          >
            <Icon :name="cat.icon" :size="18" class="nav-item-icon" />
            <div class="nav-item-text">
              <span class="nav-item-label">{{ cat.label }}</span>
              <span class="nav-item-desc">{{ cat.desc }}</span>
            </div>
          </button>
        </template>

        <template v-else>
          <p class="settings-results-label">Settings results</p>
          <button
            v-for="(result, index) in searchResults"
            :key="`${result.tab}-${result.label}-${index}`"
            class="settings-nav-item settings-search-result"
            @click="selectSearchResult(result)"
          >
            <Icon :name="categories.find((cat) => cat.id === result.tab)?.icon ?? 'settings'" :size="18" class="nav-item-icon" />
            <div class="nav-item-text">
              <span class="nav-item-label">{{ result.label }}</span>
              <span class="nav-item-desc">{{ result.desc }}</span>
              <span class="search-result-category">{{ categories.find((cat) => cat.id === result.tab)?.label }}</span>
            </div>
          </button>

          <p v-if="searchResults.length === 0" class="no-cat-hint">
            No settings match "{{ searchQuery }}"
          </p>
        </template>
      </nav>

      <!-- Main sub-page view -->
      <main class="settings-content">
        <component :is="currentCategory.component" v-if="currentCategory" />
      </main>
    </div>
  </div>
</template>
