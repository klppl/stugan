<script setup lang="ts">
import { computed, nextTick, ref, watch, onMounted, onUnmounted } from "vue";
import { connection } from "../connection";
import { ui, toggleSidebar, toggleMembers } from "../ui";
import { toggleMircTheme } from "../settings";
import ChannelBrowser from "./ChannelBrowser.vue";
import ChannelInspectorModal from "./ChannelInspectorModal.vue";

const emit = defineEmits<{ settings: [] }>();
const store = connection.store;
const q = ref("");
const browseNet = ref<string | null>(null);
const showInspector = ref(false);
// On mobile we hide the search input behind a magnifier button; this toggles it.
const searchOpen = ref(false);
const searchEl = ref<HTMLInputElement | null>(null);

// Keep search input in sync if query changes externally (e.g. clicking filter chips)
watch(
  () => store.search.query,
  (newQ) => {
    if (newQ !== undefined && newQ !== q.value) {
      q.value = newQ;
    }
  }
);

// Mobile action menu state (hamburger/overflow menu for right-side tools)
const mobileMenuOpen = ref(false);

// Search Autocomplete State
const showAutocomplete = ref(false);
const selectedIndex = ref(0);

interface SearchSuggestion {
  text: string;
  label: string;
  desc: string;
  type: "filter" | "nick" | "channel" | "date";
  isComplete: boolean;
}

const activeNet = computed(() => store.networks.find((n) => n.id === store.active?.network));

const availableChannels = computed(() => {
  if (!activeNet.value) return [];
  return activeNet.value.buffers
    .filter((b) => b.kind === "channel")
    .map((b) => b.name);
});

const availableNicks = computed(() => {
  const nicksSet = new Set<string>();
  if (buffer.value?.members) {
    for (const m of buffer.value.members) {
      if (m.nick) nicksSet.add(m.nick);
    }
  }
  if (activeNet.value) {
    for (const b of activeNet.value.buffers) {
      if (b.members) {
        for (const m of b.members) {
          if (m.nick) nicksSet.add(m.nick);
        }
      }
    }
  }
  return Array.from(nicksSet);
});

