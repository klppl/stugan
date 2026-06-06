import { reactive } from "vue";
import { settings } from "./settings";
import {
  T,
  type Envelope,
  type InitState,
  type MessageDTO,
  type MsgSend,
  type BacklogFetch,
  type BacklogResp,
  type ContextFetch,
  type ContextResp,
  type SearchReq,
  type SearchResp,
  type NetAdd,
  type NetRemove,
  type NetConnect,
  type NetInfoReq,
  type NetConfig,
  type ListReq,
  type ListResp,
  type ListChannel,
  type ReadMark,
  type Typing,
  type React,
  type Redact,
  type PluginInfo,
  type PluginListResp,
  type PluginAction,
  type PluginSettingReq,
  type CompleteReq,
  type CompleteRes,
  type NetworkDTO,
  type ChannelDTO,
  type MemberDTO,
  type WireError,
  type HighlightRules,
  type AliasTable,
  type FriendDTO,
  type MonitorRef,
  type MuteSet,
  type BufClose,
  type NetReorder,
  type BufReorder,
} from "./proto/events";

export interface Buffer {
  name: string;
  kind: string;
  topic: string;
  messages: MessageDTO[];
  members: MemberDTO[];
  unread: number;
  highlight: number;
  loaded: boolean;
  more: boolean;
  // backlogPending is true between a paged backlog:fetch (Load older /
  // scroll-to-top) and its reply, so scroll-driven auto-loading can't fire a
  // second overlapping request for the same page.
  backlogPending: boolean;
  // windowed is true after a jump-to-message fetch: messages holds a
  // centered window around some past point, not the live tail. While
  // windowed, incoming live messages are not appended (they'd appear as
  // a confusing gap below the window); the user exits the mode via
  // backToLatest(), which re-fetches the most recent page.
  windowed: boolean;
  // state mirrors ChannelDTO.state: an opaque per-buffer key/value bag
  // published by server-side plugins. Today the only consumer is the
  // sidebar's lock indicator, which checks state.encrypted.
  state: Record<string, string>;
  // unreadMarker references the first live message that arrived while this
  // buffer wasn't focused — the chat view renders a "new messages" divider
  // just above it. It's a reference into messages[] (compared by identity),
  // set when unread goes 0→1 and held until the user navigates away from the
  // buffer. null means no divider. Client-only; not persisted.
  unreadMarker: MessageDTO | null;
  // markerPending bridges the server-seeded unread *count* (from the init
  // snapshot / a badge on a never-opened buffer) to an unreadMarker once the
  // buffer's messages load. It holds the unread total captured at the moment
  // the buffer was opened — the count is then zeroed, but anchorPendingMarker
  // uses it to place the divider above messages[len - markerPending]. 0 = none.
  markerPending: number;
}

export interface Network {
  id: string;
  name: string;
  nick: string;
  state: string;
  caps: string[]; // negotiated IRCv3 caps (gates reaction/redaction UI)
  buffers: Buffer[];
  friends: FriendDTO[]; // MONITOR list with live presence (sidebar friends)
}

export type View = "chat" | "mentions" | "search";

// Toast is a transient, dismissable notice shown in a corner overlay. Today
// the only producer is the s2c `error` frame (server.route's sendError),
// which would otherwise be silently dropped; the `id` is a client-local
// sequence used as the v-for key and dismissal handle.
export interface Toast {
  id: number;
  code: string;
  message: string;
}

// Jump is a pending "scroll to this message" request: when set, ChatView
// will switch to (network, buffer) and scroll the message with this id
// into view, flashing it briefly. If the message isn't already in the
// loaded buffer, a single windowed backlog:fetch (around=time) brings in
// context on both sides of it; the watcher retries once the reply lands.
// fetched flips true after the around-fetch is sent so we don't loop.
export interface Jump {
  network: string;
  buffer: string;
  id: string;
  time: string;
  fetched: boolean;
}

// MentionContext is the inline chat surrounding one mention or search hit,
// fetched on demand when its row is expanded and keyed by the anchor message
// id. open drives the disclosure; loading is true while the context:fetch is
// in flight; messages is the surrounding window (oldest-first, including the
// anchor line itself). Client-only; discarded on reload.
export interface MentionContext {
  open: boolean;
  loading: boolean;
  messages: MessageDTO[];
}

export interface Store {
  status: "connecting" | "open" | "closed";
  server: string;
  caps: string[];
  networks: Network[];
  active: { network: string; buffer: string } | null;
  view: View;
  mentions: MessageDTO[];
  // Inline expand/collapse context for mention and search rows, keyed by the
  // anchor message id (shared between the two lists since ids are unique).
  context: Record<string, MentionContext>;
  search: { query: string; results: MessageDTO[]; busy: boolean };
  netConfigs: Record<string, NetConfig>; // network id → settings (from net:info)
  channelList: { network: string; channels: ListChannel[]; busy: boolean };
  typing: Record<string, string[]>; // bufKey → nicks currently typing
  // reactions: msgid → reaction emoji → nicks who reacted. Ephemeral
  // (session-lived), keyed globally by msgid since those are unique.
  reactions: Record<string, Record<string, string[]>>;
  jump: Jump | null;
  // Transient error/notice overlay; appended by the s2c `error` handler and
  // auto-dismissed after a few seconds (or by the user). See Toast.
  toasts: Toast[];
  // Plugins known to the server, for the Settings plugin manager. Populated
  // on demand (listPlugins) and refreshed after every load/unload/reload.
  plugins: PluginInfo[];
  // Highlight ruleset (regex patterns + exceptions), server-persisted per user.
  // Seeded from init and updated by the highlight settings form.
  highlight: HighlightRules;
  // Command aliases (name → expansion), server-persisted per user. Seeded from
  // init and updated by the aliases settings form.
  aliases: Record<string, string>;
  // Muted buffers as bufKey strings (network + U+001F + lowercased buffer).
  // Server-authoritative: seeded from init, toggled via the mute frame, and
  // shared across the user's devices. A muted buffer shows no badge and fires
  // no notification.
  muted: string[];
}

function emptyBuffer(c: Partial<ChannelDTO> & { name: string }): Buffer {
  return {
    name: c.name,
    kind: c.kind ?? (isChannel(c.name) ? "channel" : "query"),
    topic: c.topic ?? "",
    messages: [],
    members: c.members ?? [],
    unread: 0,
    highlight: 0,
    loaded: false,
    more: false,
    backlogPending: false,
    windowed: false,
    state: c.state ?? {},
    unreadMarker: null,
    markerPending: 0,
  };
}

