// Mirror of internal/proto (Go). Keep in sync by hand; see docs/protocol.md.
// Single source of truth is the Go structs.

export const PROTOCOL = 1;

// Event type discriminators (Envelope.t).
export const T = {
  Hello: "hello",
  Init: "init",
  Msg: "msg",
  NetUpdate: "net:update",
  NetRemove: "net:remove",
  NetInfo: "net:info",
  Backlog: "backlog",
  SearchResult: "search:result",
  ListResult: "list:result",
  Typing: "typing",
  React: "react",
  Redact: "redact",
  Error: "error",
  MsgSend: "msg:send",
  BacklogFetch: "backlog:fetch",
  Search: "search",
  NetAdd: "net:add",
  NetEdit: "net:edit",
  NetConnect: "net:connect",
  List: "list",
} as const;

export interface Envelope<D = unknown> {
  t: string;
  id?: string;
  d?: D;
}

export interface Hello {
  protocol: number;
  server: string;
  caps: string[];
}

export interface InitState {
  user: UserDTO;
  networks: NetworkDTO[];
}

export interface UserDTO {
  id: string;
  name: string;
}

export interface NetworkDTO {
  id: string;
  name: string;
  nick: string;
  state: string;
  caps?: string[];
  channels: ChannelDTO[];
}

export interface ChannelDTO {
  name: string;
  kind: string;
  topic: string;
  members?: MemberDTO[];
  unread: number;
  highlight: number;
  // Opaque per-buffer key/value bag set by server-side plugins (see
  // core.API.SetBufferState). The fish.lua plugin sets {"encrypted":"cbc"}
  // / "ecb" — Sidebar.vue uses that to render a lock icon. Plugin-defined;
  // unknown keys are ignored by the client.
  state?: Record<string, string>;
}

export interface MemberDTO {
  nick: string;
  modes: string;
  away: boolean;
}

export interface MessageDTO {
  id: string;
  network: string;
  buffer: string;
  time: string;
  from: string;
  kind: string;
  text: string;
  self: boolean;
  highlight?: boolean;
  tags?: Record<string, string>;
}

export interface MsgSend {
  network: string;
  buffer: string;
  text: string;
}

export interface BacklogFetch {
  network: string;
  buffer: string;
  before?: string;
  // around, when set, asks for a window of context centered on that time
  // — roughly limit/2 messages with ts ≤ around plus limit/2 strictly
  // newer. Takes precedence over `before`. Used for jump-to-message.
  around?: string;
  limit?: number;
}

export interface BacklogResp {
  network: string;
  buffer: string;
  messages: MessageDTO[];
  more: boolean;
  // Echoed from the request when this page was an Around-style window,
  // so the client can tell a centered fetch apart from a paged-backward
  // reply (they're handled differently — see Connection.applyBacklog).
  around?: string;
}

export interface SearchReq {
  query: string;
  network?: string;
  buffer?: string;
  limit?: number;
}

export interface SearchResp {
  query: string;
  results: MessageDTO[];
}

export interface NetAdd {
  name: string;
  addr: string;
  tls: boolean;
  nick: string;
  user?: string;
  realname?: string;
  sasl_user?: string;
  sasl_pass?: string;
  server_pass?: string;
  perform?: string[];
  sasl_external?: boolean;
  cert_pem?: string;
  channels?: string[];
}

export interface NetRemove {
  network: string;
}

export interface Typing {
  network: string;
  buffer: string;
  nick?: string;
  state: string; // active | paused | done
}

export interface NetConnect {
  network: string;
  connect: boolean;
}

export interface React {
  network: string;
  buffer: string;
  target: string; // msgid reacted to
  nick?: string; // who reacted (s2c)
  reaction: string;
}

export interface Redact {
  network: string;
  buffer: string;
  target: string; // msgid redacted
  by?: string; // who redacted (s2c)
  reason?: string;
}

export interface ListReq {
  network: string;
  query?: string;
}

export interface ListChannel {
  name: string;
  users: number;
  topic: string;
}

export interface ListResp {
  network: string;
  channels: ListChannel[];
}

export interface NetInfoReq {
  network: string;
}

export interface NetConfig {
  network: string;
  name: string;
  addr: string;
  tls: boolean;
  nick: string;
  user: string;
  realname: string;
  sasl_user: string;
  sasl_pass: string;
  server_pass: string;
  perform: string[];
  sasl_external: boolean;
  cert_pem: string;
  channels: string[];
}

export interface WireError {
  code: string;
  message: string;
}
