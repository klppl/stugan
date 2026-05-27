<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import { connection } from "../connection";
import MessageItem from "./MessageItem.vue";
import ChatInput from "./ChatInput.vue";

const store = connection.store;
const listEl = ref<HTMLElement | null>(null);
const inputRef = ref<InstanceType<typeof ChatInput> | null>(null);
const dragging = ref(false);

const buffer = computed(() => connection.activeBuffer());

const members = computed(() => {
  const ms = buffer.value?.members ?? [];
  const rank = (mode: string) => "~&@%+".indexOf(mode[0] ?? "");
  return [...ms].sort((a, b) => {
    const ra = rank(a.modes);
    const rb = rank(b.modes);
    if (ra !== rb) return (ra < 0 ? 99 : ra) - (rb < 0 ? 99 : rb);
    return a.nick.toLowerCase().localeCompare(b.nick.toLowerCase());
  });
});

let stick = true;
let prependHeight = 0;
function onScroll() {
  const el = listEl.value;
  if (!el) return;
  stick = el.scrollTop + el.clientHeight >= el.scrollHeight - 40;
}
function loadOlder() {
  const el = listEl.value;
  prependHeight = el ? el.scrollHeight : 0;
  if (store.active) connection.loadOlder(store.active.network, store.active.buffer);
}

watch(
  () => buffer.value?.messages.length,
  async () => {
    await nextTick();
    const el = listEl.value;
    if (!el) return;
    if (prependHeight > 0) {
      el.scrollTop = el.scrollHeight - prependHeight;
      prependHeight = 0;
    } else if (stick) {
      el.scrollTop = el.scrollHeight;
    }
  },
);
watch(
  () => [store.view, store.active && `${store.active.network} ${store.active.buffer}`].join(),
  async () => {
    stick = true;
    await nextTick();
    if (listEl.value) listEl.value.scrollTop = listEl.value.scrollHeight;
  },
);

function openQuery(nick: string) {
  if (store.active) connection.openQuery(store.active.network, nick);
}

async function onDrop(e: DragEvent) {
  dragging.value = false;
  const files = e.dataTransfer?.files;
  if (!files || !files.length || !connection.hasCap("uploads")) return;
  for (const f of Array.from(files)) {
    const url = await connection.upload(f);
    if (url) inputRef.value?.appendText(url);
  }
}
</script>

<template>
  <section class="chat">
    <!-- Search results -->
    <template v-if="store.view === 'search'">
      <header class="chat-header"><span class="buffer-name">Search: {{ store.search.query }}</span></header>
      <div class="messages">
        <div v-if="store.search.busy" class="empty">searching…</div>
        <div v-else-if="!store.search.results.length" class="empty">no matches</div>
        <MessageItem v-for="(m, i) in store.search.results" :key="i" :msg="m" :show-buffer="true" />
      </div>
    </template>

    <!-- Mentions -->
    <template v-else-if="store.view === 'mentions'">
      <header class="chat-header"><span class="buffer-name">Mentions</span></header>
      <div class="messages">
        <div v-if="!store.mentions.length" class="empty">no mentions yet</div>
        <MessageItem v-for="(m, i) in store.mentions" :key="i" :msg="m" :show-buffer="true" />
      </div>
    </template>

    <!-- Chat -->
    <template v-else>
      <header v-if="buffer" class="chat-header">
        <span class="buffer-name">{{ buffer.name }}</span>
        <span v-if="buffer.topic" class="topic">{{ buffer.topic }}</span>
      </header>
      <div v-else class="chat-header">no buffer selected</div>

      <div
        class="chat-body"
        @dragover.prevent="dragging = true"
        @dragleave.prevent="dragging = false"
        @drop.prevent="onDrop"
      >
        <div ref="listEl" class="messages" @scroll="onScroll">
          <button v-if="buffer?.more" class="load-older" @click="loadOlder">Load older messages</button>
          <MessageItem v-for="(m, i) in buffer?.messages ?? []" :key="m.id || i" :msg="m" />
        </div>
        <aside v-if="members.length" class="members">
          <div class="members-head">{{ members.length }} users</div>
          <ul>
            <li
              v-for="mem in members"
              :key="mem.nick"
              :class="{ away: mem.away }"
              :title="mem.away ? mem.nick + ' (away)' : 'open query with ' + mem.nick"
              @click="openQuery(mem.nick)"
            >
              <span class="modes">{{ mem.modes }}</span>{{ mem.nick }}
            </li>
          </ul>
        </aside>
        <div v-if="dragging" class="dropzone">Drop files to upload</div>
      </div>

      <ChatInput
        ref="inputRef"
        :network="store.active?.network ?? ''"
        :buffer="buffer"
      />
    </template>
  </section>
</template>
