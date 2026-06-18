<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import Sidebar from "./components/Sidebar.vue";
import ChatView from "./components/ChatView.vue";
import TopBar from "./components/TopBar.vue";
import Settings from "./components/Settings.vue";
import Login from "./components/Login.vue";
import MagicWord from "./components/MagicWord.vue";
import Toast from "./components/Toast.vue";
import CommandPalette from "./components/CommandPalette.vue";
import Digest from "./components/Digest.vue";
import { authState, canEnter, needsMagicWord } from "./auth";
import { ui, closeDrawers, useSwipeNav } from "./ui";
import { connection } from "./connection";

const showSettings = ref(false);
const showPalette = ref(false);

// Mobile: swipe right/left across the viewport to reveal the channel
// sidebar / members drawer.
useSwipeNav();

// Ctrl/Cmd-K toggles the command palette (quick switcher) from anywhere.
function onGlobalKey(e: KeyboardEvent) {
  if ((e.ctrlKey || e.metaKey) && (e.key === "k" || e.key === "K")) {
    e.preventDefault();
    showPalette.value = !showPalette.value;
  }
}
onMounted(() => window.addEventListener("keydown", onGlobalKey));
onUnmounted(() => window.removeEventListener("keydown", onGlobalKey));

// --- mIRC easter egg chrome -------------------------------------------------
// The menu bar and status bar below are inert decoration that only become
// visible under the "mirc" theme (display:none everywhere else, see
// style.css). The status bar pulls real values from the store so it reads
// like mIRC's: nick, network, active window, connection state.
const store = connection.store;
const MIRC_MENUS = ["File", "View", "Favourites", "Tools", "Commands", "Window", "Help"];

const activeNet = computed(() => store.networks.find((n) => n.id === store.active?.network));
const mircNick = computed(() => activeNet.value?.nick || "stugan");
const mircNet = computed(() => activeNet.value?.name || "no server");
const mircWin = computed(() => connection.activeBuffer()?.name || "Status");
const mircUsers = computed(() => connection.activeBuffer()?.members.length ?? 0);
const mircStatus = computed(
  () => ({ connecting: "Connecting…", open: "Connected", closed: "Not connected" })[store.status],
);
</script>

<template>
  <div v-if="!authState.ready" class="splash">connecting…</div>
  <MagicWord v-else-if="needsMagicWord()" />
  <Login v-else-if="!canEnter()" />
  <div v-else class="approot">
    <!-- mIRC menu bar (decorative; visible only under the mirc theme) -->
    <div class="mirc-menubar" aria-hidden="true">
      <span v-for="m in MIRC_MENUS" :key="m" class="mirc-menu"
        ><span class="mirc-menu-ul">{{ m.charAt(0) }}</span>{{ m.slice(1) }}</span
      >
    </div>

    <div
      class="app"
      :class="{ 'drawer-open': ui.sidebarOpen || ui.membersOpen }"
    >
      <Sidebar />
      <main class="main">
        <TopBar @settings="showSettings = true" />
        <ChatView />
      </main>
      <!-- Backdrop dims the chat while a drawer is open; tap closes both. -->
      <div
        class="drawer-backdrop"
        :class="{ visible: ui.sidebarOpen || ui.membersOpen }"
        @click="closeDrawers"
      />
      <Settings v-if="showSettings" @close="showSettings = false" />
      <Digest v-if="connection.store.digestOpen" />
      <CommandPalette
        :open="showPalette"
        @close="showPalette = false"
        @settings="showSettings = true"
      />
      <Toast />
    </div>

    <!-- mIRC status bar (visible only under the mirc theme) -->
    <div class="mirc-statusbar" aria-hidden="true">
      <span class="sb-cell sb-nick">{{ mircNick }}</span>
      <span class="sb-cell">{{ mircNet }}</span>
      <span class="sb-cell sb-grow">{{ mircWin }}<template v-if="mircUsers"> ({{ mircUsers }})</template></span>
      <span class="sb-cell">Lag: 0.042s</span>
      <span class="sb-cell">{{ mircStatus }}</span>
    </div>
  </div>
</template>
