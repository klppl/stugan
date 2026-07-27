<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from "vue";
import AppearanceSettings from "./AppearanceSettings.vue";
import ChatBehaviorSettings from "./ChatBehaviorSettings.vue";
import HighlightSettings from "./HighlightSettings.vue";
import AliasSettings from "./AliasSettings.vue";
import PluginSettings from "./PluginSettings.vue";
import UploadSettings from "./UploadSettings.vue";
import NotificationAccountSettings from "./NotificationAccountSettings.vue";
import { connection } from "../../connection";

const emit = defineEmits<{ close: [] }>();

export type SettingTabId =
  | "appearance"
  | "chat"
  | "highlights"
  | "aliases"
  | "plugins"
  | "uploads"
  | "account";

interface SettingCategory {
  id: SettingTabId;
  label: string;
  icon: string;
  desc: string;
  component: unknown;
  cap?: string;
}

const categories: SettingCategory[] = [
  {
    id: "appearance",
    label: "Appearance",
    icon: "🎨",
    desc: "Themes, custom CSS, text size",
    component: AppearanceSettings,
  },
  {
    id: "chat",
    label: "Chat & Formatting",
    icon: "💬",
    desc: "Events, previews, nicks, reactions",
    component: ChatBehaviorSettings,
  },
  {
    id: "highlights",
    label: "Highlights",
    icon: "🔔",
    desc: "Keyword & exception regexes",
    component: HighlightSettings,
  },
  {
    id: "aliases",
    label: "Aliases",
    icon: "⚡",
    desc: "Slash command shortcuts",
    component: AliasSettings,
  },
  {
    id: "plugins",
    label: "Plugins",
    icon: "🔌",
    desc: "Lua scripts & extension settings",
    component: PluginSettings,
    cap: "plugins",
  },
  {
    id: "uploads",
    label: "Uploads",
    icon: "📁",
    desc: "Stored file listing & expiry",
    component: UploadSettings,
    cap: "uploads",
  },
  {
    id: "account",
    label: "Account & Notifications",
    icon: "👤",
    desc: "WebPush notifications, user session",
    component: NotificationAccountSettings,
  },
];

const activeTab = ref<SettingTabId>("appearance");
const searchQuery = ref("");

const availableCategories = computed(() =>
  categories.filter((cat) => !cat.cap || connection.hasCap(cat.cap))
);

const filteredCategories = computed(() => {
  const q = searchQuery.value.trim().toLowerCase();
  if (!q) return availableCategories.value;
  return availableCategories.value.filter(
    (cat) =>
      cat.label.toLowerCase().includes(q) ||
      cat.desc.toLowerCase().includes(q) ||
      cat.id.toLowerCase().includes(q)
  );
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
          class="btn btn-ghost back-btn"
          aria-label="Back to Chat"
          title="Back to Chat (Esc)"
          @click="closeSettings"
        >
          ← <span class="back-text">Back to Chat</span>
        </button>
        <h2 class="settings-title">Settings</h2>
      </div>

      <div class="settings-header-center">
        <input
          v-model="searchQuery"
          type="search"
          class="settings-search-input"
          placeholder="Filter categories…"
        />
      </div>

      <div class="settings-header-right">
        <span class="esc-badge" title="Press Escape to exit settings">Esc</span>
        <button class="btn btn-ghost close-icon-btn" aria-label="Close settings" @click="closeSettings">✕</button>
      </div>
    </header>

    <!-- Main settings split body -->
    <div class="settings-body">
      <!-- Left sidebar navigation -->
      <nav class="settings-sidebar" aria-label="Settings categories">
        <button
          v-for="cat in filteredCategories"
          :key="cat.id"
          class="settings-nav-item"
          :class="{ active: activeTab === cat.id }"
          @click="activeTab = cat.id"
        >
          <span class="nav-item-icon" aria-hidden="true">{{ cat.icon }}</span>
          <div class="nav-item-text">
            <span class="nav-item-label">{{ cat.label }}</span>
            <span class="nav-item-desc">{{ cat.desc }}</span>
          </div>
        </button>

        <p v-if="filteredCategories.length === 0" class="no-cat-hint">
          No categories match "{{ searchQuery }}"
        </p>
      </nav>

      <!-- Main sub-page view -->
      <main class="settings-content">
        <component :is="currentCategory.component" v-if="currentCategory" />
      </main>
    </div>
  </div>
</template>
