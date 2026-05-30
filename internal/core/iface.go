package core

import (
	"context"
	"errors"
)

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

// PluginInfo describes one plugin script for the management UI. It is a
// read-only projection the host builds on demand; the runtime types (LState,
// hooks) never leak into core.
type PluginInfo struct {
	Name        string   // script identity (filename without .lua)
	Description string   // text the script declared via stugan.describe()
	Loaded      bool     // a *.lua file present and currently running
	Disabled    bool     // auto-disabled after repeated runtime errors
	Errors      int      // runtime errors raised since it was (re)loaded
	Commands    []string // /command names it registered
	Hooks       int      // message/input/signal/timer hooks it registered
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
	// Complete returns plugin-contributed tab-completion candidates for the
	// partial word being typed in (network, buffer). Each candidate is a full
	// replacement token. Empty when no completion hook matches. Read-only.
	Complete(word, network, buffer string) []string
	// Plugins lists every script the host knows about — both the loaded
	// ones and the *.lua files in the scripts dir that are not loaded — for
	// the management UI.
	Plugins() []PluginInfo
	// LoadPlugin loads (or reloads) the script named name from the scripts
	// dir. UnloadPlugin tears one down; ReloadPlugin re-reads it from disk.
	// name is a bare script name (no path separators).
	LoadPlugin(name string) error
	UnloadPlugin(name string) error
	ReloadPlugin(name string) error
	// Close releases the runtime.
	Close() error
}

// nopHost is the default PluginHost: it passes every event through
// unchanged. Used when the Lua host is disabled.
type nopHost struct{}

func (nopHost) Dispatch(_ context.Context, ev Event) (Event, bool) { return ev, true }
func (nopHost) Commands() []string                                 { return nil }
func (nopHost) Complete(_, _, _ string) []string                   { return nil }
func (nopHost) Plugins() []PluginInfo                              { return nil }
func (nopHost) LoadPlugin(string) error                            { return errors.New("plugins are disabled") }
func (nopHost) UnloadPlugin(string) error                          { return errors.New("plugins are disabled") }
func (nopHost) ReloadPlugin(string) error                          { return errors.New("plugins are disabled") }
func (nopHost) Close() error                                       { return nil }
