<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from "vue";
import type { Buffer } from "../connection";
import { bufKey, connection } from "../connection";
import { readDraft, writeDraft } from "../drafts";
import { emojiMatches, replaceEmoji } from "../emoji";
import { ui } from "../ui";
import UploadPreviewModal from "./UploadPreviewModal.vue";

const props = defineProps<{ network: string; buffer: Buffer | null }>();
const pastedImageFile = ref<File | null>(null);

function targetKey(network: string, buffer: string): string {
  return network && buffer ? bufKey(network, buffer) : "";
}

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

let activeDraftKey = targetKey(props.network, props.buffer?.name ?? "");
const text = ref(readDraft(activeDraftKey));
const inputEl = ref<HTMLTextAreaElement | null>(null);

// The Enter/Shift+Enter hint only makes sense with a physical keyboard, and at
// full length it wraps inside the narrow mobile input — pushing the resting
// one-row box to two lines with a scrollbar. Use a short, touch-appropriate
// placeholder on mobile.
//
// When the network (or stugan itself) is down the box says so instead: you can
// still type, and /commands still go through, but a plain line would bounce.
const net = computed(() => connection.store.networks.find((n) => n.id === props.network));
const offlineText = computed(() => {
  if (connection.store.status !== "open") return "Not connected to stugan — reconnecting…";
  const n = net.value;
  if (!n || n.state === "registered") return "";
  return n.state === "connecting" ? `Reconnecting to ${n.name}…` : `${n.name} is disconnected`;
});
const placeholder = computed(() => {
  if (offlineText.value) return offlineText.value;
  return ui.isMobile ? "Message" : "Enter to send · Shift+Enter for a new line · Tab completes nicks";
});

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

// Input history, recalled with ↑/↓ when no menu is open (irssi/weechat style).
// One ring per buffer. histIdx walks back from the newest entry; -1 means
// "live", i.e. the user's current draft, which is stashed in histDraft so ↓
// past the newest entry restores it. Plain lines survive a reload via
// sessionStorage (this tab only); slash commands stay in memory, since they
// can carry things like /msg NickServ IDENTIFY.
const HISTORY_MAX = 100;
const HISTORY_KEY = "stugan.inputHistory";
const histories = new Map<string, string[]>();
let histIdx = -1;
let histDraft = "";

function isCommandLine(s: string): boolean {
  return s.startsWith("/") && !s.startsWith("//");
}

function readStoredHistories(): Record<string, unknown> {
  try {
    const v = JSON.parse(sessionStorage.getItem(HISTORY_KEY) || "{}");
    return v && typeof v === "object" && !Array.isArray(v) ? v : {};
  } catch {
    return {};
  }
}

function historyFor(key: string): string[] {
  let h = histories.get(key);
  if (!h) {
    const stored = readStoredHistories()[key];
    h = Array.isArray(stored) ? stored.filter((s): s is string => typeof s === "string") : [];
    histories.set(key, h);
  }
  return h;
}

function rememberLine(key: string, line: string) {
  if (!key) return;
  const h = historyFor(key);
  if (h[h.length - 1] !== line) {
    h.push(line);
    if (h.length > HISTORY_MAX) h.shift();
  }
  try {
    const all = readStoredHistories();
    all[key] = h.filter((l) => !isCommandLine(l));
    sessionStorage.setItem(HISTORY_KEY, JSON.stringify(all));
  } catch {
    // ignore unavailable storage
  }
}

