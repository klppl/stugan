package core

import "time"

// API is the surface the engine exposes back to the plugin host, so Lua
// scripts can act on and read the IRC state without the plugin package
// touching engine internals or the IRC library. The engine implements it
// (see engineAPI). All methods are safe to call from the plugin goroutine.
type API interface {
	// Send writes a raw IRC line on a network.
	Send(network, raw string) error
	// Message/Notice/Action send to a target and echo the line locally so
	// the sender sees it (echo-message is not enabled).
	Message(network, target, text string) error
	Notice(network, target, text string) error
	Action(network, target, text string) error
	// Join/Part change channel membership.
	Join(network, channel string) error
	Part(network, channel string) error
	// Print injects a local line into a buffer without sending to IRC.
	Print(network, buffer, text string)
	// SetBufferState publishes an opaque key/value bag on a buffer. The
	// engine carries it through snapshots and the wire protocol so clients
	// can react to plugin metadata (e.g. an "encrypted" tag from a FiSH
	// plugin). Passing nil or an empty map clears state. No-op if the
	// (network, buffer) pair doesn't exist; the plugin is responsible for
	// re-publishing on buffers that materialise later.
	SetBufferState(network, buffer string, state map[string]string)
	// Networks/Channels/Members/Nick read a snapshot of current state.
	Networks() []NetworkInfo
	Channels(network string) []ChannelInfo
	Members(network, channel string) []MemberInfo
	Nick(network string) string
	// Backlog reads recent stored history for a buffer.
	Backlog(network, buffer string, limit int) []MessageInfo
	// HoldJoins / ReleaseJoins gate configured channel autojoin until service
	// authentication completes or a timeout expires.
	HoldJoins(network string) error
	ReleaseJoins(network string) error
}

// NetworkInfo is a flat snapshot of a network for the plugin API.
type NetworkInfo struct {
	ID    string
	Name  string
	Nick  string
	State string
}

// ChannelInfo is a flat snapshot of a buffer for the plugin API.
type ChannelInfo struct {
	Name  string
	Kind  string
	Topic string
}

// MemberInfo is a flat snapshot of a channel member for the plugin API.
type MemberInfo struct {
	Nick    string
	Account string
	Modes   string
	Away    bool
}

// MessageInfo is a flat snapshot of a stored message for the plugin API.
type MessageInfo struct {
	From string
	Text string
	Time time.Time
}
