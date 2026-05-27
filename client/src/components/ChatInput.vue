<script setup lang="ts">
import { computed, reactive, ref } from "vue";
import type { Buffer } from "../connection";
import { connection } from "../connection";
import { emojiMatches, replaceEmoji } from "../emoji";

const props = defineProps<{ network: string; buffer: Buffer | null }>();

const BUILTINS = ["me", "msg", "notice", "join", "part", "nick", "quit", "raw", "query", "greet"];

const text = ref("");
const inputEl = ref<HTMLInputElement | null>(null);

interface AC {
  open: boolean;
  items: string[]; // the full replacement token for each candidate
  labels: string[]; // what to show
  index: number;
  start: number; // index in text where the token begins
  end: number;
}
const ac = reactive<AC>({ open: false, items: [], labels: [], index: 0, start: 0, end: 0 });

// recentNicks: members of the active channel, those who spoke recently first.
const recentNicks = computed(() => {
  const b = props.buffer;
  if (!b) return [];
  const recent: string[] = [];
  for (let i = b.messages.length - 1; i >= 0 && recent.length < 30; i--) {
    const f = b.messages[i].from;
    if (f && !recent.includes(f)) recent.push(f);
  }
  const names = b.members.map((m) => m.nick);
  const rank = (n: string) => {
    const i = recent.findIndex((r) => r.toLowerCase() === n.toLowerCase());
    return i < 0 ? 1000 : i;
  };
  return [...names].sort((a, c) => rank(a) - rank(c) || a.localeCompare(c));
});

const allChannels = computed(() => {
  const set = new Set<string>();
  for (const n of connection.store.networks)
    for (const b of n.buffers) if (b.kind === "channel") set.add(b.name);
  return [...set];
});

function token(): { word: string; start: number; end: number } {
  const el = inputEl.value;
  const pos = el ? el.selectionStart ?? text.value.length : text.value.length;
  const upto = text.value.slice(0, pos);
  const m = upto.match(/(\S*)$/);
  const word = m ? m[1] : "";
  return { word, start: pos - word.length, end: pos };
}

function refresh() {
  const { word, start, end } = token();
  let items: string[] = [];
  let labels: string[] = [];
  if (word.startsWith("/") && start === 0) {
    const p = word.slice(1).toLowerCase();
    const m = BUILTINS.filter((c) => c.startsWith(p));
    items = m.map((c) => "/" + c + " ");
    labels = m.map((c) => "/" + c);
  } else if (word.startsWith("#")) {
    const m = allChannels.value.filter((c) => c.toLowerCase().startsWith(word.toLowerCase()));
    items = m.map((c) => c + " ");
    labels = m;
  } else if (word.startsWith(":") && word.length > 1) {
    const m = emojiMatches(word.slice(1));
    items = m.map((e) => e.char + " ");
    labels = m.map((e) => `${e.char} :${e.code}:`);
  } else if (word.length >= 1) {
    const m = recentNicks.value.filter((n) => n.toLowerCase().startsWith(word.toLowerCase())).slice(0, 8);
    items = m.map((n) => (start === 0 ? n + ": " : n + " "));
    labels = m;
  }
  ac.items = items;
  ac.labels = labels;
  ac.start = start;
  ac.end = end;
  ac.index = 0;
  ac.open = items.length > 0;
}

function accept(i = ac.index) {
  if (!ac.open || !ac.items[i]) return;
  text.value = text.value.slice(0, ac.start) + ac.items[i] + text.value.slice(ac.end);
  ac.open = false;
}

function onKeydown(e: KeyboardEvent) {
  if (ac.open) {
    if (e.key === "Tab" || e.key === "Enter") {
      e.preventDefault();
      accept();
      return;
    }
    if (e.key === "ArrowDown") {
      e.preventDefault();
      ac.index = (ac.index + 1) % ac.items.length;
      return;
    }
    if (e.key === "ArrowUp") {
      e.preventDefault();
      ac.index = (ac.index - 1 + ac.items.length) % ac.items.length;
      return;
    }
    if (e.key === "Escape") {
      ac.open = false;
      return;
    }
  } else if (e.key === "Tab") {
    e.preventDefault();
    refresh();
  }
}

function submit() {
  const t = text.value.trim();
  if (!t || !props.buffer) return;
  connection.send(props.network, props.buffer.name, replaceEmoji(t));
  text.value = "";
  ac.open = false;
}

async function onPaste(e: ClipboardEvent) {
  const file = e.clipboardData?.files?.[0];
  if (!file || !connection.hasCap("uploads")) return;
  e.preventDefault();
  const url = await connection.upload(file);
  if (url) text.value = (text.value ? text.value + " " : "") + url;
}

function appendText(s: string) {
  text.value = (text.value ? text.value.trimEnd() + " " : "") + s + " ";
  inputEl.value?.focus();
}

defineExpose({ inputEl, appendText });
</script>

<template>
  <form class="chat-input" @submit.prevent="submit">
    <div class="ac-wrap">
      <ul v-if="ac.open" class="autocomplete">
        <li
          v-for="(label, i) in ac.labels"
          :key="i"
          :class="{ sel: i === ac.index }"
          @mousedown.prevent="accept(i)"
        >
          {{ label }}
        </li>
      </ul>
      <input
        ref="inputEl"
        v-model="text"
        :disabled="!buffer"
        placeholder="Type a message… (Tab to complete, :emoji:, paste to upload)"
        autocomplete="off"
        @input="refresh"
        @keydown="onKeydown"
        @paste="onPaste"
      />
    </div>
    <button type="submit" :disabled="!buffer">Send</button>
  </form>
</template>
