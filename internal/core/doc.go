// Package core is the GUI- and transport-independent brain of stugan.
//
// It owns the domain types (User, Network, Channel, Message, Member), the
// connection state machine, and the event bus through which every
// meaningful event (message in/out, join, part, nick, connect, command)
// flows. Plugin hooks fire on this bus in priority order and may
// short-circuit (drop) or mutate events.
//
// core depends only on interfaces — IRCConn for connections and
// PluginHost for the plugin runtime — and must never import server/,
// the concrete IRC library, or any UI code. The state machine and bus are
// built across Phases 1, 3, and 5.
package core
