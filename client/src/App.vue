<script setup lang="ts">
import { ref } from "vue";
import Sidebar from "./components/Sidebar.vue";
import ChatView from "./components/ChatView.vue";
import TopBar from "./components/TopBar.vue";
import Settings from "./components/Settings.vue";
import Login from "./components/Login.vue";
import MagicWord from "./components/MagicWord.vue";
import { authState, canEnter, needsMagicWord } from "./auth";
import { ui, closeDrawers } from "./ui";

const showSettings = ref(false);
</script>

<template>
  <div v-if="!authState.ready" class="splash">connecting…</div>
  <MagicWord v-else-if="needsMagicWord()" />
  <Login v-else-if="!canEnter()" />
  <div
    v-else
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
  </div>
</template>
