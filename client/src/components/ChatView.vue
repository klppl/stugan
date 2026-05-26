<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import { connection } from "../connection";

const store = connection.store;
const input = ref("");
const listEl = ref<HTMLElement | null>(null);

const buffer = computed(() => connection.activeBuffer());

// Members sorted by mode prefix (ops first), then nick.
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

function time(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function submit() {
  const text = input.value.trim();
  if (!text || !store.active) return;
  connection.send(store.active.network, store.active.buffer, text);
  input.value = "";
}

// Autoscroll to the newest message when the active buffer's length changes.
watch(
  () => buffer.value?.messages.length,
  async () => {
    await nextTick();
    if (listEl.value) listEl.value.scrollTop = listEl.value.scrollHeight;
  },
);
</script>

<template>
  <section class="chat">
    <header v-if="buffer" class="chat-header">
      <span class="buffer-name">{{ buffer.name }}</span>
      <span v-if="buffer.topic" class="topic">{{ buffer.topic }}</span>
    </header>
    <div v-else class="chat-header">no buffer selected</div>

    <div class="chat-body">
      <div ref="listEl" class="messages">
        <div
          v-for="(m, i) in buffer?.messages ?? []"
          :key="m.id || i"
          class="message"
          :class="m.kind"
        >
          <span class="ts">{{ time(m.time) }}</span>
          <template v-if="m.kind === 'system'">
            <span class="sys">— {{ m.text }}</span>
          </template>
          <template v-else-if="m.kind === 'action'">
            <span class="sys">* {{ m.from }} {{ m.text }}</span>
          </template>
          <template v-else>
            <span class="from" :class="{ self: m.self }">{{ m.from }}</span>
            <span class="text">{{ m.text }}</span>
          </template>
        </div>
      </div>

      <aside v-if="members.length" class="members">
        <div class="members-head">{{ members.length }} users</div>
        <ul>
          <li v-for="mem in members" :key="mem.nick">
            <span class="modes">{{ mem.modes }}</span>{{ mem.nick }}
          </li>
        </ul>
      </aside>
    </div>

    <form class="chat-input" @submit.prevent="submit">
      <input
        v-model="input"
        :disabled="!store.active"
        placeholder="Type a message…"
        autocomplete="off"
      />
      <button type="submit" :disabled="!store.active">Send</button>
    </form>
  </section>
</template>
