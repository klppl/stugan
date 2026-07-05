// Package proto defines the typed JSON wire protocol shared between the
// daemon and the browser. These Go structs are the single source of truth;
// the TypeScript mirror in client/src/proto/events.ts is kept in sync by
// hand. See docs/protocol.md.
package proto

import "encoding/json"

// Protocol is the wire version advertised in Hello. Bump on a breaking
// change to any struct below.
const Protocol = 1

// Event type discriminators (the Envelope.T field). Convention: domain:verb.
const (
	THello        = "hello"         // s2c
	TInit         = "init"          // s2c
	TMsg          = "msg"           // s2c
	TNetUpdate    = "net:update"    // s2c
	TNetRemove    = "net:remove"    // s2c (and c2s)
	TNetReorder   = "net:reorder"   // s2c (and c2s) — manual network sidebar order
	TNetInfo      = "net:info"      // s2c (answers c2s net:info)
	TBacklog      = "backlog"       // s2c (answers backlog:fetch)
	TContext      = "context"       // s2c (answers context:fetch)
	TSearchResult = "search:result" // s2c (answers search)
	TListResult   = "list:result"   // s2c (answers list)
	TTyping       = "typing"        // s2c (and c2s)
	TReact        = "react"         // s2c (and c2s) — emoji reactions
	TRedact       = "redact"        // s2c (and c2s) — message redaction
	TPluginList   = "plugin:list"   // s2c (answers c2s plugin:list/plugin:action)
	TCompleteRes  = "complete:res"  // s2c (answers c2s complete:req)
	TMissedResult = "missed:result" // s2c (answers missed:fetch — "what you missed" digest)
	THighlight    = "highlight"     // s2c (current rules; answers highlight:set)
	TAliases      = "aliases"       // s2c (current alias table; answers aliases:set)
	TPong         = "pong"          // s2c (answers c2s ping; app-level liveness)
	TError        = "error"         // s2c

	TMsgSend      = "msg:send"       // c2s
	TCompleteReq  = "complete:req"   // c2s — ask plugins for tab-completion candidates
	TBacklogFetch = "backlog:fetch"  // c2s
	TContextFetch = "context:fetch"  // c2s
	TSearch       = "search"         // c2s
	TMissedFetch  = "missed:fetch"   // c2s — request the highlights missed since last read (digest)
	TNetAdd       = "net:add"        // c2s
	TNetEdit      = "net:edit"       // c2s
	TNetConnect   = "net:connect"    // c2s
	TList         = "list"           // c2s
	TPluginAction = "plugin:action"  // c2s — load/unload/reload a plugin
	TPluginSet    = "plugin:setting" // c2s — set a plugin's declared setting
	TRead         = "read"           // c2s mark a buffer read; s2c broadcast of that to the user's other tabs
	THighlightSet = "highlight:set"  // c2s — replace the highlight ruleset
	TAliasSet     = "aliases:set"    // c2s — replace the command-alias table
	TMonitorAdd   = "monitor:add"    // c2s — add a nick to a network's friends list
	TMonitorRem   = "monitor:remove" // c2s — remove a nick from a network's friends list
	TMute         = "mute"           // c2s set intent; s2c absolute state broadcast to the user's tabs
	TBufClose     = "buf:close"      // c2s — close/remove a query buffer from state
	TBufReorder   = "buf:reorder"    // c2s — manual buffer order within a network
	TPing         = "ping"           // c2s — app-level liveness probe; answered with pong
)

// Envelope is the single framing for every message in both directions. The
// router switches on T and decodes D into the matching payload struct.
type Envelope struct {
	T  string          `json:"t"`
	ID string          `json:"id,omitempty"`
	D  json.RawMessage `json:"d,omitempty"`
}

// Frame builds an Envelope of type t carrying payload d (marshaled into D).
func Frame(t string, d any) (Envelope, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{T: t, D: raw}, nil
}

// Hello is sent once on connect.
type Hello struct {
	Protocol int      `json:"protocol"`
	Server   string   `json:"server"`
	Caps     []string `json:"caps"`
}

