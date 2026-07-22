package tui

import "github.com/klippelism/stugan/internal/core"

// These are the Bubble Tea messages the sink fans out to sessions (see
// hub.go) plus the ones the model's own commands return (backlog loads,
// unread tallies). Update dispatches on their concrete types.

// printMsg is a committed buffer line.
type printMsg struct{ m core.Message }

// netChangedMsg signals a network's state/membership changed; net is a fresh
// clone (never a pointer into engine state).
type netChangedMsg struct {
	id  string
	net *core.Network
}

// netRemovedMsg signals a runtime network removal.
type netRemovedMsg struct{ id string }

// netReorderMsg carries the full network id list in its new display order.
type netReorderMsg struct{ ids []string }

// channelListMsg delivers a LIST (channel-browser) result.
type channelListMsg struct {
	network string
	items   []core.ChannelListItem
}

// typingMsg, reactMsg, redactMsg are the ephemeral inbound notifications.
type typingMsg struct{ network, buffer, nick, state string }
type reactMsg struct{ network, buffer, target, nick, reaction string }
type redactMsg struct{ network, buffer, target, nick, reason string }

// backlogMsg is the result of a lazy history load for a buffer.
type backlogMsg struct {
	network, buffer string
	msgs            []core.Message
	more            bool // older history exists before the returned page
	err             error
}

// unreadMsg carries the persisted unread/highlight tallies fetched at start.
type unreadMsg struct{ counts []core.UnreadCount }

// errMsg surfaces a background action error as a transient status line.
type errMsg struct{ err error }

// statusMsg sets a transient status line (e.g. "connected", "joined #x").
type statusMsg struct{ text string }
