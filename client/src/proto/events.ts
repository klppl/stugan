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
  NetReorder: "net:reorder",
  NetInfo: "net:info",
  Backlog: "backlog",
  Context: "context",
  SearchResult: "search:result",
  ListResult: "list:result",
  Typing: "typing",
  React: "react",
  Redact: "redact",
  PluginList: "plugin:list",
  CompleteRes: "complete:res",
  Highlight: "highlight",
  Pong: "pong",
  Error: "error",
  MsgSend: "msg:send",
  CompleteReq: "complete:req",
  BacklogFetch: "backlog:fetch",
  ContextFetch: "context:fetch",
  Search: "search",
  NetAdd: "net:add",
  NetEdit: "net:edit",
  NetConnect: "net:connect",
  List: "list",
  PluginAction: "plugin:action",
  PluginSet: "plugin:setting",
  Read: "read",
  HighlightSet: "highlight:set",
  Mute: "mute",
  BufClose: "buf:close",
  BufReorder: "buf:reorder",
  Ping: "ping",
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
  highlight: HighlightRules;
  muted?: MuteRef[];
}

// HighlightRules is the user's highlight ruleset: case-insensitive regex
// patterns that flag a message (beyond a nick mention), and exceptions that
// suppress a would-be highlight. Delivered in init, echoed after highlight:set.
export interface HighlightRules {
  patterns: string[];
  exceptions: string[];
}

// MuteRef identifies one muted buffer (no badge, no notification). The set is
// server-persisted per user and shared across devices.
export interface MuteRef {
  network: string;
  buffer: string;
}

// MuteSet mutes (muted=true) or unmutes a buffer.
export interface MuteSet {
  network: string;
  buffer: string;
  muted: boolean;
}

// BufClose closes a query/DM buffer. The server answers by re-broadcasting the
// network (net:update) without the buffer; no dedicated reply frame.
export interface BufClose {
  network: string;
  buffer: string;
}

// NetReorder: the full network id list in display order (c2s request, s2c echo).
export interface NetReorder {
  networks: string[];
}

// BufReorder: buffer display names in order within a network (c2s).
export interface BufReorder {
  network: string;
  buffers: string[];
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

// ContextFetch asks for the window of messages surrounding one anchor
// message, so a mention/search result can be expanded inline. id is the
// anchor's id (echoed back in ContextResp); around is the anchor's time.
export interface ContextFetch {
  network: string;
  buffer: string;
  id: string;
  around: string;
  limit?: number;
}

// ContextResp answers a ContextFetch with the surrounding window,
// oldest-first. id echoes the anchor message id from the request.
export interface ContextResp {
  network: string;
  buffer: string;
  id: string;
  messages: MessageDTO[];
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

// ReadMark tells the server the user has read a buffer up to now, advancing
// the server-side read marker so unread badges survive a reload. The server
// stamps the time with its own clock, so there is no time field here.
export interface ReadMark {
  network: string;
  buffer: string;
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

export interface PluginAction {
  name: string;
  action: "load" | "unload" | "reload";
}

export interface PluginInfo {
  name: string;
  description?: string;
  loaded: boolean;
  disabled?: boolean;
  errors?: number;
  commands?: string[];
  hooks: number;
  settings?: PluginSetting[];
}

export interface PluginSetting {
  name: string;
  type: string; // "text" | "number" | "select"
  label?: string;
  help?: string;
  value: string;
  default?: string;
  secret?: boolean;
  options?: string[];
}

// PluginSettingReq sets one declared setting of a loaded plugin; the reply is
// a plugin:list frame with the refreshed list.
export interface PluginSettingReq {
  name: string;
  key: string;
  value: string;
}

export interface PluginListResp {
  plugins: PluginInfo[];
}

// CompleteReq asks the plugin host for tab-completion candidates for the
// partial `word`. `seq` lets the client discard a stale reply.
export interface CompleteReq {
  network: string;
  buffer: string;
  word: string;
  seq: number;
}

// CompleteRes returns plugin completion candidates, echoing the request seq.
export interface CompleteRes {
  seq: number;
  items: string[];
}

export interface WireError {
  code: string;
  message: string;
}
