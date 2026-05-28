package core

import (
	"strings"
	"time"
)

// EventType identifies what happened. Message-bearing events
// (EvMessageIn/EvMessageOut) are mutable and droppable by plugin hooks;
// the rest are notifications.
type EventType string

const (
	EvMessageIn  EventType = "message_in"
	EvMessageOut EventType = "message_out"
	EvJoin       EventType = "join"
	EvPart       EventType = "part"
	EvQuit       EventType = "quit"
	EvNick       EventType = "nick"
	EvTopic      EventType = "topic"
	EvConnect    EventType = "connect"
	EvDisconnect EventType = "disconnect"
	EvCommand    EventType = "command"
	// EvNames carries a channel's member list from the server's NAMES reply
	// (sent on join). Members is set; no system line is emitted.
	EvNames EventType = "names"
	// EvAway is an away-notify update: Nick changed away state to Away.
	EvAway EventType = "away"
	// EvListItem / EvListEnd carry the server's LIST reply (channel browser):
	// one item per channel, then end. EvListItem uses Channel, Count, Text.
	EvListItem EventType = "list_item"
	EvListEnd  EventType = "list_end"
	// EvTyping is an inbound +typing TAGMSG: Nick is typing in Channel
	// (buffer), with Text the state (active/paused/done).
	EvTyping EventType = "typing"
	// EvReact is an inbound +draft/react TAGMSG: Nick reacted to the message
	// Target (a msgid) in Channel (buffer) with Text (the reaction, usually
	// an emoji). Ephemeral — fanned to sinks, not stored.
	EvReact EventType = "react"
	// EvRedact is an inbound REDACT (draft/message-redaction): Nick removed
	// the message Target (a msgid) in Channel (buffer); Text is the reason.
	EvRedact EventType = "redact"
	// EvNumeric carries a server numeric reply (WHOIS, WHO, WHOWAS, error
	// codes, etc.). Text is the human-readable formatted line; Nick is
	// the subject of the reply (the WHOIS target, the offending channel,
	// …) used to route the system message back to the buffer that issued
	// the request — fallback is the status buffer. Count holds the
	// numeric code so the engine can clear request-tracking state on the
	// "END OF" markers.
	EvNumeric EventType = "numeric"

	// evSetState is internal: it carries a transient connection-state
	// change (e.g. Connecting) onto the engine loop so all state mutation
	// stays single-threaded. Not dispatched to plugins.
	evSetState EventType = "set_state"
	// evPrint is internal: a plugin (via API.Print) injects a line into a
	// buffer. Not dispatched to plugins, so it cannot recurse into hooks.
	evPrint EventType = "print"
)

// Event is the unit that flows on the engine's bus. Which fields are set
// depends on Type:
//
//	EvMessageIn/Out      → Message
//	EvJoin/EvPart        → Nick, Channel, Account, Text(reason)
//	EvQuit               → Nick, Text(reason)
//	EvNick               → Nick(old), NewNick
//	EvTopic              → Channel, Text(topic), Nick(setter)
//	EvConnect            → Nick(our nick)
//	EvDisconnect         → Text(reason)
//	EvCommand            → Channel(buffer), Command, Args, Text(arg string)
//	evSetState           → State
type Event struct {
	Type    EventType
	Network string
	Time    time.Time

	Message *Message

	Nick    string
	NewNick string
	Channel string
	Account string
	Text    string
	State   ConnState
	// Target is a message id the event refers to (EvReact / EvRedact).
	Target string

	Command string   // EvCommand: the command name (without leading slash)
	Args    []string // EvCommand: whitespace-split arguments
	Members []Member // EvNames: the listed channel members
	Away    bool     // EvAway: whether Nick is now away
	Count   int      // EvListItem: user count
}

// eqFold is a small ASCII case-insensitive compare used for channel/nick
// matching. (IRC casemapping is server-defined; rfc1459 mapping lands with
// ISUPPORT handling in a later phase.)
func eqFold(a, b string) bool { return strings.EqualFold(a, b) }
