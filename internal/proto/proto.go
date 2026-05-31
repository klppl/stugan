// Package proto defines the typed JSON wire protocol shared between the
// daemon and the browser. These Go structs are the single source of truth;
// the TypeScript mirror in client/src/proto/events.ts is kept in sync by
// hand. See docs/protocol.md.
//
// Phase 2 implements the subset needed for the live loop: hello, init, msg
// (server→client) and msg:send (client→server). The remaining events from
// the schema are added in later phases.
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
	TNetInfo      = "net:info"      // s2c (answers c2s net:info)
	TBacklog      = "backlog"       // s2c (answers backlog:fetch)
	TSearchResult = "search:result" // s2c (answers search)
	TListResult   = "list:result"   // s2c (answers list)
	TTyping       = "typing"        // s2c (and c2s)
	TReact        = "react"         // s2c (and c2s) — emoji reactions
	TRedact       = "redact"        // s2c (and c2s) — message redaction
	TPluginList   = "plugin:list"   // s2c (answers c2s plugin:list/plugin:action)
	TCompleteRes  = "complete:res"  // s2c (answers c2s complete:req)
	TError        = "error"         // s2c

	TMsgSend      = "msg:send"      // c2s
	TCompleteReq  = "complete:req"  // c2s — ask plugins for tab-completion candidates
	TBacklogFetch = "backlog:fetch" // c2s
	TSearch       = "search"        // c2s
	TNetAdd       = "net:add"       // c2s
	TNetEdit      = "net:edit"      // c2s
	TNetConnect   = "net:connect"   // c2s
	TList         = "list"          // c2s
	TPluginAction = "plugin:action" // c2s — load/unload/reload a plugin
	TRead         = "read"          // c2s — mark a buffer read (advance read marker)
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
	User     UserDTO      `json:"user"`
	Networks []NetworkDTO `json:"networks"`
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
	ID        string            `json:"id"`
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
// a slash-command (parsed server-side; command handling lands in Phase 5).
type MsgSend struct {
	Network string `json:"network"`
	Buffer  string `json:"buffer"`
	Text    string `json:"text"`
}

// BacklogFetch is a client→server request for a page of history.
//
// Before is an RFC3339 timestamp cursor (empty = most recent page); the
// server returns messages older than it.
//
// Around, when set, asks for a window of context centered on that time —
// roughly Limit/2 messages with ts ≤ Around plus Limit/2 strictly newer,
// returned oldest-first. Used for "jump to this message" navigation from
// mentions and search results. When Around is non-empty it takes
// precedence over Before.
//
// Carry an Envelope.ID to correlate the reply.
type BacklogFetch struct {
	Network string `json:"network"`
	Buffer  string `json:"buffer"`
	Before  string `json:"before,omitempty"`
	Around  string `json:"around,omitempty"`
	Limit   int    `json:"limit,omitempty"`
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

// NetAdd is a client→server request to add and connect a network at runtime.
type NetAdd struct {
	Name         string   `json:"name"`
	Addr         string   `json:"addr"`
	TLS          bool     `json:"tls"`
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
// is no time field to keep client/server clocks from disagreeing.
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
	TLS          bool     `json:"tls"`
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
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Loaded      bool     `json:"loaded"`
	Disabled    bool     `json:"disabled,omitempty"`
	Errors      int      `json:"errors,omitempty"`
	Commands    []string `json:"commands,omitempty"`
	Hooks       int      `json:"hooks"`
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

// WireError is a server→client error, correlated to a request id when set
// on the Envelope.
type WireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
