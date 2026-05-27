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
  Error: "error",
  MsgSend: "msg:send",
  BacklogFetch: "backlog:fetch",
  Search: "search",
  NetAdd: "net:add",
  NetEdit: "net:edit",
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
  channels: ChannelDTO[];
}

export interface ChannelDTO {
  name: string;
  kind: string;
  topic: string;
  members?: MemberDTO[];
  unread: number;
  highlight: number;
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
  limit?: number;
}

export interface BacklogResp {
  network: string;
  buffer: string;
  messages: MessageDTO[];
  more: boolean;
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
  channels?: string[];
}

export interface NetRemove {
  network: string;
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
  channels: string[];
}

export interface WireError {
  code: string;
  message: string;
}
