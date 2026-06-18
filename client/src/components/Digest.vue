<script setup lang="ts">
import { computed, onMounted, onUnmounted } from "vue";
import { connection } from "../connection";
import type { MessageDTO } from "../proto/events";
import MessageItem from "./MessageItem.vue";

// Digest is the "what you missed" overlay shown on connect: the highlight lines
// that arrived since the user's read markers (store.missed, fetched by
// fetchMissed), plus a count summary of unread/mention activity derived from
// the init snapshot. Clicking a line jumps to it in its buffer; clicking an
// unread channel opens it. Purely a reader over store state.
const store = connection.store;

// Per-buffer unread/mention activity straight from the snapshot counters, which
// are authoritative (server read markers) and survive a reload. Sorted so the
// channels with mentions float to the top.
const unreadBuffers = computed(() => {
  const out: { network: string; netName: string; name: string; unread: number; highlight: number }[] = [];
  for (const n of store.networks) {
    for (const b of n.buffers) {
      if (b.unread > 0) {
        out.push({ network: n.id, netName: n.name, name: b.name, unread: b.unread, highlight: b.highlight });
      }
    }
  }
  return out.sort((a, b) => b.highlight - a.highlight || b.unread - a.unread);
});

const totalUnread = computed(() => unreadBuffers.value.reduce((s, b) => s + b.unread, 0));
const totalMentions = computed(() => unreadBuffers.value.reduce((s, b) => s + b.highlight, 0));

// One-line headline summarising the activity, e.g. "3 mentions · 42 messages in 5 channels".
const summary = computed(() => {
  const parts: string[] = [];
  if (totalMentions.value > 0) parts.push(`${totalMentions.value} mention${totalMentions.value === 1 ? "" : "s"}`);
  const chans = unreadBuffers.value.length;
  parts.push(`${totalUnread.value} message${totalUnread.value === 1 ? "" : "s"} in ${chans} channel${chans === 1 ? "" : "s"}`);
  return parts.join(" · ");
});

function jumpTo(m: MessageDTO) {
  connection.jumpToMessage(m);
  connection.closeDigest();
}

function openBuffer(network: string, buffer: string) {
  connection.select(network, buffer);
  connection.closeDigest();
}

function onKey(e: KeyboardEvent) {
  if (e.key === "Escape") connection.closeDigest();
}
onMounted(() => window.addEventListener("keydown", onKey));
onUnmounted(() => window.removeEventListener("keydown", onKey));
</script>

<template>
  <div class="digest-overlay" @click.self="connection.closeDigest()">
    <div class="digest" role="dialog" aria-label="What you missed">
      <header class="digest-head">
        <div>
          <h2>While you were away</h2>
          <p class="summary">{{ summary }}</p>
        </div>
        <button class="ghost" title="Close" @click="connection.closeDigest()">✕</button>
      </header>

      <!-- The actual mention lines (server-fetched), click to jump. -->
      <section v-if="store.missed.length" class="digest-section">
        <h3>Mentions</h3>
        <div
          v-for="(m, i) in store.missed"
          :key="m.id || i"
          class="missed-row"
          :title="`Go to this message in ${m.buffer}`"
          @click="jumpTo(m)"
        >
          <MessageItem :msg="m" :show-buffer="true" />
        </div>
      </section>

      <!-- Channels with unread activity, click to open. -->
      <section v-if="unreadBuffers.length" class="digest-section">
        <h3>Unread channels</h3>
        <button
          v-for="b in unreadBuffers"
          :key="b.network + b.name"
          class="unread-chan"
          @click="openBuffer(b.network, b.name)"
        >
          <span class="chan-name">{{ b.name }}</span>
          <span class="chan-net">{{ b.netName }}</span>
          <span class="chan-counts">
            <span v-if="b.highlight" class="badge hl">{{ b.highlight }}</span>
            <span class="badge">{{ b.unread }}</span>
          </span>
        </button>
      </section>

      <div v-if="!store.missed.length && !unreadBuffers.length" class="empty">
        You're all caught up.
      </div>
    </div>
  </div>
</template>

<style scoped>
.digest-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: flex-start;
  justify-content: center;
  padding: 6vh 12px;
  z-index: 50;
}
.digest {
  background: var(--bg);
  color: var(--fg);
  border: 1px solid var(--border);
  border-radius: 8px;
  width: min(720px, 100%);
  max-height: 84vh;
  overflow-y: auto;
  box-shadow: 0 12px 40px rgba(0, 0, 0, 0.4);
}
.digest-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  padding: 16px 18px 8px;
  position: sticky;
  top: 0;
  background: var(--bg);
}
.digest-head h2 {
  margin: 0;
  font-size: 1.15rem;
}
.summary {
  margin: 4px 0 0;
  color: var(--fg-dim);
  font-size: 0.9rem;
}
.digest-section {
  padding: 4px 18px 14px;
}
.digest-section h3 {
  margin: 8px 0 6px;
  font-size: 0.8rem;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--fg-dim);
}
.missed-row {
  cursor: pointer;
  border-radius: 4px;
  padding: 1px 4px;
}
.missed-row:hover {
  background: color-mix(in srgb, var(--accent) 12%, transparent);
}
.unread-chan {
  display: flex;
  align-items: baseline;
  gap: 8px;
  width: 100%;
  text-align: left;
  background: none;
  border: none;
  color: inherit;
  padding: 6px 8px;
  border-radius: 4px;
  cursor: pointer;
  font: inherit;
}
.unread-chan:hover {
  background: color-mix(in srgb, var(--accent) 12%, transparent);
}
.chan-name {
  font-weight: 600;
}
.chan-net {
  color: var(--fg-dim);
  font-size: 0.85em;
  flex: 1;
}
.chan-counts {
  display: flex;
  gap: 4px;
}
.badge {
  background: var(--border);
  color: var(--fg);
  border-radius: 9px;
  padding: 0 7px;
  font-size: 0.78em;
  line-height: 1.5;
}
.badge.hl {
  background: var(--accent);
  color: #fff;
}
.empty {
  padding: 24px 18px 28px;
  text-align: center;
  color: var(--fg-dim);
}
</style>