function isChannel(name: string): boolean {
  return /^[#&+!]/.test(name);
}

// Conversational kinds are real chat lines (vs. system notices and the
// join/part/quit/nick membership churn). Only these mark a buffer unread,
// trip the ignore filter, and anchor the unread divider.
const CONVERSATIONAL = new Set(["privmsg", "notice", "action"]);

// bufKey is the in-memory composite key for a (network, buffer) pair, used for
// the muted set, typing map, and read-mark timers. The separator is U+001F
// (ASCII unit separator): like NUL it can never appear in a network id or IRC
// target, but unlike NUL it keeps this file plain text so Git diff/blame and
// grep keep working. It is never persisted in this form — server state is
// always {network, buffer} pairs, rebuilt through bufKey on each load.
function bufKey(network: string, buffer: string): string {
  return network + "\x1f" + buffer.toLowerCase();
}

// The last buffer the user had open, persisted per-browser so reopening stugan
// lands where they left off instead of always snapping to the first channel.
// It's a hint only: ensureActive() validates it against the live network tree
// (and a different user's networks won't match), falling back to the first
// buffer when stale, so it never points somewhere that no longer exists.
const LAST_ACTIVE_KEY = "stugan.lastActive";

function loadLastActive(): { network: string; buffer: string } | null {
  try {
    const raw = localStorage.getItem(LAST_ACTIVE_KEY);
    if (!raw) return null;
    const v = JSON.parse(raw);
    if (v && typeof v.network === "string" && typeof v.buffer === "string") return v;
  } catch {
    // ignore malformed/unavailable storage
  }
  return null;
}

function saveLastActive(active: { network: string; buffer: string } | null) {
  try {
    if (active) localStorage.setItem(LAST_ACTIVE_KEY, JSON.stringify(active));
    else localStorage.removeItem(LAST_ACTIVE_KEY);
  } catch {
    // ignore unavailable storage
  }
}

// Heartbeat timing. While the socket is open the client sends an app-level
// ping every PING_INTERVAL_MS and expects any frame back within PONG_TIMEOUT_MS;
// continued silence means the link is dead and it reconnects. RECONNECT_DELAY_MS
// is the backoff after a clean close before retrying.
const PING_INTERVAL_MS = 20000;
const PONG_TIMEOUT_MS = 10000;
const RECONNECT_DELAY_MS = 1500;

export class Connection {
  readonly store: Store = reactive({
    status: "connecting",
    server: "",
    caps: [],
    networks: [],
    active: null,
    view: "chat",
    mentions: [],
    context: {},
    search: { query: "", results: [], busy: false },
    netConfigs: {},
    channelList: { network: "", channels: [], busy: false },
    typing: {},
    reactions: {},
    jump: null,
    toasts: [],
    plugins: [],
    highlight: { patterns: [], exceptions: [] },
    aliases: {},
    muted: [],
  });

  private ws: WebSocket | null = null;
  private reconnectTimer: number | null = null;
  // A buffer to land on once it exists, set by navigateTo (notification click /
  // deep link) and consumed by ensureActive.
  private pendingNav: { network: string; buffer: string } | null = null;
  // Liveness machinery. The browser never surfaces protocol ping/pong to JS and
  // won't fire onclose on a half-open socket (common when a mobile tab is
  // suspended and the TCP flow dies silently), so we run our own heartbeat:
  // pingTimer fires the periodic ping, pongTimer is the watchdog disarmed by any
  // inbound frame. lifecycleBound guards one-time visibilitychange/online wiring.
  private pingTimer: number | null = null;
  private pongTimer: number | null = null;
  private lifecycleBound = false;
  // everConnected gates reconnect re-sync: the first open is a fresh load (init
  // populates everything), but every open after is a reconnect where the visible
  // buffer may have missed messages while we were away. resyncPending carries
  // that across to applyInit, which arrives just after onopen.
  private everConnected = false;
  private resyncPending = false;
  private typingTimers: Record<string, ReturnType<typeof setTimeout>> = {};
  private readMarkTimers: Record<string, ReturnType<typeof setTimeout>> = {};
  private lastTypingSent = 0;
  private toastSeq = 0;
  // Outstanding plugin completion requests, keyed by the seq we sent. Each
  // resolver is fulfilled by the matching complete:res frame (or by a timeout
  // that resolves [] so a dropped reply never leaks a pending promise).
  private completionSeq = 0;
  private completionWaiters = new Map<number, (items: string[]) => void>();

  connect() {
    this.bindLifecycle();
    this.clearReconnect();
    const scheme = location.protocol === "https:" ? "wss" : "ws";
    const ws = new WebSocket(`${scheme}://${location.host}/ws`);
    this.ws = ws;
    this.store.status = "connecting";
    ws.onopen = () => this.onOpen();
    ws.onclose = () => this.onClose();
    ws.onerror = () => ws.close();
    ws.onmessage = (ev) => this.onFrame(JSON.parse(ev.data) as Envelope);
  }

  private onOpen() {
    this.store.status = "open";
    // A reconnect (not the first open): the init snapshot that follows carries
    // no message bodies and applyNetwork keeps already-loaded buffers, so the
    // gap that accrued while disconnected wouldn't fill on its own. Flag it so
    // applyInit re-syncs the buffers (mirrors a page refresh).
    if (this.everConnected) this.resyncPending = true;
    this.everConnected = true;
    this.startHeartbeat();
  }

  private onClose() {
    this.store.status = "closed";
    this.stopHeartbeat();
    this.scheduleReconnect();
  }

  // bindLifecycle wires the tab-visibility and network-online events once, so
  // returning to a backgrounded mobile tab (the usual silent-death case) or
  // regaining connectivity triggers an immediate liveness check instead of
  // waiting on a heartbeat that browsers throttle while hidden.
  private bindLifecycle() {
    if (this.lifecycleBound) return;
    this.lifecycleBound = true;
    if (typeof document !== "undefined") {
      document.addEventListener("visibilitychange", () => {
        if (document.visibilityState === "visible") this.checkLiveness();
      });
    }
    if (typeof window !== "undefined") {
      window.addEventListener("online", () => this.checkLiveness());
    }
  }

  // checkLiveness runs when the tab regains focus or the network returns. A
  // socket that isn't open is reconnected at once (don't wait out the backoff);
  // one that claims to be open is probed with a ping + watchdog, since a
  // foregrounded-but-half-open socket looks open but is dead.
  private checkLiveness() {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
      this.reconnectNow();
      return;
    }
    this.sendPing();
  }

  private startHeartbeat() {
    this.stopHeartbeat();
    this.pingTimer = window.setInterval(() => this.sendPing(), PING_INTERVAL_MS);
  }

  private stopHeartbeat() {
    if (this.pingTimer != null) {
      clearInterval(this.pingTimer);
      this.pingTimer = null;
    }
    this.disarmPongWatchdog();
  }

  // sendPing probes the socket and arms the watchdog. Any inbound frame (the
  // pong, or ordinary traffic — see onFrame) proves the link alive and disarms
  // it; if nothing arrives within PONG_TIMEOUT_MS the socket is dead and we
  // reconnect. The watchdog is only armed once per outstanding probe.
  private sendPing() {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;
    this.sendFrame(T.Ping, {});
    if (this.pongTimer == null) {
      this.pongTimer = window.setTimeout(() => {
        this.pongTimer = null;
        this.reconnectNow();
      }, PONG_TIMEOUT_MS);
    }
  }

  private disarmPongWatchdog() {
    if (this.pongTimer != null) {
      clearTimeout(this.pongTimer);
      this.pongTimer = null;
    }
  }

  // reconnectNow tears down the current socket without waiting for onclose (a
  // half-open socket may never fire it) and connects fresh immediately.
  private reconnectNow() {
    this.clearReconnect();
    this.stopHeartbeat();
    const ws = this.ws;
    this.ws = null;
    if (ws) {
      ws.onopen = ws.onclose = ws.onerror = ws.onmessage = null;
      try {
        ws.close();
      } catch {
        /* already closing */
      }
    }
    this.store.status = "connecting";
    this.connect();
  }

  private clearReconnect() {
    if (this.reconnectTimer != null) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
  }

  hasCap(cap: string): boolean {
    return this.store.caps.includes(cap);
  }

  // hasNetCap reports whether a network negotiated an IRCv3 capability —
  // used to light up cap-gated affordances (reactions need message-tags,
  // delete needs draft/message-redaction).
  hasNetCap(network: string, cap: string): boolean {
    return this.store.networks.find((n) => n.id === network)?.caps.includes(cap) ?? false;
  }

  // nickOn returns our current nick on a network ("" if unknown), so the UI
  // can tell our own reactions apart.
  nickOn(network: string): string {
    return this.store.networks.find((n) => n.id === network)?.nick ?? "";
  }

  private scheduleReconnect() {
    if (this.reconnectTimer != null) return;
    this.reconnectTimer = window.setTimeout(() => {
      this.reconnectTimer = null;
      this.connect();
    }, RECONNECT_DELAY_MS);
  }

  private onFrame(env: Envelope) {
    // Any frame from the server proves the socket is alive — disarm the pong
    // watchdog (a dedicated pong, or just ordinary traffic, both count).
    this.disarmPongWatchdog();
    switch (env.t) {
      case T.Pong:
        break; // liveness only; the disarm above is the whole effect
      case T.Hello: {
        const d = env.d as { server: string; caps?: string[] };
        this.store.server = d.server;
        this.store.caps = d.caps ?? [];
        break;
      }
      case T.Init:
        this.applyInit(env.d as InitState);
        break;
      case T.NetUpdate:
        this.applyNetwork(env.d as NetworkDTO);
        this.ensureActive();
        break;
      case T.NetRemove:
        this.removeNetworkLocal((env.d as NetRemove).network);
        break;
      case T.NetReorder:
        this.reorderNetworksLocal((env.d as NetReorder).networks);
        break;
      case T.NetInfo: {
        const cfg = env.d as NetConfig;
        this.store.netConfigs[cfg.network] = cfg;
        break;
      }
      case T.Msg:
        this.applyMessage(env.d as MessageDTO);
        break;
      case T.Backlog:
        this.applyBacklog(env.d as BacklogResp);
        break;
      case T.Context: {
        const d = env.d as ContextResp;
        const cur = this.store.context[d.id];
        if (cur) {
          cur.messages = d.messages;
          cur.loading = false;
        }
        break;
      }
      case T.SearchResult: {
        const d = env.d as SearchResp;
        this.store.search.results = d.results;
        this.store.search.busy = false;
        break;
      }
      case T.ListResult: {
        const d = env.d as ListResp;
        this.store.channelList = { network: d.network, channels: d.channels, busy: false };
        break;
      }
      case T.React:
        this.applyReact(env.d as React);
        break;
      case T.Redact:
        this.applyRedact(env.d as Redact);
        break;
      case T.Typing:
        this.applyTyping(env.d as Typing);
        break;
      case T.PluginList:
        this.store.plugins = (env.d as PluginListResp).plugins;
        break;
      case T.Highlight:
        this.store.highlight = env.d as HighlightRules;
        break;
      case T.Aliases:
        this.store.aliases = (env.d as AliasTable).aliases ?? {};
        break;
      case T.Mute:
        this.applyMute(env.d as MuteSet);
        break;
      case T.Read: {
        // Another of the user's tabs/devices read this buffer; converge by
        // clearing the local badge. Leave the "new messages" divider in place
        // if we're actively reading the buffer here, so it isn't yanked away
        // mid-read; just drop the badge in that case.
        const d = env.d as ReadMark;
        const buf = this.buf(d.network, d.buffer);
        if (buf) {
          buf.unread = 0;
          buf.highlight = 0;
          const a = this.store.active;
          const isActive =
            !!a && a.network === d.network && a.buffer.toLowerCase() === d.buffer.toLowerCase();
          if (!isActive) buf.unreadMarker = null;
        }
        break;
      }
      case T.CompleteRes: {
        const d = env.d as CompleteRes;
        const resolve = this.completionWaiters.get(d.seq);
        if (resolve) {
          this.completionWaiters.delete(d.seq);
          resolve(d.items ?? []);
        }
        break;
      }
      case T.Error:
        this.pushToast(env.d as WireError);
        break;
      default:
        break;
    }
  }

  // pushToast surfaces a server error frame as a transient overlay notice.
  // Without this, sendError replies from server.route (bad payloads, failed
  // connects, etc.) are silently dropped and failures look like no-ops.
  private pushToast(err: WireError) {
    const id = ++this.toastSeq;
    this.store.toasts.push({ id, code: err.code, message: err.message });
    window.setTimeout(() => this.dismissToast(id), 6000);
  }

  dismissToast(id: number) {
    const i = this.store.toasts.findIndex((t) => t.id === id);
    if (i >= 0) this.store.toasts.splice(i, 1);
  }

  private applyInit(init: InitState) {
    // Highlight rules and mutes are server-authoritative; adopt them before
    // applying networks, because applyNetwork's unread adoption consults the
    // mute set. (Older servers omit these fields — fall back to empty.)
    this.store.highlight = init.highlight ?? { patterns: [], exceptions: [] };
    this.store.aliases = init.aliases?.aliases ?? {};
    this.store.muted = (init.muted ?? []).map((r) => bufKey(r.network, r.buffer));
    this.migrateLegacyMutes();
    // adoptUnread: the init snapshot carries authoritative unread/highlight
    // counts computed from the server-side read markers, so badges reflect
    // messages that arrived while the tab was closed. Live net:update frames
    // (below) don't — their counts are always 0 — so they must not clobber
    // the locally-accumulated counters.
    for (const n of init.networks) this.applyNetwork(n, true);
    const ids = new Set(init.networks.map((n) => n.id));
    this.store.networks = this.store.networks.filter((n) => ids.has(n.id));
    // On a reconnect, drop the stale loaded message state so buffers reload
    // their latest page (filling whatever arrived while we were disconnected)
    // instead of showing an old tail with an invisible gap. Done before
    // ensureActive so it re-fetches the visible buffer.
    if (this.resyncPending) {
      this.resyncPending = false;
      this.resyncBuffers();
    }
    this.ensureActive();
    // The buffer we land on is being viewed now — clear its seeded count and
    // tell the server, so it doesn't reappear as unread on the next reload.
    const a = this.store.active;
    if (a) {
      const buf = this.activeBuffer();
      if (buf) {
        // We're landing on this buffer now, but it may carry a seeded unread
        // count from messages that arrived while the tab was closed. Capture
        // it as a pending divider anchor before zeroing, so the "new messages"
        // line and the jump bar still mark where the user left off. Messages
        // aren't loaded yet on first login — anchorPendingMarker no-ops until
        // the backlog reply lands and calls it again.
        if (buf.unread > 0 && !buf.unreadMarker) buf.markerPending = buf.unread;
        buf.unread = 0;
        buf.highlight = 0;
        this.anchorPendingMarker(buf);
      }
      this.markRead(a.network, a.buffer);
    }
  }

  // resyncBuffers discards loaded message state on every buffer after a
  // reconnect, so each reloads its latest page on demand rather than showing a
  // stale tail with an invisible gap where the disconnected messages belong.
  // This mirrors a page refresh (which starts from empty buffers); ensureActive
  // then re-fetches the visible one. Server-seeded unread counts (set by
  // applyNetwork from the read markers) are left intact so badges stay correct.
  private resyncBuffers() {
    for (const n of this.store.networks) {
      for (const b of n.buffers) {
        b.messages = [];
        b.loaded = false;
        b.more = false;
        b.windowed = false;
        b.unreadMarker = null;
        b.markerPending = 0;
      }
    }
  }

  private applyNetwork(dto: NetworkDTO, adoptUnread = false) {
    let net = this.store.networks.find((n) => n.id === dto.id);
    if (!net) {
      net = { id: dto.id, name: dto.name, nick: dto.nick, state: dto.state, caps: dto.caps ?? [], buffers: [], friends: [] };
      this.store.networks.push(net);
    } else {
      net.name = dto.name;
      net.nick = dto.nick;
      net.state = dto.state;
      net.caps = dto.caps ?? [];
    }
    // Friends (MONITOR): adopt the new list + presence. On a live update (not
    // the init snapshot), toast any friend that just transitioned to online.
    const wasOnline = new Map(net.friends.map((f) => [f.nick.toLowerCase(), f.online]));
    net.friends = dto.friends ?? [];
    if (!adoptUnread) {
      for (const f of net.friends) {
        if (f.online && !wasOnline.get(f.nick.toLowerCase())) {
          this.pushToast({ code: "friend", message: `${f.nick} is online on ${net.name}` });
        }
      }
    }
    const existing = new Map(net.buffers.map((b) => [b.name.toLowerCase(), b]));
    net.buffers = dto.channels.map((c) => {
      const prev = existing.get(c.name.toLowerCase());
      const buf = prev ?? emptyBuffer(c);
      if (prev) {
        prev.kind = c.kind;
        prev.topic = c.topic;
        prev.members = c.members ?? [];
        prev.state = c.state ?? {};
      }
      // Only the init snapshot carries real unread counts; adopt them so
      // badges survive a reload. A muted buffer never shows a count (it shows
      // the mute icon instead), matching the live counter's mute skip.
      if (adoptUnread) {
        const muted = this.isMuted(bufKey(net!.id, buf.name));
        buf.unread = muted ? 0 : c.unread ?? 0;
        buf.highlight = muted ? 0 : c.highlight ?? 0;
      }
      return buf;
    });
    // ensureActive is called by our callers (applyInit after the whole network
    // loop; the NetUpdate handler after a live change), NOT here: during init
    // applyNetwork runs once per network, and selecting before the later
    // networks are added would lock onto the first network's first buffer and
    // lose a saved lastActive that points into a network not yet loaded.
  }

  private ensureActive() {
    const has = (a: { network: string; buffer: string } | null) =>
      !!a && !!this.buf(a.network, a.buffer);
    // A pending navigation (from a notification click that arrived before the
    // buffer existed, or a cold-start deep link) wins over the last-active
    // fallback once its buffer has materialized in the snapshot.
    if (this.pendingNav && has(this.pendingNav)) {
      this.select(this.pendingNav.network, this.pendingNav.buffer);
      this.pendingNav = null;
      return;
    }
    if (!has(this.store.active)) {
      // Prefer the buffer the user last had open (persisted across reloads);
      // fall back to the first buffer of the first network with any.
      const last = loadLastActive();
      if (has(last)) {
        this.store.active = last;
      } else {
        const first = this.store.networks.find((n) => n.buffers.length > 0);
        this.store.active = first ? { network: first.id, buffer: first.buffers[0].name } : null;
      }
    }
    const buf = this.activeBuffer();
    if (buf && !buf.loaded) this.fetchBacklog(this.store.active!.network, buf.name);
  }

  // net returns the network with the given id, or undefined.
  private net(id: string): Network | undefined {
    return this.store.networks.find((n) => n.id === id);
  }

  // buf returns the buffer named `buffer` in network `network`, matched
  // case-insensitively (IRC buffer names fold case, so #Chan and #chan are the
  // same buffer), or undefined if either is unknown.
  private buf(network: string, buffer: string): Buffer | undefined {
    const lc = buffer.toLowerCase();
    return this.net(network)?.buffers.find((b) => b.name.toLowerCase() === lc);
  }

  // ensureBuf returns the named buffer in net, creating an empty one (of kind,
  // else inferred from the name) and appending it when absent.
  private ensureBuf(net: Network, name: string, kind?: string): Buffer {
    const lc = name.toLowerCase();
    let buf = net.buffers.find((b) => b.name.toLowerCase() === lc);
    if (!buf) {
      buf = emptyBuffer(kind ? { name, kind } : { name });
      net.buffers.push(buf);
    }
    return buf;
  }

  private applyMessage(m: MessageDTO) {
    const net = this.net(m.network);
    if (!net) return;
    // Per-nick ignore is enforced server-side by the bundled ignore.lua
    // plugin (it drops the message in the engine, so it is never stored,
    // counted, or highlighted). Nothing to filter here.
    const buf = this.ensureBuf(net, m.buffer);
    // While reading a jumped-to window, don't append live messages: they
    // would appear directly below the window with an invisible gap of
    // un-fetched messages between them. Counters (unread/highlight/
    // mentions) still update so the user knows new activity arrived.
    if (!buf.windowed) buf.messages.push(m);
    if (!m.self) this.clearTyping(bufKey(net.id, buf.name), m.from); // they sent → not typing

    const muted = this.isMuted(bufKey(net.id, buf.name));
    const focused =
      this.store.view === "chat" &&
      this.store.active?.network === net.id &&
      this.store.active?.buffer.toLowerCase() === buf.name.toLowerCase();

    if (!focused && !m.self && CONVERSATIONAL.has(m.kind) && !muted) {
      // First unread since last read → anchor the "new messages" divider just
      // above this message (the freshly-pushed live tail element). Hold it
      // there as more unreads pile below; cleared when the user navigates away.
      if (buf.unread === 0 && !buf.unreadMarker && !buf.windowed && buf.messages.length) {
        buf.unreadMarker = buf.messages[buf.messages.length - 1];
      }
      buf.unread++;
      if (m.highlight) buf.highlight++;
    } else if (focused && !m.self && CONVERSATIONAL.has(m.kind)) {
      // Viewing the buffer as this arrives — it's read. Advance the persisted
      // marker (debounced) so it isn't re-counted as unread on the next reload.
      this.markReadSoon(net.id, buf.name);
    }
    if (m.highlight && !m.self && !muted) {
      this.store.mentions.push(m);
      if (this.store.mentions.length > 200) this.store.mentions.shift();
    }
    // Desktop-notify on highlights and on direct messages (queries). DMs stay
    // out of the mentions view above but are still attention-worthy; this
    // mirrors the server-side push gate in maybePush.
    if (!m.self && !muted && (m.highlight || (buf.kind === "query" && CONVERSATIONAL.has(m.kind)))) {
      this.desktopNotify(m);
    }
    if (!this.store.active) this.store.active = { network: net.id, buffer: buf.name };
  }

  private applyBacklog(resp: BacklogResp) {
    const net = this.net(resp.network);
    if (!net) return;
    const buf = this.ensureBuf(net, resp.buffer);
    buf.loaded = true;
    buf.more = resp.more;
    buf.backlogPending = false; // a reply landed — release the auto-load guard
    if (resp.around) {
      // Windowed reply: discard whatever was previously loaded (live tail
      // or another window) and show the new context window in full.
      buf.messages = [...resp.messages];
      buf.windowed = true;
      return;
    }
    // Normal paged-backward reply: prepend the older slice we didn't have.
    const have = new Set(buf.messages.map((m) => m.id).filter(Boolean));
    const older = resp.messages.filter((m) => !m.id || !have.has(m.id));
    buf.messages.unshift(...older);
    // The first page for a buffer opened with a seeded unread count: now that
    // we have messages, drop the divider where the user left off.
    this.anchorPendingMarker(buf);
  }

  // anchorPendingMarker turns a captured markerPending count (the unread total
  // at the moment a buffer was opened) into an actual unreadMarker once the
  // buffer's messages are loaded, anchoring the "new messages" divider above
  // the first message that was unread when the user arrived. No-op while
  // nothing is pending, the buffer is windowed, or the backlog hasn't landed
  // yet — in that case applyBacklog calls this again once it does. When more is
  // unread than the loaded page holds, the divider clamps to the top and the
  // user can "Load older" for the rest.
  private anchorPendingMarker(buf: Buffer) {
    if (buf.markerPending <= 0 || buf.windowed || !buf.messages.length) return;
    const idx = Math.max(0, buf.messages.length - buf.markerPending);
    buf.unreadMarker = buf.messages[idx];
    buf.markerPending = 0;
  }

  // navigateTo opens a buffer by name, e.g. from a clicked push notification.
  // If the buffer isn't in the store yet (cold start before the init snapshot),
  // it's stashed and applied by ensureActive once the snapshot arrives.
  navigateTo(network: string, buffer: string) {
    if (this.buf(network, buffer)) this.select(network, buffer);
    else this.pendingNav = { network, buffer };
  }

  select(network: string, buffer: string) {
    // Leaving the current buffer clears its divider — it marked where we left
    // off; once we navigate away it's read. The buffer we're switching TO
    // keeps any divider set while it was unfocused, so we land on it.
    const prev = this.activeBuffer();
    if (prev && (prev.name.toLowerCase() !== buffer.toLowerCase() || this.store.active?.network !== network)) {
      prev.unreadMarker = null;
    }
    this.store.view = "chat";
    this.store.active = { network, buffer };
    saveLastActive(this.store.active); // remember where to land on next reload
    const buf = this.activeBuffer();
    if (buf) {
      if (buf === prev) buf.unreadMarker = null; // re-selecting the same buffer clears it
      // Opening a buffer that carried only a server-seeded unread badge (read
      // last session, never opened since): anchor a divider where we left off
      // so the jump bar can offer to scroll there. A buffer that already has a
      // live unreadMarker (set while it was unfocused) keeps that precise
      // anchor — don't overwrite it with the coarser count-based one.
      if (buf.unread > 0 && !buf.unreadMarker) buf.markerPending = buf.unread;
      buf.unread = 0;
      buf.highlight = 0;
      buf.backlogPending = false; // recover if a prior backlog request errored out
      if (!buf.loaded) this.fetchBacklog(network, buffer);
      else this.anchorPendingMarker(buf);
    }
    // Persist the read position so the badge doesn't reappear after a reload.
    this.markRead(network, buffer);
  }

  // markRead tells the server the user has read (network, buffer) up to now,
  // advancing the persisted read marker that seeds unread badges on reload.
  // The server stamps the time, so this is a bare notice with no payload time.
  markRead(network: string, buffer: string) {
    clearTimeout(this.readMarkTimers[bufKey(network, buffer)]);
    this.sendFrame<ReadMark>(T.Read, { network, buffer });
  }

  // markReadSoon debounces a markRead — used while a buffer is focused and
  // messages keep arriving, so the marker advances past lines the user is
  // actively watching (which never increment unread) without a frame per line.
  private markReadSoon(network: string, buffer: string) {
    const key = bufKey(network, buffer);
    clearTimeout(this.readMarkTimers[key]);
    this.readMarkTimers[key] = setTimeout(() => this.markRead(network, buffer), 1000);
  }

  showMentions() {
    this.store.view = "mentions";
  }

  // jumpToMessage navigates to the buffer that contains m and asks the
  // chat view to scroll m into view. The view watcher in ChatView calls
  // fetchAround() if the message isn't already in the loaded buffer.
  // Used by the clickable Mentions and Search rows.
  jumpToMessage(m: MessageDTO) {
    if (!m.id || !m.time) return; // need a stable id + time anchor
    this.store.jump = {
      network: m.network, buffer: m.buffer, id: m.id, time: m.time, fetched: false,
    };
    this.select(m.network, m.buffer);
  }

  // toggleContext expands or collapses the inline chat surrounding a mention
  // or search result. The first open fetches a window of messages around the
  // anchor (context:fetch → applyContext); later toggles just flip the
  // disclosure without refetching. Keyed by the anchor id, so reopening is
  // instant and two rows for the same line stay in step.
  toggleContext(m: MessageDTO) {
    if (!m.id || !m.time) return; // need a stable id + time anchor
    const cur = this.store.context[m.id];
    if (cur) {
      cur.open = !cur.open;
      return;
    }
    this.store.context[m.id] = { open: true, loading: true, messages: [] };
    this.sendFrame<ContextFetch>(T.ContextFetch, {
      network: m.network, buffer: m.buffer, id: m.id, around: m.time, limit: 11,
    });
  }

  // contextFor returns the expansion state for a mention/search row, or null
  // if it has never been opened. Used by the template to render the disclosure.
  contextFor(m: MessageDTO): MentionContext | null {
    return (m.id && this.store.context[m.id]) || null;
  }

  // fetchAround requests a window of context centered on around (RFC3339).
  // The reply lands in applyBacklog with resp.around set, which replaces
  // buf.messages with the window and flips buf.windowed = true.
  fetchAround(network: string, buffer: string, around: string) {
    this.sendFrame<BacklogFetch>(T.BacklogFetch, { network, buffer, around, limit: 100 });
  }

  // backToLatest exits windowed mode for the given buffer by re-fetching
  // the most recent page. Used by the "Back to latest" affordance shown
  // beneath the chat list while buf.windowed is true.
  backToLatest(network: string, buffer: string) {
    const buf = this.buf(network, buffer);
    if (!buf) return;
    buf.messages = [];
    buf.windowed = false;
    buf.loaded = false;
    buf.more = false;
    this.fetchBacklog(network, buffer);
  }

  clearJump() {
    this.store.jump = null;
  }

  // openQuery opens (creating if needed) a private query with a nick.
  openQuery(network: string, nick: string) {
    const net = this.net(network);
    if (!net) return;
    const buf = this.ensureBuf(net, nick, "query");
    this.select(network, buf.name);
  }

  fetchBacklog(network: string, buffer: string, before?: string) {
    const buf = this.buf(network, buffer);
    if (buf) {
      if (!before) buf.loaded = true;
      else buf.backlogPending = true; // cleared in applyBacklog when the page lands
    }
    this.sendFrame<BacklogFetch>(T.BacklogFetch, { network, buffer, before, limit: 100 });
  }

  loadOlder(network: string, buffer: string) {
    const buf = this.buf(network, buffer);
    if (!buf || !buf.more || buf.backlogPending || buf.messages.length === 0) return;
    this.fetchBacklog(network, buffer, buf.messages[0].time);
  }

  search(query: string) {
    query = query.trim();
    if (!query) return;
    this.store.view = "search";
    this.store.search = { query, results: [], busy: true };
    const scope = this.store.active;
    this.sendFrame<SearchReq>(T.Search, {
      query,
      network: scope?.network,
      limit: 100,
    });
  }

  send(network: string, buffer: string, text: string) {
    this.sendFrame<MsgSend>(T.MsgSend, { network, buffer, text });
  }

  // addNetwork asks the server to add and connect a network at runtime.
  addNetwork(params: NetAdd) {
    this.sendFrame<NetAdd>(T.NetAdd, params);
  }

  // removeNetwork asks the server to disconnect and forget a network.
  removeNetwork(name: string) {
    this.sendFrame<NetRemove>(T.NetRemove, { network: name });
  }

  // setConnected connects or disconnects a network without forgetting it.
  setConnected(network: string, connect: boolean) {
    this.sendFrame<NetConnect>(T.NetConnect, { network, connect });
  }

  // closeBuffer closes a query/DM buffer. The server removes it from state and
  // re-broadcasts the network (net:update) without it — applyNetwork then drops
  // it here and re-selects an active buffer, converging every tab. Channels are
  // left via /part instead, so this is only wired up for query buffers.
  closeBuffer(network: string, buffer: string) {
    this.sendFrame<BufClose>(T.BufClose, { network, buffer });
  }

  // reorderNetworks tells the server the new manual network order (full id
  // list, display order). The store is reordered optimistically by the caller;
  // the server persists and echoes a net:reorder to every tab.
  reorderNetworks(ids: string[]) {
    this.sendFrame<NetReorder>(T.NetReorder, { networks: ids });
  }

  // reorderBuffers tells the server the new manual buffer order within a
  // network (display names, status buffer omitted). The server persists it and
  // re-broadcasts the network (net:update).
  reorderBuffers(network: string, buffers: string[]) {
    this.sendFrame<BufReorder>(T.BufReorder, { network, buffers });
  }

  // isMuted reports whether a buffer (by bufKey) is muted. Muted buffers show
  // no unread badge and fire no notification.
  isMuted(key: string): boolean {
    return this.store.muted.includes(key);
  }

  // toggleMute flips a buffer's muted state, updating the store optimistically
  // (snappy local feedback) and asking the server to persist it. The server
  // broadcasts the absolute new state back to every tab — applyMute makes that
  // a no-op here and brings other tabs/devices into sync.
  toggleMute(network: string, buffer: string) {
    const muted = !this.isMuted(bufKey(network, buffer));
    this.applyMute({ network, buffer, muted });
    this.sendFrame<MuteSet>(T.Mute, { network, buffer, muted });
  }

  // applyMute sets a buffer's muted state to an absolute value (not a toggle),
  // so it converges no matter how many times it's applied — used for both the
  // optimistic local update and the server's broadcast to the user's tabs.
  private applyMute(m: MuteSet) {
    const key = bufKey(m.network, m.buffer);
    const i = this.store.muted.indexOf(key);
    if (m.muted && i < 0) this.store.muted.push(key);
    else if (!m.muted && i >= 0) this.store.muted.splice(i, 1);
  }

  // setHighlight replaces the highlight ruleset. The server validates the
  // regexes, persists them, and echoes the normalized rules back (a highlight
  // frame), which refreshes store.highlight; a bad regex returns an error.
  setHighlight(patterns: string[], exceptions: string[]) {
    this.sendFrame<HighlightRules>(T.HighlightSet, { patterns, exceptions });
  }

  // addFriend / removeFriend manage a network's MONITOR list. The server arms
  // MONITOR, persists the list, and re-broadcasts the network (net:update),
  // which refreshes net.friends here.
  addFriend(network: string, nick: string) {
    this.sendFrame<MonitorRef>(T.MonitorAdd, { network, nick });
  }

  removeFriend(network: string, nick: string) {
    this.sendFrame<MonitorRef>(T.MonitorRemove, { network, nick });
  }

  // setAliases replaces the command-alias table. The server normalizes names,
  // drops invalid entries, persists the table, and echoes it back (an aliases
  // frame), which refreshes store.aliases.
  setAliases(aliases: Record<string, string>) {
    this.sendFrame<AliasTable>(T.AliasSet, { aliases });
  }

  // migrateLegacyMutes carries forward per-channel mutes that older builds
  // stored in localStorage (under stugan.settings.muted) to the server, once.
  // It runs after the server-authoritative set is seeded, so it only adds
  // entries the server doesn't already have, then drops the local copy.
  private migrateLegacyMutes() {
    let raw: string | null;
    try {
      raw = localStorage.getItem("stugan.settings");
    } catch {
      return;
    }
    if (!raw) return;
    let parsed: { muted?: unknown };
    try {
      parsed = JSON.parse(raw);
    } catch {
      return;
    }
    const old = Array.isArray(parsed.muted) ? (parsed.muted as string[]) : [];
    for (const key of old) {
      if (this.store.muted.includes(key)) continue;
      // bufKey is `network + " " + buffer`; IRC targets never contain spaces,
      // so the buffer is the final token and the network is everything before.
      const i = key.lastIndexOf(" ");
      if (i <= 0) continue;
      this.toggleMute(key.slice(0, i), key.slice(i + 1));
    }
    // Drop the migrated field so this only happens once.
    try {
      delete parsed.muted;
      localStorage.setItem("stugan.settings", JSON.stringify(parsed));
    } catch {
      /* best-effort */
    }
  }

  // sendTyping notifies the server the user is typing (throttled). state is
  // "active" (keystroke) or "done" (sent/cleared).
  sendTyping(network: string, buffer: string, state: "active" | "done") {
    // Opt-in: broadcasting typing lets others see when you're composing, so it
    // stays off unless the user enables it (mirrors the default-off reactions).
    if (!settings.sendTyping) return;
    // Typing rides the +typing client tag on TAGMSG, which needs message-tags.
    // Skip networks that didn't negotiate it: the server drops these anyway,
    // but gating here avoids pointless frames and keeps the behaviour
    // consistent with reactions/redaction (no affordance when unsupported).
    if (!this.hasNetCap(network, "message-tags")) return;
    if (state === "active") {
      const now = Date.now();
      if (now - this.lastTypingSent < 3000) return; // throttle active pings
      this.lastTypingSent = now;
    } else {
      this.lastTypingSent = 0;
    }
    this.sendFrame<Typing>(T.Typing, { network, buffer, state });
  }

  private applyTyping(t: Typing) {
    if (!t.nick || !settings.showTyping) return;
    const key = bufKey(t.network, t.buffer);
    if (t.state === "done") {
      this.clearTyping(key, t.nick);
      return;
    }
    const list = this.store.typing[key] ?? [];
    if (!list.includes(t.nick)) this.store.typing[key] = [...list, t.nick];
    // Auto-expire if no refresh arrives within 6s.
    const tk = key + "\0" + t.nick;
    clearTimeout(this.typingTimers[tk]);
    this.typingTimers[tk] = setTimeout(() => this.clearTyping(key, t.nick!), 6000);
  }

  private clearTyping(key: string, nick: string) {
    const list = this.store.typing[key];
    if (list) this.store.typing[key] = list.filter((n) => n.toLowerCase() !== nick.toLowerCase());
    clearTimeout(this.typingTimers[key + "\0" + nick]);
  }

  // applyReact toggles a reaction in the store. A repeated (nick, reaction)
  // pair removes it — IRCv3 reactions are add-only on the wire, so we treat a
  // re-send as a toggle, which matches how the affordance is clicked.
  private applyReact(r: React) {
    if (!r.target || !r.reaction || !r.nick) return;
    const byEmoji = (this.store.reactions[r.target] ??= {});
    const nicks = byEmoji[r.reaction] ?? [];
    const i = nicks.findIndex((n) => n.toLowerCase() === r.nick!.toLowerCase());
    if (i >= 0) {
      nicks.splice(i, 1);
      if (nicks.length) byEmoji[r.reaction] = nicks;
      else delete byEmoji[r.reaction];
    } else {
      byEmoji[r.reaction] = [...nicks, r.nick];
    }
    if (!Object.keys(byEmoji).length) delete this.store.reactions[r.target];
  }

  // applyRedact removes the redacted message from its buffer and drops any
  // reactions it carried.
  private applyRedact(r: Redact) {
    const buf = this.buf(r.network, r.buffer);
    if (buf) {
      const i = buf.messages.findIndex((m) => m.id && m.id === r.target);
      if (i >= 0) {
        if (buf.unreadMarker === buf.messages[i]) buf.unreadMarker = null;
        buf.messages.splice(i, 1);
      }
    }
    delete this.store.reactions[r.target];
  }

  // react sends an emoji reaction to a message (toggles on re-send). The
  // server echoes it back, which is what updates the store.
  react(network: string, buffer: string, target: string, reaction: string) {
    this.sendFrame<React>(T.React, { network, buffer, target, reaction });
  }

  // redact asks the server to delete a message (one we sent, or any if we're
  // an op). The buffer updates when the REDACT is relayed back.
  redact(network: string, buffer: string, target: string, reason?: string) {
    this.sendFrame<Redact>(T.Redact, { network, buffer, target, reason });
  }

  // listChannels requests the channel browser for a network. query is passed
  // to the server's LIST (e.g. ">50" or "*term*").
  listChannels(network: string, query: string) {
    this.store.channelList = { network, channels: [], busy: true };
    this.sendFrame<ListReq>(T.List, { network, query: query || undefined });
  }

  // listPlugins asks the server for the current plugin list (reply lands in
  // store.plugins via the plugin:list handler).
  listPlugins() {
    this.sendFrame(T.PluginList, {});
  }

  // pluginAction loads, unloads, or reloads a plugin by name. The server
  // replies with a refreshed plugin:list, so the UI updates itself.
  pluginAction(name: string, action: PluginAction["action"]) {
    this.sendFrame<PluginAction>(T.PluginAction, { name, action });
  }

  // setPluginSetting changes one declared setting of a loaded plugin. The
  // server validates it, persists it, runs the plugin's apply callback, and
  // replies with a refreshed plugin:list so the form reflects the new value.
  setPluginSetting(name: string, key: string, value: string) {
    this.sendFrame<PluginSettingReq>(T.PluginSet, { name, key, value });
  }

  // desktopNotify shows a desktop notification for a highlight when the tab
  // is in the background and permission has been granted.
  private desktopNotify(m: MessageDTO) {
    if (typeof Notification === "undefined" || Notification.permission !== "granted") return;
    if (!document.hidden) return;
    try {
      // A DM's buffer name is just the sender's nick, so "alice in alice" reads
      // badly — title it with the sender alone.
      const title = isChannel(m.buffer) ? `${m.from} in ${m.buffer}` : m.from;
      new Notification(title, { body: m.text, tag: m.network + "/" + m.buffer });
    } catch {
      /* notifications may be unavailable */
    }
  }

  // requestNetInfo fetches a network's current settings (reply populates
  // store.netConfigs[id]).
  requestNetInfo(id: string) {
    this.sendFrame<NetInfoReq>(T.NetInfo, { network: id });
  }

  // requestCompletions asks the server's plugin host for tab-completion
  // candidates for `word`. Resolves with the candidate list; resolves [] if
  // the socket is down or no reply arrives within a short window (so the
  // caller's await never hangs and stale waiters are reaped).
  requestCompletions(network: string, buffer: string, word: string): Promise<string[]> {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return Promise.resolve([]);
    const seq = ++this.completionSeq;
    return new Promise<string[]>((resolve) => {
      this.completionWaiters.set(seq, resolve);
      window.setTimeout(() => {
        if (this.completionWaiters.delete(seq)) resolve([]);
      }, 1500);
      this.sendFrame<CompleteReq>(T.CompleteReq, { network, buffer, word, seq });
    });
  }

  // editNetwork applies edited settings; the server reconnects the network.
  editNetwork(cfg: NetConfig) {
    this.sendFrame<NetConfig>(T.NetEdit, cfg);
  }

  // removeNetworkLocal drops a network from the store on a net:remove frame.
  private removeNetworkLocal(id: string) {
    this.store.networks = this.store.networks.filter((n) => n.id !== id);
    if (this.store.active?.network === id) this.ensureActive();
  }

  // reorderNetworksLocal reorders store.networks to match the server's order
  // (a net:reorder frame). Ids not present are skipped; networks missing from
  // the list keep their relative order at the end (defensive — the server
  // always sends the full set).
  private reorderNetworksLocal(ids: string[]) {
    const byId = new Map(this.store.networks.map((n) => [n.id, n]));
    const ordered: Network[] = [];
    for (const id of ids) {
      const n = byId.get(id);
      if (n) {
        ordered.push(n);
        byId.delete(id);
      }
    }
    for (const n of byId.values()) ordered.push(n);
    this.store.networks = ordered;
  }

  private sendFrame<D>(t: string, d: D) {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;
    this.ws.send(JSON.stringify({ t, d } as Envelope<D>));
  }

  activeBuffer(): Buffer | null {
    if (!this.store.active) return null;
    return this.buf(this.store.active.network, this.store.active.buffer) ?? null;
  }

  // upload posts a file and returns its served URL.
  async upload(file: File): Promise<string | null> {
    const fd = new FormData();
    fd.append("file", file);
    try {
      const r = await fetch("/api/upload", { method: "POST", body: fd });
      if (!r.ok) return null;
      const j = (await r.json()) as { url: string };
      return location.origin + j.url;
    } catch {
      return null;
    }
  }
}

export { bufKey };
export const connection = new Connection();