// InitState is the authoritative full snapshot sent after Hello.
type InitState struct {
	User      UserDTO        `json:"user"`
	Networks  []NetworkDTO   `json:"networks"`
	Highlight HighlightRules `json:"highlight"`
	Aliases   AliasTable     `json:"aliases"`
	Muted     []MuteRef      `json:"muted,omitempty"`
}

// UserDTO is the wire projection of core.User.
type UserDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// NetworkDTO is the wire projection of core.Network.
type NetworkDTO struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Nick     string       `json:"nick"`
	State    string       `json:"state"`
	Caps     []string     `json:"caps,omitempty"` // negotiated IRCv3 caps
	Channels []ChannelDTO `json:"channels"`
	// Friends is the network's MONITOR list with live presence, for the sidebar
	// friends section. Omitted when the network has no friends.
	Friends []FriendDTO `json:"friends,omitempty"`
}

// FriendDTO is one monitored nick and whether it is currently online.
type FriendDTO struct {
	Nick   string `json:"nick"`
	Online bool   `json:"online"`
}

// ChannelDTO is the wire projection of core.Channel. State is an opaque
// per-buffer key/value bag published by plugins (see core.API.SetBufferState);
// the client treats it as plugin-defined metadata it can react to (e.g. a
// "encrypted" key from the FiSH plugin → render a lock icon).
type ChannelDTO struct {
	Name      string            `json:"name"`
	Kind      string            `json:"kind"`
	Topic     string            `json:"topic"`
	Members   []MemberDTO       `json:"members,omitempty"`
	Unread    int               `json:"unread"`
	Highlight int               `json:"highlight"`
	State     map[string]string `json:"state,omitempty"`
}

// MemberDTO is the wire projection of core.Member.
type MemberDTO struct {
	Nick  string `json:"nick"`
	Modes string `json:"modes"`
	Away  bool   `json:"away"`
}

// MessageDTO is the wire projection of core.Message. Time is RFC3339.
type MessageDTO struct {
	ID string `json:"id"`
	// Seq is the store's monotonic rowid for a persisted message, used by the
	// client as the keyset cursor when paging history backward (BacklogFetch.
	// BeforeSeq). Omitted (0) for live messages not read back from the store.
	Seq       int64             `json:"seq,omitempty"`
	Network   string            `json:"network"`
	Buffer    string            `json:"buffer"`
	Time      string            `json:"time"`
	From      string            `json:"from"`
	Kind      string            `json:"kind"`
	Text      string            `json:"text"`
	Self      bool              `json:"self"`
	Highlight bool              `json:"highlight,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`
}

// MsgSend is a client→server request to send text to a buffer. Text may be
// a slash-command, parsed server-side (see core.runBuiltinCommand and the
// plugin command hooks).
type MsgSend struct {
	Network string `json:"network"`
	Buffer  string `json:"buffer"`
	Text    string `json:"text"`
}

