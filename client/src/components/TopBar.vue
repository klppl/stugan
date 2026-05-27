<script setup lang="ts">
import { computed, ref } from "vue";
import { connection } from "../connection";

const emit = defineEmits<{ settings: [] }>();
const store = connection.store;
const q = ref("");

const mentionCount = computed(() => store.mentions.length);

function doSearch() {
  if (q.value.trim()) connection.search(q.value);
}
</script>

<template>
  <header class="topbar">
    <input
      v-if="connection.hasCap('search')"
      v-model="q"
      class="search"
      placeholder="Search messages…"
      @keydown.enter="doSearch"
    />
    <button class="ghost" :class="{ active: store.view === 'mentions' }" @click="connection.showMentions()">
      @ Mentions
      <span v-if="mentionCount" class="badge">{{ mentionCount }}</span>
    </button>
    <span class="spacer" />
    <span class="conn-pill" :class="store.status">{{ store.status }}</span>
    <button class="ghost" title="Settings" @click="emit('settings')">⚙</button>
  </header>
</template>
