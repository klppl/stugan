<script setup lang="ts">
import { computed, nextTick, ref } from "vue";
import { connection } from "../connection";
import { ui, toggleSidebar, toggleMembers } from "../ui";
import { toggleMircTheme } from "../settings";
import ChannelBrowser from "./ChannelBrowser.vue";

const emit = defineEmits<{ settings: [] }>();
const store = connection.store;
const q = ref("");
const browseNet = ref<string | null>(null);
// On mobile we hide the search input behind a magnifier button; this toggles it.
const searchOpen = ref(false);
const searchEl = ref<HTMLInputElement | null>(null);

// Opening search focuses the field so the user can type straight away. The
// focus must wait a tick: the input is display:none until the mobile-open
// class binding flushes, and hidden elements can't take focus.
function toggleSearch() {
  searchOpen.value = !searchOpen.value;
  if (searchOpen.value) nextTick(() => searchEl.value?.focus());
}

// The topic row is hidden on phones (no room in the bar); tapping the channel
// name reveals it on its own wrapped line. Desktop always shows the topic
// inline, so the class this toggles only has effect inside the mobile
// media query.
const topicOpen = ref(false);

const buffer = computed(() => connection.activeBuffer());

function browse() {
  if (store.active) browseNet.value = store.active.network;
}

const mentionCount = computed(() => store.mentions.length);

// Show the "people" toggle only when looking at a buffer that has members.
const hasMembers = computed(() => {
  const b = buffer.value;
  return !!b && b.members.length > 0;
});

function doSearch() {
  if (q.value.trim()) connection.search(q.value);
}

// Channel name + topic are folded into the bar (chat view only — search and
// mentions render their own title inside ChatView). The topic is click-to-edit
// for channels, mirroring the old standalone chat-header row.
const showBufferHeader = computed(() => store.view === "chat" && !!buffer.value);

// The per-network status buffer ("*status") is folded into the network header
// in the sidebar, so showing its literal name here would read "*status". Show
// the network name instead, matching what the user clicked to get here.
const bufferTitle = computed(() => {
  const b = buffer.value;
  if (!b) return "";
  if (b.kind === "status") {
    return store.networks.find((n) => n.id === store.active?.network)?.name ?? b.name;
  }
  return b.name;
});
const editingTopic = ref(false);
const topicDraft = ref("");
const topicInput = ref<HTMLInputElement | null>(null);

function startEditTopic() {
  if (buffer.value?.kind !== "channel") return;
  topicDraft.value = buffer.value.topic;
  editingTopic.value = true;
  nextTick(() => topicInput.value?.focus());
}
function saveTopic() {
  if (store.active) connection.send(store.active.network, store.active.buffer, "/topic " + topicDraft.value.trim());
  editingTopic.value = false;
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

    <template v-if="showBufferHeader && buffer">
      <span class="buffer-name" @click="topicOpen = !topicOpen">
        {{ bufferTitle }}<span v-if="buffer.kind === 'channel'" class="topic-caret" aria-hidden="true">▾</span>
      </span>
      <input
        v-if="editingTopic"
        ref="topicInput"
        v-model="topicDraft"
        class="topic-edit"
        @keydown.enter="saveTopic"
        @keydown.esc="editingTopic = false"
        @blur="editingTopic = false"
      />
      <span
        v-else
        class="topic"
        :class="{ editable: buffer.kind === 'channel', 'mobile-open': topicOpen }"
        :title="buffer.kind === 'channel' ? 'click to edit topic' : ''"
        @click="startEditTopic"
      >{{ buffer.topic || (buffer.kind === "channel" ? "(set topic…)" : "") }}</span>
    </template>

    <span class="spacer" />

    <input
      v-if="connection.hasCap('search')"
      ref="searchEl"
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
      @click="toggleSearch"
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
    <button
      v-if="hasMembers"
      class="ghost icon-btn members-btn"
      :class="{ active: ui.membersOpen }"
      aria-label="Members"
      title="Members"
      @click="toggleMembers"
    >👥</button>
    <button class="ghost icon-btn" aria-label="Settings" title="Settings" @click="emit('settings')">⚙</button>

    <!-- mIRC easter-egg window controls. Hidden unless the mIRC theme is
         active (see style.css); the close box flips the theme back off. -->
    <span class="mirc-winctl" aria-hidden="true">
      <button class="mirc-wb" tabindex="-1">_</button>
      <button class="mirc-wb" tabindex="-1">☐</button>
      <button class="mirc-wb mirc-close" tabindex="-1" title="Close" @click="toggleMircTheme()">✕</button>
    </span>

    <ChannelBrowser v-if="browseNet" :network="browseNet" @close="browseNet = null" />
  </header>
</template>