// BacklogFetch is a client→server request for a page of history.
//
// BeforeSeq is a keyset cursor: the Seq (store rowid) of the oldest message
// the client holds (0 or absent = most recent page). The server returns
// messages with a smaller Seq. Paging on Seq rather than time is exact even
// when many messages share a millisecond timestamp.
//
// Around, when set, asks for a window of context centered on that time —
// roughly Limit/2 messages with ts ≤ Around plus Limit/2 strictly newer,
// returned oldest-first. Used for "jump to this message" navigation from
// mentions and search results. When Around is non-empty it takes
// precedence over BeforeSeq.
//
// Carry an Envelope.ID to correlate the reply.
type BacklogFetch struct {
	Network   string `json:"network"`
	Buffer    string `json:"buffer"`
	BeforeSeq int64  `json:"before_seq,omitempty"`
	Around    string `json:"around,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// BacklogResp answers a BacklogFetch with a page of history, oldest-first.
// More reports whether older history remains before this page. Around is
// echoed when this page was produced by an Around-style request, so the
// client can distinguish a centered window from a paged-backward reply.
type BacklogResp struct {
	Network  string       `json:"network"`
	Buffer   string       `json:"buffer"`
	Messages []MessageDTO `json:"messages"`
	More     bool         `json:"more"`
	Around   string       `json:"around,omitempty"`
}

// ContextFetch asks for a window of messages surrounding a single anchor
// message, so a mention/search result can be expanded inline without leaving
// the list. ID is the anchor message's id (echoed back in ContextResp so the
// client attaches the window to the right row); Around is the anchor's time,
// which the server centers the window on (see Store.BacklogAround). Carry an
// Envelope.ID to correlate the reply.
type ContextFetch struct {
	Network string `json:"network"`
	Buffer  string `json:"buffer"`
	ID      string `json:"id"`
	Around  string `json:"around"`
	Limit   int    `json:"limit,omitempty"`
}

// ContextResp answers a ContextFetch with the surrounding window,
// oldest-first. ID echoes the anchor message id from the request so the
// client can match the window to the row that requested it.
type ContextResp struct {
	Network  string       `json:"network"`
	Buffer   string       `json:"buffer"`
	ID       string       `json:"id"`
	Messages []MessageDTO `json:"messages"`
}

// SearchReq is a client→server full-text search. Network/Buffer scope it
// when set. Carry an Envelope.ID to correlate the reply.
type SearchReq struct {
	Query   string `json:"query"`
	Network string `json:"network,omitempty"`
	Buffer  string `json:"buffer,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

// SearchResp answers a SearchReq, newest matches first.
type SearchResp struct {
	Query   string       `json:"query"`
	Results []MessageDTO `json:"results"`
}

// MissedResp answers a missed:fetch with the highlight lines that arrived
// while the user was away — i.e. messages flagged as a highlight whose time is
// newer than their buffer's read marker, across every buffer, oldest first.
// It is the body of the "what you missed" digest the client shows on connect;
// the unread/mention *counts* shown alongside come from the init snapshot's
// per-channel tallies, so only the actual lines need a fetch. There is no
// request struct: the marker set is server-side state, so the request carries
// no parameters.
type MissedResp struct {
	Messages []MessageDTO `json:"messages"`
}

// NetAdd is a client→server request to add and connect a network at runtime.
type NetAdd struct {
	Name         string   `json:"name"`
	Addr         string   `json:"addr"`
	Fallbacks    []string `json:"fallbacks,omitempty"`
	TLS          bool     `json:"tls"`
	Insecure     bool     `json:"insecure,omitempty"`
	Nick         string   `json:"nick"`
	User         string   `json:"user,omitempty"`
	Realname     string   `json:"realname,omitempty"`
	SASLUser     string   `json:"sasl_user,omitempty"`
	SASLPass     string   `json:"sasl_pass,omitempty"`
	ServerPass   string   `json:"server_pass,omitempty"`
	Perform      []string `json:"perform,omitempty"`
	SASLExternal bool     `json:"sasl_external,omitempty"`
	CertPEM      string   `json:"cert_pem,omitempty"`
	Channels     []string `json:"channels,omitempty"`
}

// NetRemove is a client→server request to remove a network, and the
// server→client notification that one was removed.
type NetRemove struct {
	Network string `json:"network"`
}

// NetReorder is a client→server request to set the manual network order, and
// the server→client notification of it: Networks is the full network id list
// in display order.
type NetReorder struct {
	Networks []string `json:"networks"`
}

// BufReorder is a client→server request to set the manual buffer order within
// a network: Buffers is the buffer display names in order (the status buffer
// may be omitted).
type BufReorder struct {
	Network string   `json:"network"`
	Buffers []string `json:"buffers"`
}

// ListReq is a client→server channel-browser request. Query is passed to
// the server's LIST verbatim (e.g. ">100"); empty lists everything.
type ListReq struct {
	Network string `json:"network"`
	Query   string `json:"query,omitempty"`
}

// ListResp answers a ListReq with the network's channels.
type ListResp struct {
	Network  string        `json:"network"`
	Channels []ListChannel `json:"channels"`
}

// ListChannel is one channel in a LIST result.
type ListChannel struct {
	Name  string `json:"name"`
	Users int    `json:"users"`
	Topic string `json:"topic"`
}

// Typing is a typing notification. c2s carries Network/Buffer/State (the
// user is typing); s2c additionally carries Nick (someone else is typing).
// State is "active", "paused", or "done".
type Typing struct {
	Network string `json:"network"`
	Buffer  string `json:"buffer"`
	Nick    string `json:"nick,omitempty"`
	State   string `json:"state"`
}

// React is an emoji reaction on a message. c2s carries
// Network/Buffer/Target/Reaction (react to the message Target, a msgid);
// s2c additionally carries Nick (who reacted). The client treats a repeated
// (nick, reaction) pair as a toggle.
type React struct {
	Network  string `json:"network"`
	Buffer   string `json:"buffer"`
	Target   string `json:"target"` // msgid being reacted to
	Nick     string `json:"nick,omitempty"`
	Reaction string `json:"reaction"`
}

// Redact removes a message. c2s carries Network/Buffer/Target[/Reason]
// (redact the message Target, a msgid); s2c additionally carries By (who
// redacted it).
type Redact struct {
	Network string `json:"network"`
	Buffer  string `json:"buffer"`
	Target  string `json:"target"` // msgid being redacted
	By      string `json:"by,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// ReadMark is a client→server notice that the user has read a buffer up to
// "now" — it advances the server-side read marker so unread counts survive a
// reload. Sent when a buffer is focused and (debounced) as messages arrive in
// the focused buffer. The server stamps the marker with its own clock; there
// is no time field to keep client/server clocks from disagreeing. After
// recording it, the server echoes the same frame (s2c) to the user's other
// connected clients so they clear the buffer's badge and stay in sync.
type ReadMark struct {
	Network string `json:"network"`
	Buffer  string `json:"buffer"`
}

// NetConnect is a client→server request to connect or disconnect a network
// without removing it.
type NetConnect struct {
	Network string `json:"network"`
	Connect bool   `json:"connect"`
}

// NetInfoReq is a client→server request for a network's current config (to
// populate the settings form). The reply is a net:info frame carrying a
// NetConfig.
type NetInfoReq struct {
	Network string `json:"network"`
}

// NetConfig is a network's full editable configuration, used for the
// net:info reply and the net:edit request. Network identifies the existing
// network being edited.
type NetConfig struct {
	Network      string   `json:"network"`
	Name         string   `json:"name"`
	Addr         string   `json:"addr"`
	Fallbacks    []string `json:"fallbacks,omitempty"`
	TLS          bool     `json:"tls"`
	Insecure     bool     `json:"insecure"`
	Nick         string   `json:"nick"`
	User         string   `json:"user"`
	Realname     string   `json:"realname"`
	SASLUser     string   `json:"sasl_user"`
	SASLPass     string   `json:"sasl_pass"`
	ServerPass   string   `json:"server_pass"`
	Perform      []string `json:"perform"`
	SASLExternal bool     `json:"sasl_external"`
	CertPEM      string   `json:"cert_pem"`
	Channels     []string `json:"channels"`
}

// PluginAction is a client→server request to load, unload, or reload a
// plugin script at runtime. Action is "load", "unload", or "reload"; Name
// is a bare script name (the filename without ".lua"). The reply is a
// plugin:list frame with the refreshed list.
type PluginAction struct {
	Name   string `json:"name"`
	Action string `json:"action"`
}

// PluginInfo is the wire projection of core.PluginInfo: one row in the
// plugin manager. Loaded is false for *.lua files present in the scripts
// dir but not currently running.
type PluginInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Loaded      bool            `json:"loaded"`
	Disabled    bool            `json:"disabled,omitempty"`
	Errors      int             `json:"errors,omitempty"`
	Commands    []string        `json:"commands,omitempty"`
	Hooks       int             `json:"hooks"`
	Settings    []PluginSetting `json:"settings,omitempty"`
}

// PluginSetting is the wire projection of core.PluginSetting: one field in a
// plugin's settings form. Value is the current value (blank for Secret
// settings). Options is populated only when Type == "select".
type PluginSetting struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Label   string   `json:"label,omitempty"`
	Help    string   `json:"help,omitempty"`
	Value   string   `json:"value"`
	Default string   `json:"default,omitempty"`
	Secret  bool     `json:"secret,omitempty"`
	Options []string `json:"options,omitempty"`
}

// PluginSettingReq is a client→server request to set one declared setting of a
// loaded plugin. Name is the script; Key is the setting name; Value is the new
// value (validated server-side against the setting's type). The reply is a
// plugin:list frame with the refreshed list.
type PluginSettingReq struct {
	Name  string `json:"name"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

// PluginListResp answers a plugin:list request (and every plugin:action),
// carrying the full set of known plugins.
type PluginListResp struct {
	Plugins []PluginInfo `json:"plugins"`
}

// CompleteReq asks the plugin host for tab-completion candidates for the
// partial Word the user is typing in (Network, Buffer). Sent on Tab and as
// the user keeps typing. Seq lets the client discard a stale reply that
// arrives after the token has already changed.
type CompleteReq struct {
	Network string `json:"network"`
	Buffer  string `json:"buffer"`
	Word    string `json:"word"`
	Seq     int    `json:"seq"`
}

// CompleteRes answers a CompleteReq with the plugin-contributed candidates,
// echoing the request's Seq. Items are full replacement tokens; the client
// merges them into its local nick/channel/emoji/command menu.
type CompleteRes struct {
	Seq   int      `json:"seq"`
	Items []string `json:"items"`
}

// HighlightRules is the user's highlight ruleset: case-insensitive regex
// patterns that flag an incoming message (in addition to a nick mention) and
// exceptions that suppress a would-be highlight. Delivered in InitState, echoed
// in a highlight frame after a highlight:set, and carried by highlight:set
// itself. A bad regex is rejected with an error frame and leaves rules
// unchanged.
type HighlightRules struct {
	Patterns   []string `json:"patterns"`
	Exceptions []string `json:"exceptions"`
}

// AliasTable is the user's command-alias map: a slash-command name (lowercase,
// no leading slash) to an expansion template using $1..$9, $* and $N-. Like
// HighlightRules it is delivered in InitState, carried by aliases:set, and
// echoed back in an aliases frame after a set (with names normalized and
// blank/invalid entries dropped). Persisted per user, so it survives reloads
// and is shared across the user's devices.
type AliasTable struct {
	Aliases map[string]string `json:"aliases"`
}

// MuteRef identifies one muted buffer. A muted buffer shows no unread badge and
// fires no notification (in-app or push). The set is server-persisted per user
// so it survives reloads and is shared across the user's devices; push
// suppression needs it server-side because push fires while no client is
// connected. Buffer is matched case-insensitively.
type MuteRef struct {
	Network string `json:"network"`
	Buffer  string `json:"buffer"`
}

// MuteSet is a client→server request to mute (Muted=true) or unmute a buffer.
type MuteSet struct {
	Network string `json:"network"`
	Buffer  string `json:"buffer"`
	Muted   bool   `json:"muted"`
}

// MonitorRef identifies a nick to add to or remove from a network's friends
// list (monitor:add / monitor:remove). The updated list rides the following
// net:update snapshot, so there is no dedicated reply.
type MonitorRef struct {
	Network string `json:"network"`
	Nick    string `json:"nick"`
}

// BufClose is a client→server request to close a query/DM buffer, removing it
// from the network's state. Channels are left via /part instead; the status
// buffer cannot be closed. The engine answers by re-broadcasting the network
// (net:update) without the buffer, so every tab drops it.
type BufClose struct {
	Network string `json:"network"`
	Buffer  string `json:"buffer"`
}

// Ping (c2s) and Pong (s2c) are payloadless liveness frames: the browser's
// WebSocket API can't surface protocol-level ping/pong to JS, so the client
// probes a possibly half-open socket by sending a ping and watching for any
// reply. The server answers every ping with a pong; there is no struct to
// decode. (This is independent of the server's own protocol-level Ping, which
// detects a dead browser from the server side.)

// WireError is a server→client error, correlated to a request id when set
// on the Envelope.
type WireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
