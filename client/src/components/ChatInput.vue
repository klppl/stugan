<script setup lang="ts">
import { computed, nextTick, reactive, ref } from "vue";
import type { Buffer } from "../connection";
import { connection } from "../connection";
import { emojiMatches, replaceEmoji } from "../emoji";
import { ui } from "../ui";
import UploadPreviewModal from "./UploadPreviewModal.vue";

const props = defineProps<{ network: string; buffer: Buffer | null }>();
const pastedImageFile = ref<File | null>(null);

// Built-in slash commands exposed via Tab-completion. Mirrors the cases in
// internal/core/command.go — keep them in rough sync, though the default
// branch passes any unknown /FOO through as raw FOO so the completion list
// is a convenience, not a gate.
const BUILTINS = [
  "me", "msg", "notice", "join", "part", "nick", "quit", "raw", "query",
  "whois", "whowas", "who", "names",
  "mode", "kick", "ban", "unban", "invite",
  "op", "deop", "voice", "devoice", "halfop", "dehalfop",
  "away", "back",
  "topic", "chathistory",
];

const text = ref("");
const inputEl = ref<HTMLTextAreaElement | null>(null);

// The Enter/Shift+Enter hint only makes sense with a physical keyboard, and at
// full length it wraps inside the narrow mobile input — pushing the resting
// one-row box to two lines with a scrollbar. Use a short, touch-appropriate
// placeholder on mobile.
const placeholder = computed(() =>
  ui.isMobile ? "Message" : "Enter to send · Shift+Enter for a new line",
);

// Auto-grow the textarea up to a few lines, then scroll. Called after every
// change (typed, recalled, programmatic) so the box tracks its content.
const MAX_INPUT_PX = 160;
function autosize() {
  const el = inputEl.value;
  if (!el) return;
  el.style.height = "auto";
  // scrollHeight is content + padding but excludes the border, while the box is
  // border-box — so set the height to scrollHeight + border, otherwise the box
  // lands ~2px short and overflow-y:auto shows a spurious scrollbar on every
  // multi-line message.
  const cs = getComputedStyle(el);
  const border = parseFloat(cs.borderTopWidth) + parseFloat(cs.borderBottomWidth);
  el.style.height = Math.min(el.scrollHeight + border, MAX_INPUT_PX) + "px";
}

// Input history: a session-scoped ring of sent lines, recalled with ↑/↓ when
// the autocomplete menu is closed (irssi/weechat style). histIdx walks back
// from the newest entry; -1 means "live", i.e. the user's current draft, which
// is stashed in histDraft so ↓ past the newest entry restores it. Not
// persisted — recalled lines can contain /msg-style content.
const HISTORY_MAX = 100;
const history: string[] = [];
let histIdx = -1;
let histDraft = "";

// setText replaces the input and parks the caret at the end on the next tick
// (v-model hasn't flushed to the DOM yet), mirroring typeChar's approach.
function setText(s: string) {
  text.value = s;
  nextTick(() => {
    const el = inputEl.value;
    if (el) el.setSelectionRange(s.length, s.length);
    autosize();
  });
}

// caretOnFirstLine / caretOnLastLine gate history recall in a multi-line
// textarea: ↑/↓ recall history only at the top/bottom edge, and otherwise move
// the caret between lines as usual.
function caretOnFirstLine(): boolean {
  const pos = inputEl.value?.selectionStart ?? 0;
  return !text.value.slice(0, pos).includes("\n");
}
function caretOnLastLine(): boolean {
  const pos = inputEl.value?.selectionStart ?? text.value.length;
  return !text.value.slice(pos).includes("\n");
}

function historyPrev() {
  if (!history.length) return;
  if (histIdx === -1) {
    histDraft = text.value;
    histIdx = history.length - 1;
  } else if (histIdx > 0) {
    histIdx--;
  }
  setText(history[histIdx]);
}

function historyNext() {
  if (histIdx === -1) return;
  if (histIdx < history.length - 1) {
    histIdx++;
    setText(history[histIdx]);
  } else {
    histIdx = -1;
    setText(histDraft);
  }
}

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

// reqSeq tags each completion pass. A bump invalidates any in-flight plugin
// reply (from a superseding refresh, or from accept/escape/submit), so a
// late complete:res can't reopen or mutate a menu the user moved past.
let reqSeq = 0;

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

  // Plugins (hook_completion) contribute extra candidates over the wire. The
  // request is async, so the local menu shows instantly and plugin items are
  // appended when the reply lands — unless the token has moved on by then.
  const mine = ++reqSeq;
  if (props.buffer && word.length >= 1) {
    connection.requestCompletions(props.network, props.buffer.name, word).then((extra) => {
      if (mine !== reqSeq || !extra.length) return;
      const cur = token();
      if (cur.word !== word) return; // user kept typing; this reply is stale
      const have = new Set(ac.items);
      for (const e of extra) {
        const item = e.endsWith(" ") ? e : e + " ";
        if (have.has(item)) continue;
        have.add(item);
        ac.items.push(item);
        ac.labels.push(e);
      }
      ac.start = cur.start;
      ac.end = cur.end;
      ac.open = ac.items.length > 0;
    });
  }
}

