package core

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
	// Networks/Channels/Members/Nick read a snapshot of current state.
	Networks() []NetworkInfo
	Channels(network string) []ChannelInfo
	Members(network, channel string) []MemberInfo
	Nick(network string) string
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
