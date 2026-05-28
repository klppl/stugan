<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, provide, ref, watch } from "vue";
import { connection, bufKey } from "../connection";
import { isIgnored, toggleIgnore, settings } from "../settings";
import { nickColor } from "../nickColor";
import { useContextMenu } from "../contextMenu";
import { ui, closeDrawers } from "../ui";
import type { MemberDTO, MessageDTO } from "../proto/events";
import MessageItem from "./MessageItem.vue";
import ChatInput from "./ChatInput.vue";

const store = connection.store;

function memberColor(nick: string): string {
  return settings.coloredNicks ? nickColor(nick) : "";
}

// "X is typing…" for the active buffer.
const typingText = computed(() => {
  if (!store.active) return "";
  const who = store.typing[bufKey(store.active.network, store.active.buffer)] ?? [];
  if (!who.length) return "";
  if (who.length === 1) return `${who[0]} is typing…`;
  if (who.length === 2) return `${who[0]} and ${who[1]} are typing…`;
  return "several people are typing…";
});
const listEl = ref<HTMLElement | null>(null);
const inputRef = ref<InstanceType<typeof ChatInput> | null>(null);
const dragging = ref(false);

const buffer = computed(() => connection.activeBuffer());

// The "new messages" divider renders just above this message (identity
// compared against the row's message). null = no divider for this buffer.
const markerMsg = computed(() => buffer.value?.unreadMarker ?? null);

// Group messages by calendar day so the list shows "— Today —" / "— Yesterday —"
// / "— Wed, May 21 —" separators wherever the date changes.
function dayLabel(iso: string): string {
  const d = new Date(iso);
  const now = new Date();
  const ymd = (x: Date) => x.getFullYear() * 10000 + (x.getMonth() + 1) * 100 + x.getDate();
  const diff = ymd(now) - ymd(d);
  if (diff === 0) return "Today";
  if (diff === 1) return "Yesterday";
  const sameYear = d.getFullYear() === now.getFullYear();
  return d.toLocaleDateString([], {
    weekday: "short",
    month: "short",
    day: "numeric",
    year: sameYear ? undefined : "numeric",
  });
}
function dayKey(iso: string): string {
  const d = new Date(iso);
  return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;
}
// A run of consecutive join/part/quit/nick lines collapses into one foldable
// row (when settings.foldEvents is on) so busy channels don't drown in churn.
// A day boundary or any real message closes an open run.
const EVENT_KINDS = ["join", "part", "quit", "nick"];
type Row = { day?: string; msg?: MessageDTO; events?: MessageDTO[] };

const rows = computed<Row[]>(() => {
  const msgs = buffer.value?.messages ?? [];
  const out: Row[] = [];
  let prev = "";
  let pending: MessageDTO[] = [];
  let pendingDay: string | undefined;

  const flush = () => {
    if (!pending.length) return;
    if (pending.length === 1) out.push({ msg: pending[0], day: pendingDay });
    else out.push({ events: pending, day: pendingDay });
    pending = [];
    pendingDay = undefined;
  };

  for (const m of msgs) {
    const k = m.time ? dayKey(m.time) : "";
    const day = k && k !== prev ? dayLabel(m.time) : undefined;
    if (k) prev = k;
    if (day) flush(); // let the day separator render above the next row

    if (settings.foldEvents && EVENT_KINDS.includes(m.kind)) {
      if (!pending.length) pendingDay = day;
      pending.push(m);
      continue;
    }
    flush();
    out.push({ msg: m, day });
  }
  flush();
  return out;
});

