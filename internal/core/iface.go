package core

import "context"

// IRCConn is core's view of a single network connection. It is implemented
// in internal/irc; the underlying IRC library never leaks past that
// package, so the implementation can be swapped without touching core.
type IRCConn interface {
	// Connect runs the connection, blocking until it is disconnected or ctx
	// is cancelled. Inbound activity is delivered via the ConnHandler the
	// connection was constructed with.
	Connect(ctx context.Context) error
	// SendRaw writes a raw IRC line.
	SendRaw(line string) error
	// Message sends a PRIVMSG to target.
	Message(target, text string) error
	// Caps returns the negotiated IRCv3 capabilities.
	Caps() []string
	// CurrentNick returns our current nick on this network.
	CurrentNick() string
	// Close terminates the connection.
	Close() error
}

// ConnHandler receives normalized inbound events from a connection. The
// Engine implements it; a connection calls HandleEvent from its read loop.
type ConnHandler interface {
	HandleEvent(ev Event)
}

// PluginHost is core's view of the plugin runtime (implemented in
// internal/plugin in Phase 5). For mutable events the Engine calls
// Dispatch before committing; the runtime type never leaks into core.
type PluginHost interface {
	// Dispatch runs registered hooks for ev in priority order and returns
	// the possibly-mutated event, with keep=false if a hook dropped it.
	// Per-script errors are isolated and logged, never returned.
	Dispatch(ctx context.Context, ev Event) (out Event, keep bool)
	// Commands returns the /command names scripts have registered.
	Commands() []string
	// Close releases the runtime.
	Close() error
}

// nopHost is the default PluginHost: it passes every event through
// unchanged. Used until the Lua host is wired in Phase 5.
type nopHost struct{}

func (nopHost) Dispatch(_ context.Context, ev Event) (Event, bool) { return ev, true }
func (nopHost) Commands() []string                                 { return nil }
func (nopHost) Close() error                                       { return nil }
