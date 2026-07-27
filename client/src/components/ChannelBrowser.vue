<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import { connection } from "../connection";

const props = defineProps<{ network: string }>();
const emit = defineEmits<{ close: [] }>();

const query = ref("");
const store = connection.store;

const result = computed(() =>
  store.channelList.network === props.network ? store.channelList : { channels: [], busy: false },
);

// Sort by popularity (users count) for a useful default ordering.
const channels = computed(() => [...result.value.channels].sort((a, b) => b.users - a.users));

function refresh() {
  connection.listChannels(props.network, query.value.trim());
}

function join(name: string) {
  connection.send(props.network, "*status", "/join " + name);
  emit("close");
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === "Escape") {
    emit("close");
  }
}

onMounted(() => {
  refresh();
  window.addEventListener("keydown", onKeydown);
});

onUnmounted(() => {
  window.removeEventListener("keydown", onKeydown);
});
</script>

<template>
  <div class="modal-backdrop" @click.self="emit('close')">
    <div class="modal-card browser-modal">
      <header class="modal-header">
        <div class="header-title">
          <h2>Channels on {{ network }}</h2>
        </div>
        <button class="close-btn" aria-label="Close" @click="emit('close')">✕</button>
      </header>

      <div class="modal-search-bar">
        <form class="search-form" @submit.prevent="refresh">
          <input
            v-model="query"
            type="search"
            class="search-input"
            placeholder="Search or filter (e.g. >50, *react*, #dev)…"
            autofocus
          />
          <button type="submit" class="btn btn-primary btn-sm">Search</button>
        </form>
      </div>

      <div class="modal-body browser-list-container">
        <div v-if="result.busy" class="loading-state">
          <p class="hint">Loading channel catalog… (large networks can take a few seconds)</p>
        </div>
        <div v-else-if="!channels.length" class="empty-state">
          <p class="hint">No channels found matching "{{ query }}".</p>
        </div>
        <div v-else class="browser-list">
          <div
            v-for="c in channels"
            :key="c.name"
            class="browser-item"
            title="Click to join channel"
            @click="join(c.name)"
          >
            <div class="item-left">
              <span class="bc-name">{{ c.name }}</span>
              <span class="bc-users" title="Member count">👥 {{ c.users }}</span>
            </div>
            <span class="bc-topic">{{ c.topic || "(no topic set)" }}</span>
          </div>
        </div>
      </div>

      <footer class="modal-footer">
        <span class="hint footer-hint">
          {{ channels.length }} channel{{ channels.length === 1 ? "" : "s" }} listed · Click any channel to join
        </span>
        <span class="spacer" />
        <button class="btn btn-sm btn-ghost" @click="emit('close')">Close</button>
      </footer>
    </div>
  </div>
</template>

<style scoped>
.modal-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.65);
  backdrop-filter: blur(4px);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 100;
  padding: 16px;
}

.browser-modal {
  background: var(--bg);
  color: var(--fg);
  border: 1px solid var(--border);
  border-radius: 12px;
  width: 620px;
  max-width: 100%;
  max-height: 85vh;
  display: flex;
  flex-direction: column;
  box-shadow: 0 20px 50px rgba(0, 0, 0, 0.45);
  overflow: hidden;
  animation: modal-pop 0.15s ease-out;
}

@keyframes modal-pop {
  from {
    opacity: 0;
    transform: scale(0.96);
  }
  to {
    opacity: 1;
    transform: scale(1);
  }
}

.modal-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 16px 20px;
  background: var(--bg-sidebar);
  border-bottom: 1px solid var(--border);
}

.header-title h2 {
  margin: 0;
  font-size: 18px;
  font-weight: 700;
}

.close-btn {
  background: transparent;
  border: none;
  color: var(--fg-dim);
  font-size: 18px;
  cursor: pointer;
  padding: 4px 8px;
  border-radius: 4px;
}
.close-btn:hover {
  color: var(--fg);
  background: var(--bg-alt);
}

.modal-search-bar {
  padding: 12px 20px;
  background: var(--bg-sidebar);
  border-bottom: 1px solid var(--border);
}

.search-form {
  display: flex;
  gap: 10px;
}

.search-input {
  flex: 1;
  background: var(--bg);
  color: var(--fg);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 6px 12px;
  font-size: 13px;
  outline: none;
}
.search-input:focus {
  border-color: var(--accent);
}

.browser-list-container {
  flex: 1;
  overflow-y: auto;
  min-height: 220px;
  max-height: 480px;
  padding: 12px;
}

.loading-state,
.empty-state {
  padding: 32px 16px;
  text-align: center;
}

.browser-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.browser-item {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 10px 14px;
  border-radius: 6px;
  background: var(--bg-alt);
  border: 1px solid var(--border);
  cursor: pointer;
  transition: all 0.12s ease;
}

.browser-item:hover {
  border-color: var(--accent);
  background: color-mix(in srgb, var(--accent) 10%, var(--bg-alt));
}

.item-left {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-shrink: 0;
  min-width: 180px;
}

.bc-name {
  font-weight: 600;
  font-size: 14px;
  color: var(--fg);
}

.bc-users {
  font-size: 12px;
  color: var(--accent);
  background: color-mix(in srgb, var(--accent) 15%, transparent);
  padding: 2px 6px;
  border-radius: 4px;
  font-weight: 500;
  white-space: nowrap;
}

.bc-topic {
  flex: 1;
  font-size: 12px;
  color: var(--fg-dim);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.modal-footer {
  display: flex;
  align-items: center;
  padding: 12px 20px;
  background: var(--bg-sidebar);
  border-top: 1px solid var(--border);
}

.footer-hint {
  font-size: 12px;
  color: var(--fg-dim);
}

.spacer {
  flex: 1;
}

.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 6px 12px;
  border-radius: 6px;
  border: 1px solid var(--border);
  background: var(--bg);
  color: var(--fg);
  font-size: 13px;
  cursor: pointer;
}
.btn:hover {
  border-color: var(--accent);
  background: var(--bg-alt);
}
.btn-primary {
  background: var(--accent);
  color: #fff;
  border-color: var(--accent);
}
.btn-sm {
  padding: 4px 10px;
  font-size: 12px;
}
.btn-ghost {
  border: none;
  background: transparent;
}
</style>