// Fold expansion is keyed by the first event's time+text (join/part lines
// carry no msgid). Collapsed by default; click to reveal the individual lines.
const expandedFolds = ref<Set<string>>(new Set());
function foldKey(events: MessageDTO[]): string {
  const f = events[0];
  return (f.id || "") + "\0" + f.time + "\0" + f.text;
}
function isFoldOpen(events: MessageDTO[]): boolean {
  return expandedFolds.value.has(foldKey(events));
}
function toggleFold(events: MessageDTO[]) {
  const k = foldKey(events);
  const next = new Set(expandedFolds.value);
  if (next.has(k)) next.delete(k);
  else next.add(k);
  expandedFolds.value = next;
}
function eventSummary(events: MessageDTO[]): string {
  const counts: Record<string, number> = { join: 0, part: 0, quit: 0, nick: 0 };
  for (const e of events) counts[e.kind] = (counts[e.kind] ?? 0) + 1;
  const parts: string[] = [];
  if (counts.join) parts.push(`${counts.join} joined`);
  if (counts.part) parts.push(`${counts.part} left`);
  if (counts.quit) parts.push(`${counts.quit} quit`);
  if (counts.nick) parts.push(`${counts.nick} ${counts.nick === 1 ? "nick change" : "nick changes"}`);
  return parts.join(" · ");
}

