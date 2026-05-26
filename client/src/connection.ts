import { reactive } from "vue";
import {
  T,
  type Envelope,
  type InitState,
  type MessageDTO,
  type MsgSend,
  type BacklogFetch,
  type BacklogResp,
  type NetworkDTO,
  type ChannelDTO,
  type MemberDTO,
} from "./proto/events";

export interface Buffer {
  name: string;
  kind: string;
  topic: string;
  messages: MessageDTO[];
  members: MemberDTO[];
  unread: number;
  highlight: number;
  loaded: boolean; // backlog requested at least once
  more: boolean; // older history available
}

export interface Network {
  id: string;
  name: string;
  nick: string;
  state: string;
  buffers: Buffer[];
}

export interface Store {
  status: "connecting" | "open" | "closed";
  server: string;
  networks: Network[];
  active: { network: string; buffer: string } | null;
}

const STATUS_BUFFER = "*status";

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
  };
}

function isChannel(name: string): boolean {
  return /^[#&+!]/.test(name);
}

export class Connection {
  readonly store: Store = reactive({
    status: "connecting",
    server: "",
    networks: [],
    active: null,
  });

  private ws: WebSocket | null = null;
  private reconnectTimer: number | null = null;

  connect() {
    const proto = location.protocol === "https:" ? "wss" : "ws";
    const ws = new WebSocket(`${proto}://${location.host}/ws`);
    this.ws = ws;
    this.store.status = "connecting";

    ws.onopen = () => {
      this.store.status = "open";
    };
    ws.onclose = () => {
      this.store.status = "closed";
      this.scheduleReconnect();
    };
    ws.onerror = () => ws.close();
    ws.onmessage = (ev) => this.onFrame(JSON.parse(ev.data) as Envelope);
  }

  private scheduleReconnect() {
    if (this.reconnectTimer != null) return;
    this.reconnectTimer = window.setTimeout(() => {
      this.reconnectTimer = null;
      this.connect();
    }, 1500);
  }

  private onFrame(env: Envelope) {
    switch (env.t) {
      case T.Hello:
        this.store.server = (env.d as { server: string }).server;
        break;
      case T.Init:
        this.applyInit(env.d as InitState);
        break;
      case T.NetUpdate:
        this.applyNetwork(env.d as NetworkDTO);
        break;
      case T.Msg:
        this.applyMessage(env.d as MessageDTO);
        break;
      case T.Backlog:
        this.applyBacklog(env.d as BacklogResp);
        break;
      default:
        // error and later-phase events ignored for now
        break;
    }
  }

  private applyInit(init: InitState) {
    // Merge each network so message history survives a reconnect, then drop
    // any network the snapshot no longer lists.
    for (const n of init.networks) this.applyNetwork(n);
    const ids = new Set(init.networks.map((n) => n.id));
    this.store.networks = this.store.networks.filter((n) => ids.has(n.id));
    this.ensureActive();
  }

  // applyNetwork reconciles a network snapshot into the store, preserving the
  // message arrays of buffers that persist across the update.
  private applyNetwork(dto: NetworkDTO) {
    let net = this.store.networks.find((n) => n.id === dto.id);
    if (!net) {
      net = { id: dto.id, name: dto.name, nick: dto.nick, state: dto.state, buffers: [] };
      this.store.networks.push(net);
    } else {
      net.name = dto.name;
      net.nick = dto.nick;
      net.state = dto.state;
    }
    const existing = new Map(net.buffers.map((b) => [b.name.toLowerCase(), b]));
    net.buffers = dto.channels.map((c) => {
      const prev = existing.get(c.name.toLowerCase());
      if (prev) {
        prev.kind = c.kind;
        prev.topic = c.topic;
        prev.members = c.members ?? [];
        return prev;
      }
      return emptyBuffer(c);
    });
    this.ensureActive();
  }

  // ensureActive keeps the selection pointing at a buffer that still exists,
  // and lazily loads backlog for whatever ends up active.
  private ensureActive() {
    const a = this.store.active;
    const stillValid =
      a && this.store.networks.find((n) => n.id === a.network)?.buffers.some((b) => b.name === a.buffer);
    if (!stillValid) {
      const first = this.store.networks.find((n) => n.buffers.length > 0);
      this.store.active = first ? { network: first.id, buffer: first.buffers[0].name } : null;
    }
    const buf = this.activeBuffer();
    if (buf && !buf.loaded) this.fetchBacklog(this.store.active!.network, buf.name);
  }

  private applyMessage(m: MessageDTO) {
    const net = this.store.networks.find((n) => n.id === m.network);
    if (!net) return;
    let buf = net.buffers.find((b) => b.name.toLowerCase() === m.buffer.toLowerCase());
    if (!buf) {
      buf = emptyBuffer({ name: m.buffer });
      net.buffers.push(buf);
    }
    buf.messages.push(m);

    // Unread/highlight tracking for buffers that aren't focused.
    const focused =
      this.store.active?.network === net.id &&
      this.store.active?.buffer.toLowerCase() === buf.name.toLowerCase();
    if (!focused && !m.self && m.kind !== "system") {
      buf.unread++;
      if (this.mentions(net.nick, m.text)) buf.highlight++;
    }

    if (!this.store.active) {
      this.store.active = { network: net.id, buffer: buf.name };
    }
  }

  private mentions(nick: string, text: string): boolean {
    if (!nick) return false;
    return new RegExp(`\\b${escapeRegExp(nick)}\\b`, "i").test(text);
  }

  private applyBacklog(resp: BacklogResp) {
    const net = this.store.networks.find((n) => n.id === resp.network);
    if (!net) return;
    let buf = net.buffers.find((b) => b.name.toLowerCase() === resp.buffer.toLowerCase());
    if (!buf) {
      buf = emptyBuffer({ name: resp.buffer });
      net.buffers.push(buf);
    }
    buf.loaded = true;
    buf.more = resp.more;
    // Backlog is older than anything loaded: prepend, skipping any message
    // we already hold (by msgid).
    const have = new Set(buf.messages.map((m) => m.id).filter(Boolean));
    const older = resp.messages.filter((m) => !m.id || !have.has(m.id));
    buf.messages.unshift(...older);
  }

  select(network: string, buffer: string) {
    this.store.active = { network, buffer };
    const buf = this.activeBuffer();
    if (buf) {
      buf.unread = 0;
      buf.highlight = 0;
      if (!buf.loaded) this.fetchBacklog(network, buffer);
    }
  }

  // fetchBacklog requests history for a buffer. With no before cursor it
  // loads the most recent page; pass the oldest loaded message's time to
  // page backward.
  fetchBacklog(network: string, buffer: string, before?: string) {
    if (!before) {
      // Mark the initial load as requested so it is not fired twice.
      const buf = this.store.networks
        .find((n) => n.id === network)
        ?.buffers.find((b) => b.name.toLowerCase() === buffer.toLowerCase());
      if (buf) buf.loaded = true;
    }
    this.sendFrame<BacklogFetch>(T.BacklogFetch, { network, buffer, before, limit: 100 });
  }

  // loadOlder fetches the page before the oldest message currently held.
  loadOlder(network: string, buffer: string) {
    const net = this.store.networks.find((n) => n.id === network);
    const buf = net?.buffers.find((b) => b.name === buffer);
    if (!buf || !buf.more || buf.messages.length === 0) return;
    this.fetchBacklog(network, buffer, buf.messages[0].time);
  }

  send(network: string, buffer: string, text: string) {
    this.sendFrame<MsgSend>(T.MsgSend, { network, buffer, text });
  }

  private sendFrame<D>(t: string, d: D) {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;
    const env: Envelope<D> = { t, d };
    this.ws.send(JSON.stringify(env));
  }

  activeBuffer(): Buffer | null {
    if (!this.store.active) return null;
    const net = this.store.networks.find((n) => n.id === this.store.active!.network);
    return net?.buffers.find((b) => b.name === this.store.active!.buffer) ?? null;
  }
}

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

export const STATUS = STATUS_BUFFER;
export const connection = new Connection();
