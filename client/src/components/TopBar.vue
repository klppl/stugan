<script setup lang="ts">
import { computed, ref } from "vue";
import { connection } from "../connection";
import { ui, toggleSidebar, toggleMembers } from "../ui";
import ChannelBrowser from "./ChannelBrowser.vue";

const emit = defineEmits<{ settings: [] }>();
const store = connection.store;
const q = ref("");
const browseNet = ref<string | null>(null);
// On mobile we hide the search input behind a magnifier button; this toggles it.
const searchOpen = ref(false);

function browse() {
  if (store.active) browseNet.value = store.active.network;
}

const mentionCount = computed(() => store.mentions.length);

// Show the "people" toggle only when looking at a buffer that has members.
const hasMembers = computed(() => {
  const b = connection.activeBuffer();
  return !!b && b.members.length > 0;
});

// Map the raw WebSocket state to a friendly label.
const statusLabel = computed(
  () => ({ connecting: "connecting", open: "connected", closed: "disconnected" })[store.status],
);

function doSearch() {
  if (q.value.trim()) connection.search(q.value);
}
</script>

<template>
  <header class="topbar">
    <button
      class="ghost icon-btn menu-btn"
      aria-label="Menu"
      title="Channels"
      @click="toggleSidebar"
    >
      <span class="menu-icon" :class="{ open: ui.sidebarOpen }" aria-hidden="true">
        <span /><span /><span />
      </span>
    </button>

    <input
      v-if="connection.hasCap('search')"
      v-model="q"
      class="search"
      :class="{ 'mobile-open': searchOpen }"
      placeholder="Search messages…"
      @keydown.enter="doSearch"
      @blur="searchOpen = false"
    />
    <button
      v-if="connection.hasCap('search')"
      class="ghost icon-btn search-toggle"
      aria-label="Search"
      title="Search"
      @click="searchOpen = !searchOpen"
    >🔍</button>

    <button class="ghost" :class="{ active: store.view === 'mentions' }" @click="connection.showMentions()">
      <span class="btn-label">@ Mentions</span>
      <span class="btn-icon" aria-hidden="true">@</span>
      <span v-if="mentionCount" class="badge">{{ mentionCount }}</span>
    </button>
    <button v-if="store.active" class="ghost channels-btn" title="Browse channels" @click="browse">
      <span class="btn-label">⊞ Channels</span>
      <span class="btn-icon" aria-hidden="true">⊞</span>
    </button>
    <span class="spacer" />
    <span class="conn-pill" :class="store.status" :title="statusLabel">
      <span class="conn-label">{{ statusLabel }}</span>
      <span class="conn-dot" aria-hidden="true" />
    </span>
    <button
      v-if="hasMembers"
      class="ghost icon-btn members-btn"
      :class="{ active: ui.membersOpen }"
      aria-label="Members"
      title="Members"
      @click="toggleMembers"
    >👥</button>
    <button class="ghost icon-btn" aria-label="Settings" title="Settings" @click="emit('settings')">⚙</button>
    <ChannelBrowser v-if="browseNet" :network="browseNet" @close="browseNet = null" />
  </header>
</template>
