<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, provide, ref, watch } from "vue";
import { connection, bufKey } from "../connection";
import { settings } from "../settings";
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
const contentEl = ref<HTMLElement | null>(null);
const inputRef = ref<InstanceType<typeof ChatInput> | null>(null);
const dragging = ref(false);

const buffer = computed(() => connection.activeBuffer());

// The "new messages" divider renders just above this message (identity
// compared against the row's message). null = no divider for this buffer.
const markerMsg = computed(() => buffer.value?.unreadMarker ?? null);

// unreadCount: how many messages sit from the divider down to the live tail —
// the number shown in the jump bar. indexOf compares by reference, matching
// how the divider itself is placed (r.msg === markerMsg).
const unreadCount = computed(() => {
  const m = markerMsg.value;
  const msgs = buffer.value?.messages;
  if (!m || !msgs) return 0;
  const i = msgs.indexOf(m);
  return i < 0 ? 0 : msgs.length - i;
});

// The "jump to unread" bar floats over the top of the log when you open a
// buffer with messages from before you last read it. It hides once the user
// acts on it (jump or dismiss) while the divider itself stays; a fresh marker
// (new unread, or switching to another buffer's divider) re-shows it.
const barHidden = ref(false);
watch(markerMsg, () => (barHidden.value = false));

// markerOffscreen tracks whether the "new messages" divider is actually out of
// view — i.e. the user would have to scroll to reach it. The bar is only a
// useful affordance in that case; when the divider (and the unread lines below
// it) already fit on screen there's nothing to jump to, so we suppress it even
// for a single new message. Recomputed on scroll and after every layout change.
const markerOffscreen = ref(false);
function updateMarkerOffscreen() {
  const el = listEl.value;
  if (!el || !markerMsg.value) {
    markerOffscreen.value = false;
    return;
  }
  const sep = el.querySelector(".unread-sep") as HTMLElement | null;
  if (!sep) {
    // Divider not rendered (e.g. it sits above the loaded backlog) → off-screen.
    markerOffscreen.value = true;
    return;
  }
  const view = el.getBoundingClientRect();
  const r = sep.getBoundingClientRect();
  // Off-screen when the divider lies entirely above or below the viewport.
  markerOffscreen.value = r.bottom <= view.top || r.top >= view.bottom;
}

const showUnreadBar = computed(
  () => !!markerMsg.value && unreadCount.value > 0 && !barHidden.value && markerOffscreen.value,
);

// jumpToMarker scrolls the divider into view and releases stick-to-bottom so
// the user reads upward from where they left off. dismissMarker drops the
// divider entirely — the boundary has been acknowledged.
function jumpToMarker() {
  const sep = listEl.value?.querySelector(".unread-sep") as HTMLElement | null;
  if (sep) {
    sep.scrollIntoView({ block: "center", behavior: "smooth" });
    stick = false;
  }
  barHidden.value = true;
}
function dismissMarker() {
  if (buffer.value) buffer.value.unreadMarker = null;
  barHidden.value = true;
}

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
let lastScrollTop = 0;
function onScroll() {
  const el = listEl.value;
  if (!el) return;
  const st = el.scrollTop;
  if (st + el.clientHeight >= el.scrollHeight - 40) {
    // At (or near) the bottom — follow the tail. Also re-engages when the user
    // scrolls back down to it.
    stick = true;
  } else if (st < lastScrollTop - 2) {
    // A genuine upward scroll (wheel, drag, touch, keys) — the user wants to
    // read history, so stop following the bottom. Crucially, async content
    // growth and our own scrollToBottom only ever *increase* scrollTop, so
    // they never reach this branch and can't accidentally disengage stick.
    stick = false;
  }
  lastScrollTop = st;
  updateMarkerOffscreen();
}
function scrollToBottom() {
  const el = listEl.value;
  if (el) el.scrollTop = el.scrollHeight;
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
      scrollToBottom();
    }
    updateMarkerOffscreen();
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
    scrollToBottom();
    updateMarkerOffscreen();
  },
);
// A fresh marker (or its removal) may appear without a scroll or length change
// — recompute once the divider has rendered so the bar's visibility is right.
watch(markerMsg, async () => {
  await nextTick();
  updateMarkerOffscreen();
});

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
// Ignore is enforced server-side by the bundled ignore.lua plugin, which
// claims /ignore and /unignore and drops the nick's messages in the engine.
// The client just sends the command — the daemon is the source of truth, so
// there's no local ignore state to keep in sync.
function ctxIgnore() {
  const m = memberCtx.state.value?.payload;
  if (!m || !store.active) return;
  connection.send(store.active.network, store.active.buffer, "/ignore " + m.nick);
  memberCtx.close();
}
function ctxUnignore() {
  const m = memberCtx.state.value?.payload;
  if (!m || !store.active) return;
  connection.send(store.active.network, store.active.buffer, "/unignore " + m.nick);
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
// Keep the view pinned to the bottom as late-loading content (link-preview
// embeds, lazy images) expands *after* the initial scroll-to-bottom. Without
// this, opening a buffer whose tail holds an embed lands a little short of the
// bottom: the scroll fires at nextTick against the not-yet-laid-out content,
// then the embed finishes loading and grows the log, leaving the viewport
// above the new bottom. The observer re-pins on every such growth, but only
// while sticking — a user scrolled up to read history, or mid-jump, is never
// yanked down.
let contentRO: ResizeObserver | null = null;
onMounted(() => {
  document.addEventListener("keydown", onGlobalKeydown);
  if ("ResizeObserver" in window) {
    contentRO = new ResizeObserver(() => {
      if (stick && !(store.jump && jumpMatchesActive())) scrollToBottom();
      updateMarkerOffscreen();
    });
    if (contentEl.value) contentRO.observe(contentEl.value);
  }
  updateMarkerOffscreen();
});
onUnmounted(() => {
  document.removeEventListener("keydown", onGlobalKeydown);
  contentRO?.disconnect();
});
// contentEl unmounts/remounts when switching to the search/mentions views (it
// sits behind the chat-view v-else), so rebind the observer when it changes.
watch(contentEl, (el, prev) => {
  if (prev) contentRO?.unobserve(prev);
  if (el) contentRO?.observe(el);
});

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
      <div
        class="chat-body"
        @dragover.prevent="dragging = true"
        @dragleave.prevent="dragging = false"
        @drop.prevent="onDrop"
      >
        <div ref="listEl" class="messages" @scroll="onScroll">
          <div ref="contentEl" class="messages-content">
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
        </div>
        <div v-if="showUnreadBar" class="unread-jump" role="status">
          <button type="button" class="unread-jump-go" @click="jumpToMarker">
            ↑ {{ unreadCount }} new message{{ unreadCount === 1 ? "" : "s" }} — jump to first unread
          </button>
          <button type="button" class="unread-jump-x" aria-label="Dismiss" @click="dismissMarker">✕</button>
        </div>
        <aside v-if="members.length" class="members" :class="{ open: ui.membersOpen }">
          <div class="members-head">{{ members.length }} users</div>
          <ul>
            <li
              v-for="mem in members"
              :key="mem.nick"
              :class="{ away: mem.away }"
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
          <button class="ctx-item" type="button" @click="ctxIgnore">Ignore</button>
          <button class="ctx-item" type="button" @click="ctxUnignore">Unignore</button>
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