function accept(i = ac.index) {
  if (!ac.open || !ac.items[i]) return;
  text.value = text.value.slice(0, ac.start) + ac.items[i] + text.value.slice(ac.end);
  ac.open = false;
  reqSeq++; // drop any in-flight plugin reply for the token we just replaced
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
      reqSeq++;
      return;
    }
  } else if (e.key === "Enter" && !e.shiftKey) {
    // Enter sends; Shift+Enter falls through to insert a newline.
    e.preventDefault();
    submit();
  } else if (e.key === "Tab") {
    e.preventDefault();
    refresh();
  } else if (e.key === "ArrowUp" && caretOnFirstLine()) {
    e.preventDefault();
    historyPrev();
  } else if (e.key === "ArrowDown" && caretOnLastLine()) {
    e.preventDefault();
    historyNext();
  }
}

function onInput() {
  refresh();
  autosize();
  if (!props.buffer) return;
  if (text.value.trim()) connection.sendTyping(props.network, props.buffer.name, "active");
  else connection.sendTyping(props.network, props.buffer.name, "done");
}

function submit() {
  const t = text.value.trim();
  if (!t || !props.buffer) return;
  // A failed send (socket down mid-reconnect) keeps the draft in the box —
  // clearing it would silently destroy the message.
  if (!connection.send(props.network, props.buffer.name, replaceEmoji(t))) return;
  connection.sendTyping(props.network, props.buffer.name, "done");
  if (history[history.length - 1] !== t) {
    history.push(t);
    if (history.length > HISTORY_MAX) history.shift();
  }
  histIdx = -1;
  histDraft = "";
  text.value = "";
  ac.open = false;
  reqSeq++;
  // Keep typing after a button-send: mousedown.prevent on the Send button
  // stops the focus steal, and this covers any path where focus left anyway
  // (without it the mobile keyboard collapses on every send).
  inputEl.value?.focus();
  nextTick(autosize); // shrink the box back to one line
}

async function onPaste(e: ClipboardEvent) {
  const file = e.clipboardData?.files?.[0];
  if (!file || !connection.hasCap("uploads")) return;
  e.preventDefault();
  if (file.type.startsWith("image/")) {
    pastedImageFile.value = file;
    return;
  }
  const url = await connection.upload(file);
  if (url) text.value = (text.value ? text.value + " " : "") + url;
}

// File-picker upload: the paperclip button opens a hidden <input type=file>; picked
// files are uploaded the same way as paste/drag-drop, their URLs appended to
// the message. Only available when the server negotiated the uploads cap.
const fileEl = ref<HTMLInputElement | null>(null);
const uploads = computed(() => connection.hasCap("uploads"));

function pickFile() {
  fileEl.value?.click();
}

async function onFilePicked(e: Event) {
  const input = e.target as HTMLInputElement;
  const files = input.files ? Array.from(input.files) : [];
  input.value = ""; // reset so picking the same file again re-fires change
  for (const f of files) {
    const url = await connection.upload(f);
    if (url) appendText(url);
  }
}

function appendText(s: string) {
  text.value = (text.value ? text.value.trimEnd() + " " : "") + s + " ";
  inputEl.value?.focus();
  nextTick(autosize);
}

function focus() {
  inputEl.value?.focus();
}

// typeChar: focus the input and append `ch` to the current text. Used by
// ChatView's global keydown when the user starts typing somewhere else in
// the chat (focus on a sidebar button, body, etc). Focusing alone isn't
// enough — synchronously redirecting focus inside keydown drops the
// triggering character in practice across browsers, so we insert it here.
function typeChar(ch: string) {
  text.value = text.value + ch;
  const el = inputEl.value;
  if (el) {
    el.focus();
    // v-model hasn't flushed to the DOM yet; wait a tick so setSelectionRange
    // and the autocomplete refresh see the actual input length/value.
    nextTick(() => {
      const end = text.value.length;
      el.setSelectionRange(end, end);
      refresh();
      autosize();
    });
  }
  if (props.buffer && text.value.trim()) {
    connection.sendTyping(props.network, props.buffer.name, "active");
  }
}

defineExpose({ inputEl, appendText, focus, typeChar });
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
      <textarea
        ref="inputEl"
        v-model="text"
        :disabled="!buffer"
        rows="1"
        autocomplete="off"
        spellcheck="false"
        :placeholder="placeholder"
        @input="onInput"
        @keydown="onKeydown"
        @paste="onPaste"
      />
    </div>
    <button
      v-if="uploads"
      type="button"
      class="upload-btn"
      title="Attach a file"
      :disabled="!buffer"
      @click="pickFile"
    >
      <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
        <path d="M21.44 11.05l-9.19 9.19a6 6 0 0 1-8.49-8.49l9.19-9.19a4 4 0 0 1 5.66 5.66l-9.2 9.19a2 2 0 0 1-2.83-2.83l8.49-8.48" />
      </svg>
    </button>
    <input ref="fileEl" type="file" multiple hidden @change="onFilePicked" />
    <button type="submit" :disabled="!buffer" @mousedown.prevent>Send</button>
    <UploadPreviewModal
      v-if="pastedImageFile && buffer"
      :file="pastedImageFile"
      :network="network"
      :buffer="buffer.name"
      @close="pastedImageFile = null"
    />
  </form>
</template>