function getTodayString(): string {
  const d = new Date();
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

const suggestions = computed<SearchSuggestion[]>(() => {
  const text = q.value;
  const pos = searchEl.value?.selectionStart ?? text.length;
  const textBefore = text.slice(0, pos);
  const lastSpace = textBefore.lastIndexOf(" ");
  const tokenStart = lastSpace === -1 ? 0 : lastSpace + 1;
  const token = textBefore.slice(tokenStart).trim();

  const today = getTodayString();
  const results: SearchSuggestion[] = [];

  const baseFilters: SearchSuggestion[] = [
    { text: "from:", label: "from:", desc: "Filter by nick", type: "filter", isComplete: false },
    { text: "in:", label: "in:", desc: "Filter by channel", type: "filter", isComplete: false },
    { text: "has:link", label: "has:link", desc: "Only messages with links", type: "filter", isComplete: true },
    { text: `after:${today}`, label: "after:", desc: `After date (${today})`, type: "date", isComplete: true },
    { text: `before:${today}`, label: "before:", desc: `Before date (${today})`, type: "date", isComplete: true },
  ];

  if (!token) {
    return baseFilters;
  }

  const colonIdx = token.indexOf(":");
  if (colonIdx !== -1) {
    const key = token.slice(0, colonIdx).toLowerCase();
    const val = token.slice(colonIdx + 1).toLowerCase();

    if (["from", "by", "author"].includes(key)) {
      const matched = availableNicks.value.filter((n) => n.toLowerCase().includes(val));
      for (const nick of matched) {
        results.push({
          text: `from:${nick}`,
          label: `from:${nick}`,
          desc: `Messages from ${nick}`,
          type: "nick",
          isComplete: true,
        });
      }
      if (!results.length && val) {
        results.push({
          text: `from:${val}`,
          label: `from:${val}`,
          desc: `Filter by nick ${val}`,
          type: "nick",
          isComplete: true,
        });
      }
    } else if (["in", "chan", "channel", "buffer"].includes(key)) {
      const matched = availableChannels.value.filter((c) => c.toLowerCase().includes(val));
      for (const chan of matched) {
        results.push({
          text: `in:${chan}`,
          label: `in:${chan}`,
          desc: `Messages in ${chan}`,
          type: "channel",
          isComplete: true,
        });
      }
      if (!results.length && val) {
        const formattedChan = val.startsWith("#") ? val : `#${val}`;
        results.push({
          text: `in:${formattedChan}`,
          label: `in:${formattedChan}`,
          desc: `Filter by channel ${formattedChan}`,
          type: "channel",
          isComplete: true,
        });
      }
    } else if (key === "has") {
      if ("link".startsWith(val) || "url".startsWith(val)) {
        results.push({
          text: "has:link",
          label: "has:link",
          desc: "Messages with links",
          type: "filter",
          isComplete: true,
        });
      }
    } else if (["after", "since"].includes(key)) {
      results.push({
        text: `after:${val || today}`,
        label: `after:${val || today}`,
        desc: "Filter after date (YYYY-MM-DD)",
        type: "date",
        isComplete: true,
      });
    } else if (key === "before") {
      results.push({
        text: `before:${val || today}`,
        label: `before:${val || today}`,
        desc: "Filter before date (YYYY-MM-DD)",
        type: "date",
        isComplete: true,
      });
    }
  } else {
    const lowerToken = token.toLowerCase();
    for (const f of baseFilters) {
      if (f.text.toLowerCase().includes(lowerToken) || f.label.toLowerCase().includes(lowerToken)) {
        results.push(f);
      }
    }
  }

  return results;
});

function applySuggestion(s: SearchSuggestion) {
  const text = q.value;
  const pos = searchEl.value?.selectionStart ?? text.length;
  const textBefore = text.slice(0, pos);
  const textAfter = text.slice(pos);
  const lastSpace = textBefore.lastIndexOf(" ");
  const tokenStart = lastSpace === -1 ? 0 : lastSpace + 1;

  const replacement = s.text + (s.isComplete ? " " : "");
  const newText = text.slice(0, tokenStart) + replacement + textAfter.trimStart();
  const newPos = tokenStart + replacement.length;

  q.value = newText;
  showAutocomplete.value = false;

  nextTick(() => {
    if (searchEl.value) {
      searchEl.value.focus();
      searchEl.value.setSelectionRange(newPos, newPos);
    }
  });
}

function onSearchKeydown(e: KeyboardEvent) {
  if (!showAutocomplete.value || !suggestions.value.length) {
    if (e.key === "Enter") {
      doSearch();
    }
    return;
  }

  if (e.key === "ArrowDown") {
    e.preventDefault();
    selectedIndex.value = (selectedIndex.value + 1) % suggestions.value.length;
  } else if (e.key === "ArrowUp") {
    e.preventDefault();
    selectedIndex.value = (selectedIndex.value - 1 + suggestions.value.length) % suggestions.value.length;
  } else if (e.key === "Tab") {
    e.preventDefault();
    if (suggestions.value[selectedIndex.value]) {
      applySuggestion(suggestions.value[selectedIndex.value]);
    }
  } else if (e.key === "Enter") {
    e.preventDefault();
    if (suggestions.value[selectedIndex.value]) {
      applySuggestion(suggestions.value[selectedIndex.value]);
    } else {
      doSearch();
    }
  } else if (e.key === "Escape") {
    showAutocomplete.value = false;
  }
}

function onSearchBlur() {
  setTimeout(() => {
    showAutocomplete.value = false;
  }, 150);
}

function toggleSearch() {
  searchOpen.value = !searchOpen.value;
  if (searchOpen.value) {
    nextTick(() => {
      searchEl.value?.focus();
      showAutocomplete.value = true;
    });
  }
}

function handleMobileSearch() {
  mobileMenuOpen.value = false;
  toggleSearch();
}

function handleMobileMentions() {
  mobileMenuOpen.value = false;
  connection.showMentions();
}

function handleMobileBrowse() {
  mobileMenuOpen.value = false;
  browse();
}

function handleMobileMembers() {
  mobileMenuOpen.value = false;
  toggleMembers();
}

function handleMobileSettings() {
  mobileMenuOpen.value = false;
  emit("settings");
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === "Escape" && mobileMenuOpen.value) {
    mobileMenuOpen.value = false;
  }
}

onMounted(() => window.addEventListener("keydown", onKeydown));
onUnmounted(() => window.removeEventListener("keydown", onKeydown));

// The topic row is hidden on phones (no room in the bar); tapping the channel
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
  showAutocomplete.value = false;
  if (q.value.trim()) connection.search(q.value);
}

