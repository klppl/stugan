import { reactive } from "vue";
import {
  T,
  type Envelope,
  type InitState,
  type MessageDTO,
  type MsgSend,
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

  // ensureActive keeps the selection pointing at a buffer that still exists.
  private ensureActive() {
    const a = this.store.active;
    if (a) {
      const net = this.store.networks.find((n) => n.id === a.network);
      if (net?.buffers.some((b) => b.name === a.buffer)) return;
    }
    const first = this.store.networks.find((n) => n.buffers.length > 0);
    this.store.active = first ? { network: first.id, buffer: first.buffers[0].name } : null;
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
    if (!this.store.active) {
      this.store.active = { network: net.id, buffer: buf.name };
    }
  }

  select(network: string, buffer: string) {
    this.store.active = { network, buffer };
  }

  send(network: string, buffer: string, text: string) {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;
    const env: Envelope<MsgSend> = {
      t: T.MsgSend,
      d: { network, buffer, text },
    };
    this.ws.send(JSON.stringify(env));
  }

  activeBuffer(): Buffer | null {
    if (!this.store.active) return null;
    const net = this.store.networks.find((n) => n.id === this.store.active!.network);
    return net?.buffers.find((b) => b.name === this.store.active!.buffer) ?? null;
  }
}

export const STATUS = STATUS_BUFFER;
export const connection = new Connection();
