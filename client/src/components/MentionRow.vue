<script setup lang="ts">
import { computed } from "vue";
import { connection } from "../connection";
import type { MessageDTO } from "../proto/events";
import MessageItem from "./MessageItem.vue";

// One row in the Mentions / Search panes. Clicking the row expands the chat
// surrounding the message inline (fetched on demand, keyed by message id) so
// the user gets context without leaving the list; clicking again collapses it.
const props = defineProps<{ msg: MessageDTO }>();

const ctx = computed(() => connection.contextFor(props.msg));
const open = computed(() => ctx.value?.open ?? false);
</script>

<template>
  <div class="mention-row">
    <div
      class="jump-row"
      :class="{ open }"
      :title="open ? 'Hide context' : `Show chat around this message in ${msg.buffer}`"
      @click="connection.toggleContext(msg)"
    >
      <span class="disclosure" aria-hidden="true">{{ open ? "▾" : "▸" }}</span>
      <MessageItem :msg="msg" :show-buffer="true" />
    </div>
    <div v-if="open" class="mention-context">
      <div v-if="ctx?.loading" class="empty">loading context…</div>
      <div v-else-if="!ctx?.messages.length" class="empty">no surrounding messages</div>
      <template v-else>
        <div
          v-for="(cm, j) in ctx.messages"
          :key="cm.id || j"
          class="context-line"
          :class="{ anchor: cm.id === msg.id }"
        >
          <MessageItem :msg="cm" />
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.jump-row {
  display: flex;
  align-items: baseline;
  gap: 4px;
}
.disclosure {
  color: var(--fg-dim);
  font-size: 0.8em;
  width: 1em;
  flex: none;
}
/* The expanded context sits under the row, indented with a left rule so it
   reads as a nested excerpt of the origin buffer. */
.mention-context {
  margin: 2px 0 8px 1em;
  padding-left: 10px;
  border-left: 2px solid var(--border);
}
/* The mention/search hit itself, highlighted within its surroundings so the
   eye lands on the line the row is about. */
.context-line.anchor {
  background: color-mix(in srgb, var(--accent) 14%, transparent);
  border-radius: 3px;
}
</style>
