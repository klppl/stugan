<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch, watchEffect } from "vue";
import Sidebar from "./components/Sidebar.vue";
import ChatView from "./components/ChatView.vue";
import TopBar from "./components/TopBar.vue";
import SettingsView from "./components/settings/SettingsView.vue";
import Login from "./components/Login.vue";
import MagicWord from "./components/MagicWord.vue";
import Toast from "./components/Toast.vue";
import CommandPalette from "./components/CommandPalette.vue";
import Digest from "./components/Digest.vue";
import ImageViewerModal from "./components/ImageViewerModal.vue";
import KeyboardShortcuts from "./components/KeyboardShortcuts.vue";
import { authState, canEnter, needsMagicWord } from "./auth";
import { ui, closeDrawers, closeImageViewer, useSwipeNav, isMac, stepBuffer, selectNth, nextUnread } from "./ui";
import { connection } from "./connection";
import { settings } from "./settings";

const showPalette = ref(false);

function toggleSettings() {
  connection.showSettings();
}

// Mobile: swipe right/left across the viewport to reveal the channel
// sidebar / members drawer.
useSwipeNav();

// Resolve the message layout setting ("auto" follows the phone breakpoint)
// onto <html>, where style.css switches the chat log between the classic
// one-line IRC form and the Discord-style "cozy" header-over-text form.
watchEffect(() => {
  const l = settings.messageLayout;
  document.documentElement.dataset.msgLayout = l === "auto" ? (ui.isMobile ? "cozy" : "classic") : l;
});

// Global shortcuts (the full list lives in KeyboardShortcuts.vue):
//   Ctrl/Cmd-K        command palette (quick switcher)
//   Ctrl/Cmd-/        shortcut cheat sheet
//   Alt+↑/↓           previous/next buffer in sidebar order (irssi/WeeChat)
//   Alt+Shift+↑/↓     previous/next buffer with unread activity (Slack)
//   Alt+A             next unread (irssi; not on macOS, where Option types text)
//   Alt+1–9, 0        buffer by sidebar position (Ctrl+digit on macOS)
// Keys are matched on e.code so they work on any keyboard layout.
function onGlobalKey(e: KeyboardEvent) {
  const mod = e.ctrlKey || e.metaKey;
  if (mod && !e.altKey && (e.key === "k" || e.key === "K")) {
    e.preventDefault();
    showPalette.value = !showPalette.value;
    return;
  }
  if (mod && !e.altKey && (e.code === "Slash" || e.key === "/")) {
    e.preventDefault();
    ui.shortcutsOpen = !ui.shortcutsOpen;
    return;
  }
  const digit = /^Digit(\d)$/.exec(e.code);
  const numMod = isMac ? e.ctrlKey && !e.altKey && !e.metaKey : e.altKey && !e.ctrlKey && !e.metaKey;
  if (digit && numMod && !e.shiftKey) {
    e.preventDefault();
    const n = Number(digit[1]);
    selectNth(n === 0 ? 9 : n - 1);
    return;
  }
  if (!e.altKey || e.ctrlKey || e.metaKey) return;
  if (e.key === "ArrowUp" || e.key === "ArrowDown") {
    e.preventDefault();
    const dir = e.key === "ArrowUp" ? -1 : 1;
    if (e.shiftKey) nextUnread(dir);
    else stepBuffer(dir);
  } else if (!isMac && !e.shiftKey && e.code === "KeyA") {
    e.preventDefault();
    nextUnread(1);
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

// --- Unread signal outside the tab -------------------------------------------
// The tab title carries "(N)" for things addressed to you (highlights plus
// unread DMs) or a "•" when there is only ordinary unread chatter, then the
// open buffer's name. The favicon gets a red dot while N > 0.
const attention = computed(() => {
  let n = 0;
  for (const net of store.networks) {
    for (const b of net.buffers) {
      n += b.kind === "query" ? Math.max(b.unread, b.highlight) : b.highlight;
    }
  }
  return n;
});
const anyUnread = computed(() => store.networks.some((n) => n.buffers.some((b) => b.unread > 0)));
const activeTitle = computed(() => {
  const a = store.active;
  if (!a || store.view !== "chat") return "";
  const buf = connection.activeBuffer();
  if (!buf) return "";
  return buf.kind === "status" ? store.networks.find((n) => n.id === a.network)?.name ?? "" : buf.name;
});
watchEffect(() => {
  const prefix = attention.value ? `(${attention.value}) ` : anyUnread.value ? "• " : "";
  document.title = `${prefix}${activeTitle.value ? activeTitle.value + " — " : ""}stugan`;
});

// The dotted favicon is drawn once from the PNG icon and cached; the original
// hrefs are restored when the count drops back to zero.
const iconLinks = Array.from(document.querySelectorAll<HTMLLinkElement>('link[rel="icon"]'));
const iconOriginals = iconLinks.map((l) => l.href);
let dottedIcon: Promise<string | null> | null = null;
function drawDottedIcon(): Promise<string | null> {
  return new Promise((resolve) => {
    const img = new Image();
    img.onload = () => {
      try {
        const size = 64;
        const c = document.createElement("canvas");
        c.width = c.height = size;
        const g = c.getContext("2d");
        if (!g) return resolve(null);
        g.drawImage(img, 0, 0, size, size);
        g.beginPath();
        g.arc(size - 15, 15, 14, 0, Math.PI * 2);
        g.fillStyle = "#e5484d";
        g.fill();
        g.lineWidth = 4;
        g.strokeStyle = "#ffffff";
        g.stroke();
        resolve(c.toDataURL("image/png"));
      } catch {
        resolve(null);
      }
    };
    img.onerror = () => resolve(null);
    img.src = "/icons/icon-192.png?v=2";
  });
}
watch(
  () => attention.value > 0,
  async (on) => {
    if (!iconLinks.length) return;
    if (!on) {
      iconLinks.forEach((l, i) => (l.href = iconOriginals[i]));
      return;
    }
    dottedIcon ??= drawDottedIcon();
    const url = await dottedIcon;
    if (url && attention.value > 0) iconLinks.forEach((l) => (l.href = url));
  },
  { immediate: true },
);

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
      :class="{ 'drawer-open': ui.sidebarOpen || ui.membersOpen, 'settings-active': store.view === 'settings' }"
    >
      <template v-if="store.view !== 'settings'">
        <Sidebar />
        <main class="main">
          <TopBar @settings="toggleSettings" />
          <ChatView />
        </main>
      </template>
      <SettingsView v-else @close="store.view = 'chat'" />
      <!-- Backdrop dims the chat while a drawer is open; tap closes both. -->
      <div
        v-if="store.view !== 'settings'"
        class="drawer-backdrop"
        :class="{ visible: ui.sidebarOpen || ui.membersOpen }"
        @click="closeDrawers"
      />
      <Digest v-if="connection.store.digestOpen" />
      <CommandPalette
        :open="showPalette"
        @close="showPalette = false"
        @settings="toggleSettings"
      />
      <KeyboardShortcuts v-if="ui.shortcutsOpen" @close="ui.shortcutsOpen = false" />
      <ImageViewerModal
        v-if="ui.activeImage"
        :image="ui.activeImage"
        @close="closeImageViewer"
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
