<script setup lang="ts">
import { ref } from "vue";
import Sidebar from "./components/Sidebar.vue";
import ChatView from "./components/ChatView.vue";
import TopBar from "./components/TopBar.vue";
import Settings from "./components/Settings.vue";

const showSettings = ref(false);
</script>

<template>
  <div class="app">
    <Sidebar />
    <main class="main">
      <TopBar @settings="showSettings = true" />
      <ChatView />
    </main>
    <Settings v-if="showSettings" @close="showSettings = false" />
  </div>
</template>