const members = computed(() => {
  const ms = buffer.value?.members ?? [];
  // IRC prefix order: owner, admin, op, halfop, voice, then everyone else.
  // (Guard against the empty-mode case: "".indexOf("") is 0, which used to
  // bury ops below the unprefixed crowd.)
  const rank = (mode: string) => {
    const c = mode[0];
    if (!c) return 99;
    const i = "~&@%+".indexOf(c);
    return i < 0 ? 99 : i;
  };
  return [...ms].sort((a, b) => {
    const ra = rank(a.modes);
    const rb = rank(b.modes);
    if (ra !== rb) return ra - rb;
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

// backToLatest exits jumped-to window mode: ask the connection to drop
// the window and re-fetch the live tail. Re-engage stick-to-bottom so
// the user lands where new messages arrive.
function backToLatest() {
  if (!store.active) return;
  stick = true;
  connection.backToLatest(store.active.network, store.active.buffer);
}

watch(
  () => buffer.value?.messages.length,
  async () => {
    await nextTick();
    const el = listEl.value;
    if (!el) return;
    // A pending jump takes priority: don't clobber its scroll target with
    // the usual stick-to-bottom / preserve-on-prepend behaviour.
    if (store.jump && jumpMatchesActive()) {
      tryJump();
      return;
    }
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
    if (store.jump && jumpMatchesActive()) {
      tryJump();
      return;
    }
    if (listEl.value) listEl.value.scrollTop = listEl.value.scrollHeight;
  },
);

// When a fresh jump is set up, the view/active/messages-length watchers
// may not fire (e.g. clicking a mention for the buffer you're already
// reading). Trigger tryJump explicitly so those cases still work.
watch(
  () => store.jump?.id,
  async (id) => {
    if (!id) return;
    await nextTick();
    if (jumpMatchesActive()) tryJump();
  },
);

// jumpMatchesActive returns true when the active buffer is the one the
// pending jump targets (case-insensitive, like the rest of the client).
function jumpMatchesActive(): boolean {
  const j = store.jump;
  const a = store.active;
  if (!j || !a) return false;
  return j.network === a.network && j.buffer.toLowerCase() === a.buffer.toLowerCase();
}

// tryJump: find the target message in the current list and scroll to it.
// If it isn't loaded yet, request a single windowed page of context
// around the target's time and let the reply re-trigger this function
// via the messages-length watcher. One fetched flip prevents a request
// loop if the id genuinely isn't in the store.
function tryJump() {
  const j = store.jump;
  const el = listEl.value;
  if (!j || !el) return;
  if (!jumpMatchesActive()) {
    connection.clearJump();
    return;
  }
  const target = el.querySelector(`[data-msgid="${CSS.escape(j.id)}"]`) as HTMLElement | null;
  if (target) {
    target.scrollIntoView({ block: "center", behavior: "auto" });
    target.classList.add("jump-flash");
    // Stop sticking to bottom — the user is reading context, not chat tail.
    stick = false;
    setTimeout(() => target.classList.remove("jump-flash"), 1800);
    connection.clearJump();
    return;
  }
  if (!j.fetched) {
    j.fetched = true;
    connection.fetchAround(j.network, j.buffer, j.time);
  } else {
    // Window arrived but the id still isn't here — pruned, or a stale
    // reference; bail out silently.
    connection.clearJump();
  }
}

function openQuery(nick: string) {
  // A long-press-triggered open already fired; swallow the tap-to-DM.
  if (memberCtx.shouldSuppressClick()) return;
  if (store.active) connection.openQuery(store.active.network, nick);
  // On mobile the members list is a drawer — collapse it after picking someone.
  if (ui.isMobile) closeDrawers();
}

// Member context menu: right-click / long-press a nick in the user list
// OR on a sender nick in a chat message. The payload is a MemberDTO; for
// nicks that don't appear in the active channel's member list (queries,
// status buffer, stranger mentions) we synthesize one with empty modes —
// the menu items still work, the ±o/±h/±v labels just default to "+".
const memberCtx = useContextMenu<MemberDTO>({ height: 320 });

// memberForNick looks up a nick in the active buffer's member list and
// returns the live MemberDTO, falling back to a stub so the menu can open
// for any nick mentioned in chat (including senders of historical lines).
function memberForNick(nick: string): MemberDTO {
  const buf = connection.activeBuffer();
  if (buf) {
    const found = buf.members.find((m) => m.nick.toLowerCase() === nick.toLowerCase());
    if (found) return found;
  }
  return { nick, modes: "", away: false };
}

// Expose the right-click / long-press handlers down the tree so
// MessageItem can wire them onto sender nicks without inheriting the
// whole memberCtx state object. Same set of handlers as the user list.
provide("nickCtx", {
  onContext: (nick: string, ev: MouseEvent) => memberCtx.onContext(memberForNick(nick), ev),
  onTouchStart: (nick: string, ev: TouchEvent) => memberCtx.onTouchStart(memberForNick(nick), ev),
  onTouchMove: memberCtx.onTouchMove,
  cancelLp: memberCtx.cancelLp,
});

// hasMode returns true if the member carries the named prefix in their
// channel modes — used to flip between "+o" and "-o" labels and actions.
// The IRC convention: each mode has a one-char prefix that piles up at
// the start of modes (e.g. "@+" = op and voice). We check character-wise.
const MODE_PREFIX: Record<string, string> = { o: "@", h: "%", v: "+" };
function hasMode(member: MemberDTO, flag: "o" | "h" | "v"): boolean {
  return member.modes.includes(MODE_PREFIX[flag]);
}

function activeChannel(): string | null {
  // Member modes are channel-only — protect against the rare query case
  // where the menu somehow opens with a member payload.
  if (!store.active) return null;
  const buf = connection.activeBuffer();
  if (!buf || buf.kind !== "channel") return null;
  return store.active.buffer;
}

function ctxWhois() {
  // Use the proper /whois built-in so the engine routes the reply
  // numerics back to this buffer (see core.applyNumeric).
  const m = memberCtx.state.value?.payload;
  if (!m || !store.active) return;
  connection.send(store.active.network, store.active.buffer, "/whois " + m.nick);
  memberCtx.close();
}
function ctxIgnore() {
  const m = memberCtx.state.value?.payload;
  if (!m || !store.active) return;
  toggleIgnore(store.active.network, m.nick);
  memberCtx.close();
}
function ctxDM() {
  const m = memberCtx.state.value?.payload;
  if (!m || !store.active) return;
  connection.openQuery(store.active.network, m.nick);
  memberCtx.close();
  if (ui.isMobile) closeDrawers();
}
function ctxMode(flag: "o" | "h" | "v") {
  // Use the /op /deop /voice /devoice /halfop /dehalfop built-ins —
  // they expand to a single MODE line and work in the current channel.
  const m = memberCtx.state.value?.payload;
  if (!m || !activeChannel() || !store.active) return;
  const cmdName = flag === "o" ? (hasMode(m, "o") ? "deop" : "op")
    : flag === "h" ? (hasMode(m, "h") ? "dehalfop" : "halfop")
    : (hasMode(m, "v") ? "devoice" : "voice");
  connection.send(store.active.network, store.active.buffer, `/${cmdName} ${m.nick}`);
  memberCtx.close();
}
function ctxKick() {
  const m = memberCtx.state.value?.payload;
  if (!m || !activeChannel() || !store.active) return;
  connection.send(store.active.network, store.active.buffer, `/kick ${m.nick}`);
  memberCtx.close();
}
function ctxKickBan() {
  const m = memberCtx.state.value?.payload;
  const chan = activeChannel();
  if (!m || !chan || !store.active) return;
  // Bare-nick ban mask; user can refine with /ban manually for tighter
  // user@host masks. MODE +b before KICK so a rejoin can't race.
  connection.send(store.active.network, store.active.buffer, `/ban ${m.nick}!*@*`);
  connection.send(store.active.network, store.active.buffer, `/kick ${m.nick}`);
  memberCtx.close();
}

// Inline topic editing for channel buffers.
const editingTopic = ref(false);
const topicDraft = ref("");
const topicInput = ref<HTMLInputElement | null>(null);

function startEditTopic() {
  if (buffer.value?.kind !== "channel") return;
  topicDraft.value = buffer.value.topic;
  editingTopic.value = true;
  nextTick(() => topicInput.value?.focus());
}
function saveTopic() {
  if (store.active) connection.send(store.active.network, store.active.buffer, "/topic " + topicDraft.value.trim());
  editingTopic.value = false;
}

// Auto-focus the chat input when the user starts typing somewhere else in
// the chat view (e.g. focus landed on a sidebar button after picking a
// buffer, or nothing in particular). Same affordance as Discord/Slack:
// hit any printable key and the keystroke lands in the input. We can't
// just focus() and let the browser deliver the character — that's
// unreliable cross-browser — so we preventDefault and insert `e.key`
// into the input ourselves via typeChar.
function onGlobalKeydown(e: KeyboardEvent) {
  if (store.view !== "chat" || !buffer.value) return;
  if (e.ctrlKey || e.metaKey || e.altKey) return;
  if (e.isComposing) return;
  // Only printable characters — single-char e.key, so "Tab", "Enter",
  // "Escape", "ArrowDown" etc. are left to their existing handlers.
  if (e.key.length !== 1) return;
  // Don't steal focus from another editable: topic edit, search box, the
  // chat input itself, or anything contenteditable.
  const a = document.activeElement as HTMLElement | null;
  if (a && (a.tagName === "INPUT" || a.tagName === "TEXTAREA" || a.tagName === "SELECT" || a.isContentEditable)) return;
  e.preventDefault();
  inputRef.value?.typeChar(e.key);
}
onMounted(() => document.addEventListener("keydown", onGlobalKeydown));
onUnmounted(() => document.removeEventListener("keydown", onGlobalKeydown));

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
        <div
          v-for="(m, i) in store.search.results"
          :key="i"
          class="jump-row"
          :title="`Open ${m.buffer} at this message`"
          @click="connection.jumpToMessage(m)"
        >
          <MessageItem :msg="m" :show-buffer="true" />
        </div>
      </div>
    </template>

    <!-- Mentions -->
    <template v-else-if="store.view === 'mentions'">
      <header class="chat-header"><span class="buffer-name">Mentions</span></header>
      <div class="messages">
        <div v-if="!store.mentions.length" class="empty">no mentions yet</div>
        <div
          v-for="(m, i) in store.mentions"
          :key="i"
          class="jump-row"
          :title="`Open ${m.buffer} at this message`"
          @click="connection.jumpToMessage(m)"
        >
          <MessageItem :msg="m" :show-buffer="true" />
        </div>
      </div>
    </template>

    <!-- Chat -->
    <template v-else>
      <header v-if="buffer" class="chat-header">
        <span class="buffer-name">{{ buffer.name }}</span>
        <input
          v-if="editingTopic"
          ref="topicInput"
          v-model="topicDraft"
          class="topic-edit"
          @keydown.enter="saveTopic"
          @keydown.esc="editingTopic = false"
          @blur="editingTopic = false"
        />
        <span
          v-else
          class="topic"
          :class="{ editable: buffer.kind === 'channel' }"
          :title="buffer.kind === 'channel' ? 'click to edit topic' : ''"
          @click="startEditTopic"
        >{{ buffer.topic || (buffer.kind === "channel" ? "(set topic…)" : "") }}</span>
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
          <template v-for="(r, i) in rows" :key="r.msg?.id || (r.events && foldKey(r.events)) || i">
            <div v-if="r.day" class="day-sep"><span>{{ r.day }}</span></div>
            <div v-if="r.msg && markerMsg && r.msg === markerMsg" class="unread-sep"><span>new messages</span></div>
            <MessageItem v-if="r.msg" :msg="r.msg" />
            <template v-else-if="r.events">
              <div class="fold" :class="{ open: isFoldOpen(r.events) }" @click="toggleFold(r.events)">
                <span class="fold-caret">{{ isFoldOpen(r.events) ? "▾" : "▸" }}</span>
                <span class="fold-summary">{{ eventSummary(r.events) }}</span>
              </div>
              <template v-if="isFoldOpen(r.events)">
                <MessageItem v-for="(ev, j) in r.events" :key="'f' + i + '-' + j" :msg="ev" />
              </template>
            </template>
          </template>
          <button
            v-if="buffer?.windowed"
            class="load-older back-to-latest"
            @click="backToLatest"
          >Back to latest messages</button>
        </div>
        <aside v-if="members.length" class="members" :class="{ open: ui.membersOpen }">
          <div class="members-head">{{ members.length }} users</div>
          <ul>
            <li
              v-for="mem in members"
              :key="mem.nick"
              :class="{
                away: mem.away,
                ignored: store.active && isIgnored(store.active.network, mem.nick),
              }"
              :title="mem.away ? mem.nick + ' (away)' : 'click to DM; right-click for more'"
              @click="openQuery(mem.nick)"
              @contextmenu="memberCtx.onContext(mem, $event)"
              @touchstart.passive="memberCtx.onTouchStart(mem, $event)"
              @touchmove.passive="memberCtx.onTouchMove($event)"
              @touchend="memberCtx.cancelLp"
              @touchcancel="memberCtx.cancelLp"
            >
              <span class="modes">{{ mem.modes }}</span><span :style="{ color: memberColor(mem.nick) }">{{ mem.nick }}</span>
            </li>
          </ul>
        </aside>

        <div
          v-if="memberCtx.state.value"
          class="ctx-menu"
          :style="{ left: memberCtx.state.value.x + 'px', top: memberCtx.state.value.y + 'px' }"
          role="menu"
        >
          <div class="ctx-header">{{ memberCtx.state.value.payload.nick }}</div>
          <button class="ctx-item" type="button" @click="ctxWhois">WHOIS</button>
          <button class="ctx-item" type="button" @click="ctxIgnore">
            {{ store.active && isIgnored(store.active.network, memberCtx.state.value.payload.nick) ? "Unignore" : "Ignore" }}
          </button>
          <button class="ctx-item" type="button" @click="ctxDM">Open DM</button>
          <div class="ctx-sep"></div>
          <button class="ctx-item" type="button" :disabled="!activeChannel()" @click="ctxMode('o')">
            {{ hasMode(memberCtx.state.value.payload, 'o') ? "−o (deop)" : "+o (op)" }}
          </button>
          <button class="ctx-item" type="button" :disabled="!activeChannel()" @click="ctxMode('h')">
            {{ hasMode(memberCtx.state.value.payload, 'h') ? "−h (unhalfop)" : "+h (halfop)" }}
          </button>
          <button class="ctx-item" type="button" :disabled="!activeChannel()" @click="ctxMode('v')">
            {{ hasMode(memberCtx.state.value.payload, 'v') ? "−v (devoice)" : "+v (voice)" }}
          </button>
          <div class="ctx-sep"></div>
          <button class="ctx-item" type="button" :disabled="!activeChannel()" @click="ctxKick">Kick</button>
          <button class="ctx-item danger" type="button" :disabled="!activeChannel()" @click="ctxKickBan">Kick + Ban</button>
        </div>
        <div v-if="dragging" class="dropzone">Drop files to upload</div>
      </div>

      <div v-if="typingText" class="typing-indicator">{{ typingText }}</div>
      <ChatInput
        ref="inputRef"
        :network="store.active?.network ?? ''"
        :buffer="buffer"
      />
    </template>
  </section>
</template>