// setText replaces the input and parks the caret at `caret` (default: the end)
// on the next tick (v-model hasn't flushed to the DOM yet).
function setText(s: string, caret = s.length) {
  text.value = s;
  nextTick(() => {
    const el = inputEl.value;
    if (el) el.setSelectionRange(caret, caret);
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
  const history = historyFor(activeDraftKey);
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
  const history = historyFor(activeDraftKey);
  if (histIdx < history.length - 1) {
    histIdx++;
    setText(history[histIdx]);
  } else {
    histIdx = -1;
    setText(histDraft);
  }
}

// Completion runs in two modes:
//  - "type": a menu that follows the caret as you type, only for /commands
//    and :emoji (prefixes that can't be ordinary prose). Enter still sends
//    unless you picked a row with the arrow keys; Tab takes the top row.
//  - "tab": irssi/WeeChat-style Tab completion for everything (nicks,
//    #channels, commands, emoji). The first Tab inserts the first match in
//    place, each further Tab (Shift+Tab backwards) swaps in the next, Escape
//    restores what you typed, and any other key just keeps the current pick.
interface AC {
  open: boolean;
  mode: "type" | "tab";
  items: string[]; // the full replacement token for each candidate
  labels: string[]; // what to show
  index: number;
  start: number; // index in text where the token begins
  end: number; // index where it ends (moves as tab mode swaps candidates in)
  navigated: boolean; // type mode: user moved through the list with arrows
  original: string; // tab mode: the token as typed, restored on Escape
}
const ac = reactive<AC>({
  open: false, mode: "type", items: [], labels: [], index: 0, start: 0, end: 0, navigated: false, original: "",
});

function closeAC() {
  ac.open = false;
  ac.navigated = false;
  reqSeq++; // drop any in-flight plugin reply for this token
}

// Save the old target before restoring the new target's draft. ChatInput is
// deliberately not keyed in ChatView, so without this transition a draft
// typed in one buffer follows the component into whichever buffer is selected
// next. Ending typing on the old target also prevents a stale indicator.
watch(
  () => [props.network, props.buffer?.name ?? ""] as const,
  ([network, buffer], [oldNetwork, oldBuffer]) => {
    writeDraft(activeDraftKey, text.value);
    if (oldNetwork && oldBuffer && text.value.trim()) {
      connection.sendTyping(oldNetwork, oldBuffer, "done");
    }
    activeDraftKey = targetKey(network, buffer);
    histIdx = -1;
    histDraft = "";
    closeAC();
    setText(readDraft(activeDraftKey));
  },
);

watch(
  () => readDraft(activeDraftKey),
  (newDraft) => {
    if (newDraft !== text.value && (document.activeElement !== inputEl.value || !text.value)) {
      setText(newDraft);
    }
  },
);

// Wrapping depends on the box's width, so a height measured at one width is
// wrong at another: the input could stay several lines tall after the window
// widened, or after a first measure taken mid-layout (pane still opening,
// background tab). Re-measure whenever the width changes — only the width,
// since autosize itself changes the height.
let inputRO: ResizeObserver | null = null;
let lastInputWidth = 0;
onMounted(() => {
  nextTick(autosize);
  if (!("ResizeObserver" in window) || !inputEl.value) return;
  inputRO = new ResizeObserver(([entry]) => {
    const w = entry.contentRect.width;
    if (w === lastInputWidth) return;
    lastInputWidth = w;
    autosize();
  });
  inputRO.observe(inputEl.value);
});

onUnmounted(() => {
  inputRO?.disconnect();
  writeDraft(activeDraftKey, text.value);
  if (props.buffer && text.value.trim()) {
    connection.sendTyping(props.network, props.buffer.name, "done");
  }
});

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

function commandItems(word: string) {
  const p = word.slice(1).toLowerCase();
  const m = BUILTINS.filter((c) => c.startsWith(p));
  return { items: m.map((c) => "/" + c + " "), labels: m.map((c) => "/" + c) };
}
function emojiItems(word: string) {
  const m = emojiMatches(word.slice(1));
  return { items: m.map((e) => e.char + " "), labels: m.map((e) => `${e.char} :${e.code}:`) };
}

// tabCandidates: everything Tab can complete for the word under the caret.
function tabCandidates(word: string, start: number): { items: string[]; labels: string[] } {
  if (word.startsWith("/") && start === 0) return commandItems(word);
  if (word.startsWith(":") && word.length > 1) return emojiItems(word);
  if (word.startsWith("#")) {
    const m = allChannels.value.filter((c) => c.toLowerCase().startsWith(word.toLowerCase()));
    return { items: m.map((c) => c + " "), labels: m };
  }
  const m = recentNicks.value.filter((n) => n.toLowerCase().startsWith(word.toLowerCase())).slice(0, 12);
  return { items: m.map((n) => (start === 0 ? n + ": " : n + " ")), labels: m };
}

// Plugins (hook_completion) contribute extra candidates over the wire. The
// request is async, so the local list shows at once and plugin items arrive
// later; `onExtra` gets them only while this pass is still current.
function requestExtras(word: string, onExtra: (items: string[], labels: string[]) => void) {
  const mine = ++reqSeq;
  if (!props.buffer || !word) return;
  connection.requestCompletions(props.network, props.buffer.name, word).then((extra) => {
    if (mine !== reqSeq || !extra.length) return;
    const have = new Set(ac.open ? ac.items : []);
    const items: string[] = [];
    const labels: string[] = [];
    for (const e of extra) {
      const item = e.endsWith(" ") ? e : e + " ";
      if (have.has(item)) continue;
      have.add(item);
      items.push(item);
      labels.push(e);
    }
    if (items.length) onExtra(items, labels);
  });
}

// suggestAsYouType runs on every edit. It only ever opens the menu for a
// leading /command or an :emoji code — never for nicks or #channels, which
// look like ordinary words and would otherwise hijack Enter.
function suggestAsYouType() {
  const { word, start, end } = token();
  let found = { items: [] as string[], labels: [] as string[] };
  if (word.startsWith("/") && start === 0 && !word.startsWith("//")) found = commandItems(word);
  else if (word.startsWith(":") && word.length > 2) found = emojiItems(word);
  else {
    closeAC();
    return;
  }
  Object.assign(ac, { mode: "type", items: found.items, labels: found.labels, start, end, index: 0, navigated: false });
  ac.open = found.items.length > 0;
  if (word.startsWith("/")) {
    requestExtras(word, (items, labels) => {
      if (token().word !== word) return; // user kept typing; this reply is stale
      ac.items.push(...items);
      ac.labels.push(...labels);
      ac.open = true;
    });
  }
}

// applyCandidate swaps candidate i in for the token in place (tab mode).
function applyCandidate(i: number) {
  const item = ac.items[i];
  if (item === undefined) return;
  const next = text.value.slice(0, ac.start) + item + text.value.slice(ac.end);
  ac.end = ac.start + item.length;
  ac.index = i;
  setText(next, ac.end);
  writeDraft(activeDraftKey, next);
}

function startCycle(items: string[], labels: string[], start: number, end: number, word: string) {
  Object.assign(ac, { mode: "tab", items, labels, start, end, index: 0, navigated: false, original: word });
  applyCandidate(0);
  // A single match is simply completed; there's nothing to cycle through.
  ac.open = items.length > 1;
}

function tabComplete(dir: 1 | -1) {
  if (ac.open && ac.mode === "tab") {
    applyCandidate((ac.index + dir + ac.items.length) % ac.items.length);
    return;
  }
  if (ac.open && ac.mode === "type") {
    accept();
    return;
  }
  const { word, start, end } = token();
  if (!word) return;
  const local = tabCandidates(word, start);
  if (local.items.length) startCycle(local.items, local.labels, start, end, word);
  requestExtras(word, (items, labels) => {
    if (ac.open && ac.mode === "tab") {
      ac.items.push(...items);
      ac.labels.push(...labels);
    } else if (!local.items.length && token().word === word) {
      startCycle(items, labels, start, end, word);
    }
  });
}

// accept takes a type-mode suggestion (Tab, click, or Enter after arrowing).
function accept(i = ac.index) {
  if (!ac.open) return;
  if (ac.mode === "tab") {
    applyCandidate(i);
    closeAC();
    return;
  }
  const item = ac.items[i];
  if (item === undefined) return;
  const next = text.value.slice(0, ac.start) + item + text.value.slice(ac.end);
  setText(next, ac.start + item.length);
  writeDraft(activeDraftKey, next);
  closeAC();
}

// Keys that leave a Tab cycle alone; anything else (typing, caret moves)
// commits the current pick and ends the cycle.
const CYCLE_KEYS = new Set(["Tab", "Shift", "Enter", "Escape", "ArrowUp", "ArrowDown"]);

function onKeydown(e: KeyboardEvent) {
  if (e.isComposing) return;
  if (ac.open && ac.mode === "tab" && !CYCLE_KEYS.has(e.key)) closeAC();

  if (e.key === "Tab") {
    e.preventDefault();
    tabComplete(e.shiftKey ? -1 : 1);
    return;
  }
  if (ac.open) {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const d = e.key === "ArrowDown" ? 1 : -1;
      const i = (ac.index + d + ac.items.length) % ac.items.length;
      if (ac.mode === "tab") applyCandidate(i);
      else {
        ac.index = i;
        ac.navigated = true;
      }
      return;
    }
    if (e.key === "Escape") {
      e.preventDefault();
      if (ac.mode === "tab") {
        const next = text.value.slice(0, ac.start) + ac.original + text.value.slice(ac.end);
        setText(next, ac.start + ac.original.length);
        writeDraft(activeDraftKey, next);
      }
      closeAC();
      return;
    }
    if (e.key === "Enter" && ac.mode === "type" && ac.navigated && !e.shiftKey) {
      e.preventDefault();
      accept();
      return;
    }
  }
  if (e.key === "Enter" && !e.shiftKey) {
    // Enter sends; Shift+Enter falls through to insert a newline.
    e.preventDefault();
    submit();
  } else if (e.key === "ArrowUp" && caretOnFirstLine()) {
    e.preventDefault();
    historyPrev();
  } else if (e.key === "ArrowDown" && caretOnLastLine()) {
    e.preventDefault();
    historyNext();
  }
}

function onInput() {
  writeDraft(activeDraftKey, text.value);
  suggestAsYouType();
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
  rememberLine(activeDraftKey, t);
  histIdx = -1;
  histDraft = "";
  text.value = "";
  writeDraft(activeDraftKey, "");
  closeAC();
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

// insertNick addresses someone: "nick: " at the front of an empty box,
// otherwise "nick " at the caret (Shift-click on a nick in the log).
function insertNick(nick: string) {
  const el = inputEl.value;
  if (!text.value.trim()) {
    setText(nick + ": ");
  } else {
    const pos = el?.selectionStart ?? text.value.length;
    const before = text.value.slice(0, pos);
    const pad = before && !/\s$/.test(before) ? " " : "";
    const ins = pad + nick + " ";
    setText(before + ins + text.value.slice(pos), pos + ins.length);
  }
  writeDraft(activeDraftKey, text.value);
  el?.focus();
}

// quoteLine puts "> <nick> text" on its own line above whatever is being
// typed, so the reply that follows reads in context.
function quoteLine(nick: string, body: string) {
  const q = `> <${nick}> ${body}`;
  const rest = text.value.replace(/^\n+/, "");
  setText(q + "\n" + rest);
  writeDraft(activeDraftKey, text.value);
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
      suggestAsYouType();
      autosize();
    });
  }
  if (props.buffer && text.value.trim()) {
    connection.sendTyping(props.network, props.buffer.name, "active");
  }
}

defineExpose({ inputEl, appendText, focus, typeChar, insertNick, quoteLine });
</script>

<template>
  <form class="chat-input" :class="{ offline: !!offlineText }" @submit.prevent="submit">
    <div class="ac-wrap">
      <ul v-if="ac.open" class="autocomplete" role="listbox">
        <li
          v-for="(label, i) in ac.labels"
          :key="i"
          role="option"
          :aria-selected="i === ac.index"
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
        :aria-label="offlineText || 'Message'"
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
