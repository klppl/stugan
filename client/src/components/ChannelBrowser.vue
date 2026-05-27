<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { connection } from "../connection";

const props = defineProps<{ network: string }>();
const emit = defineEmits<{ close: [] }>();

const query = ref("");
const store = connection.store;

const result = computed(() =>
  store.channelList.network === props.network ? store.channelList : { channels: [], busy: false },
);

// Sort by popularity for a useful default ordering.
const channels = computed(() => [...result.value.channels].sort((a, b) => b.users - a.users));

function refresh() {
  connection.listChannels(props.network, query.value.trim());
}

function join(name: string) {
  connection.send(props.network, "*status", "/join " + name);
  emit("close");
}

onMounted(refresh);
</script>

<template>
  <div class="settings-overlay" @click.self="emit('close')">
    <div class="settings browser">
      <h2>Channels on {{ network }}</h2>
      <form class="row" @submit.prevent="refresh">
        <input v-model="query" placeholder="filter, e.g. >50 or *term*" />
        <button type="submit">Search</button>
      </form>

      <div class="browser-list">
        <p v-if="result.busy" class="hint">Loading… (large networks can take a moment)</p>
        <p v-else-if="!channels.length" class="hint">No channels — try a filter.</p>
        <div v-for="c in channels" :key="c.name" class="browser-item" @click="join(c.name)">
          <span class="bc-name">{{ c.name }}</span>
          <span class="bc-users">{{ c.users }}</span>
          <span class="bc-topic">{{ c.topic }}</span>
        </div>
      </div>

      <div class="row">
        <span class="hint">{{ channels.length }} shown — click to join.</span>
        <span class="spacer" />
        <button @click="emit('close')">Close</button>
      </div>
    </div>
  </div>
</template>
