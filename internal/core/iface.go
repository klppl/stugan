package core

import (
	"context"
	"errors"
)

// ErrAuthFailed marks a Connect error as an authentication failure (bad
// SASL credentials, services-enforced nick, …). Unlike a network error,
// retrying won't fix it — the engine stops the reconnect loop instead of
// hammering the server with bad credentials until the user edits them.
var ErrAuthFailed = errors.New("authentication failed")

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
	// Autojoin joins the connection's configured channels. The engine calls it
	// after Perform has completed so service auth/user modes can settle first.
	Autojoin()
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
	Name        string          // script identity (filename without .lua)
	Description string          // text the script declared via stugan.describe()
	Loaded      bool            // a *.lua file present and currently running
	Disabled    bool            // auto-disabled after repeated runtime errors
	Errors      int             // runtime errors raised since it was (re)loaded
	Commands    []string        // /command names it registered
	Hooks       int             // message/input/signal/timer hooks it registered
	Settings    []PluginSetting // values declared via stugan.setting()
}

// PluginSetting is one configurable value a script declared with
// stugan.setting(), projected for the management UI's per-plugin form. Value
// is the current effective value (the kv override, else Default); it is blank
// for Secret settings, which are never sent to the client.
type PluginSetting struct {
	Name    string   // setting key (also the kv key)
	Type    string   // "text" | "number" | "select"
	Label   string   // human label for the form field
	Help    string   // optional one-line hint
	Value   string   // current effective value (blank if Secret)
	Default string   // built-in default
	Secret  bool     // value is sensitive; never sent to the client
	Options []string // allowed values when Type == "select"
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
	// DownloadPlugin downloads the named script from the official plugin
	// repository into the scripts directory and loads it.
	DownloadPlugin(ctx context.Context, name string) error
	// SetPluginSetting writes value to the named setting (declared via
	// stugan.setting) of a loaded script: it validates against the setting's
	// type, persists it to the script's kv, and runs the setting's apply
	// callback — all on the plugin goroutine so the kv cache stays coherent.
	SetPluginSetting(script, key, value string) error
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
func (nopHost) DownloadPlugin(context.Context, string) error {
	return errors.New("plugins are disabled")
}
func (nopHost) SetPluginSetting(string, string, string) error {
	return errors.New("plugins are disabled")
}
func (nopHost) Close() error { return nil }
