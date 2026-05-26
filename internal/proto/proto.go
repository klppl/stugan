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
	THello     = "hello"      // s2c
	TInit      = "init"       // s2c
	TMsg       = "msg"        // s2c
	TNetUpdate = "net:update" // s2c
	TBacklog   = "backlog"    // s2c (answers backlog:fetch)
	TError     = "error"      // s2c

	TMsgSend      = "msg:send"      // c2s
	TBacklogFetch = "backlog:fetch" // c2s
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
	Channels []ChannelDTO `json:"channels"`
}

// ChannelDTO is the wire projection of core.Channel.
type ChannelDTO struct {
	Name      string      `json:"name"`
	Kind      string      `json:"kind"`
	Topic     string      `json:"topic"`
	Members   []MemberDTO `json:"members,omitempty"`
	Unread    int         `json:"unread"`
	Highlight int         `json:"highlight"`
}

// MemberDTO is the wire projection of core.Member.
type MemberDTO struct {
	Nick  string `json:"nick"`
	Modes string `json:"modes"`
	Away  bool   `json:"away"`
}

// MessageDTO is the wire projection of core.Message. Time is RFC3339.
type MessageDTO struct {
	ID      string            `json:"id"`
	Network string            `json:"network"`
	Buffer  string            `json:"buffer"`
	Time    string            `json:"time"`
	From    string            `json:"from"`
	Kind    string            `json:"kind"`
	Text    string            `json:"text"`
	Self    bool              `json:"self"`
	Tags    map[string]string `json:"tags,omitempty"`
}

// MsgSend is a client→server request to send text to a buffer. Text may be
// a slash-command (parsed server-side; command handling lands in Phase 5).
type MsgSend struct {
	Network string `json:"network"`
	Buffer  string `json:"buffer"`
	Text    string `json:"text"`
}

// BacklogFetch is a client→server request for a page of history. Before is
// an RFC3339 timestamp cursor (empty = most recent page); the server
// returns messages older than it. Carry an Envelope.ID to correlate the
// reply.
type BacklogFetch struct {
	Network string `json:"network"`
	Buffer  string `json:"buffer"`
	Before  string `json:"before,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

// BacklogResp answers a BacklogFetch with a page of history, oldest-first.
// More reports whether older history remains before this page.
type BacklogResp struct {
	Network  string       `json:"network"`
	Buffer   string       `json:"buffer"`
	Messages []MessageDTO `json:"messages"`
	More     bool         `json:"more"`
}

// WireError is a server→client error, correlated to a request id when set
// on the Envelope.
type WireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