// Channel name + topic are folded into the bar (chat view only — search and
// mentions render their own title inside ChatView).
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
      <span class="buffer-name" title="Channel Info & Topic Details" @click="showInspector = true">
        {{ bufferTitle }}
      </span>
      <button
        v-if="store.active"
        class="ghost icon-btn inspector-btn"
        title="Channel Details / Inspector"
        aria-label="Channel Details"
        @click="showInspector = true"
      >ℹ</button>
      <span
        class="topic"
        :class="{ editable: buffer.kind === 'channel' }"
        title="Click for Channel Details"
        @click="showInspector = true"
      >{{ buffer.topic || (buffer.kind === "channel" ? "(set topic…)" : "") }}</span>
    </template>

    <span class="spacer" />

    <div v-if="connection.hasCap('search')" class="search-box-container" :class="{ 'mobile-open': searchOpen }">
      <input
        ref="searchEl"
        v-model="q"
        class="search"
        :class="{ 'mobile-open': searchOpen }"
        placeholder="Search messages…"
        @focus="showAutocomplete = true"
        @input="showAutocomplete = true; selectedIndex = 0"
        @keydown="onSearchKeydown"
        @blur="onSearchBlur"
      />
      <div
        v-if="showAutocomplete && suggestions.length"
        class="search-autocomplete"
        role="listbox"
      >
        <div
          v-for="(s, idx) in suggestions"
          :key="s.text"
          class="autocomplete-item"
          :class="{ selected: idx === selectedIndex }"
          role="option"
          :aria-selected="idx === selectedIndex"
          @mouseenter="selectedIndex = idx"
          @mousedown.prevent="applySuggestion(s)"
        >
          <span class="item-type-tag" :class="s.type">{{ s.type }}</span>
          <span class="item-text">{{ s.label }}</span>
          <span class="item-desc">{{ s.desc }}</span>
        </div>
      </div>
    </div>
    <button
      v-if="connection.hasCap('search')"
      class="ghost icon-btn search-toggle desktop-action"
      aria-label="Search"
      title="Search"
      @click="toggleSearch"
    >🔍</button>

    <button
      class="ghost mentions-btn desktop-action"
      :class="{ active: store.view === 'mentions' }"
      @click="connection.showMentions()"
    >
      <span class="btn-label">@ Mentions</span>
      <span class="btn-icon" aria-hidden="true">@</span>
      <span v-if="mentionCount" class="badge" :class="{ highlight: mentionCount > 0 }">{{ mentionCount }}</span>
    </button>
    <button
      v-if="store.active"
      class="ghost channels-btn desktop-action"
      title="Browse channels"
      @click="browse"
    >
      <span class="btn-label">⊞ Channels</span>
      <span class="btn-icon" aria-hidden="true">⊞</span>
    </button>
    <button
      v-if="hasMembers"
      class="ghost icon-btn members-btn desktop-action"
      :class="{ active: ui.membersOpen }"
      aria-label="Members"
      title="Members"
      @click="toggleMembers"
    >👥</button>
    <button
      class="ghost icon-btn settings-btn desktop-action"
      aria-label="Settings"
      title="Settings"
      @click="emit('settings')"
    >⚙</button>

    <div class="mobile-actions-wrapper">
      <button
        class="ghost icon-btn mobile-actions-toggle"
        :class="{ active: mobileMenuOpen }"
        aria-label="Actions Menu"
        title="Actions"
        @click="mobileMenuOpen = !mobileMenuOpen"
      >
        <span class="mobile-actions-icon" aria-hidden="true">⋮</span>
        <span v-if="mentionCount" class="badge dot-badge" />
      </button>

      <div
        v-if="mobileMenuOpen"
        class="mobile-actions-backdrop"
        @click="mobileMenuOpen = false"
      />

      <div
        v-if="mobileMenuOpen"
        class="mobile-actions-menu"
        role="menu"
      >
        <button
          v-if="connection.hasCap('search')"
          class="mobile-action-item"
          role="menuitem"
          @click="handleMobileSearch"
        >
          <span class="item-icon" aria-hidden="true">🔍</span>
          <span class="item-label">Search</span>
        </button>
        <button
          class="mobile-action-item"
          :class="{ active: store.view === 'mentions' }"
          role="menuitem"
          @click="handleMobileMentions"
        >
          <span class="item-icon" aria-hidden="true">@</span>
          <span class="item-label">Mentions</span>
          <span v-if="mentionCount" class="badge" :class="{ highlight: mentionCount > 0 }">{{ mentionCount }}</span>
        </button>
        <button
          v-if="store.active"
          class="mobile-action-item"
          role="menuitem"
          @click="handleMobileBrowse"
        >
          <span class="item-icon" aria-hidden="true">⊞</span>
          <span class="item-label">Channels</span>
        </button>
        <button
          v-if="hasMembers"
          class="mobile-action-item"
          :class="{ active: ui.membersOpen }"
          role="menuitem"
          @click="handleMobileMembers"
        >
          <span class="item-icon" aria-hidden="true">👥</span>
          <span class="item-label">Userlist</span>
        </button>
        <button
          class="mobile-action-item"
          role="menuitem"
          @click="handleMobileSettings"
        >
          <span class="item-icon" aria-hidden="true">⚙</span>
          <span class="item-label">Settings</span>
        </button>
      </div>
    </div>

    <!-- mIRC easter-egg window controls. Hidden unless the mIRC theme is
         active (see style.css); the close box flips the theme back off. -->
    <span class="mirc-winctl" aria-hidden="true">
      <button class="mirc-wb" tabindex="-1">_</button>
      <button class="mirc-wb" tabindex="-1">☐</button>
      <button class="mirc-wb mirc-close" tabindex="-1" title="Close" @click="toggleMircTheme()">✕</button>
    </span>

    <ChannelBrowser v-if="browseNet" :network="browseNet" @close="browseNet = null" />
    <ChannelInspectorModal
      v-if="showInspector && store.active && buffer"
      :network="store.active.network"
      :channel="buffer"
      @close="showInspector = false"
    />
  </header>
</template>
