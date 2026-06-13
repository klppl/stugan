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
	// EvMode is a channel MODE change. MemberModes carries the membership
	// prefix changes (op/voice/...) it makes; Buffer is the target, Nick the
	// setter, Text the raw mode string (flags + args) for the system line.
	EvMode EventType = "mode"
	// EvListItem / EvListEnd carry the server's LIST reply (channel browser):
	// one item per channel, then end. EvListItem uses Buffer, Count, Text.
	EvListItem EventType = "list_item"
	EvListEnd  EventType = "list_end"
	// EvTyping is an inbound +typing TAGMSG: Nick is typing in Buffer, with
	// Text the state (active/paused/done).
	EvTyping EventType = "typing"
	// EvReact is an inbound +draft/react TAGMSG: Nick reacted to the message
	// Target (a msgid) in Buffer with Text (the reaction, usually an emoji).
	// Ephemeral — fanned to sinks, not stored.
	EvReact EventType = "react"
	// EvRedact is an inbound REDACT (draft/message-redaction): Nick removed
	// the message Target (a msgid) in Buffer; Text is the reason.
	EvRedact EventType = "redact"
	// EvNumeric carries a server numeric reply (WHOIS, WHO, WHOWAS, error
	// codes, etc.). Text is the human-readable formatted line; Nick is
	// the subject of the reply (the WHOIS target, the offending channel,
	// …) used to route the system message back to the buffer that issued
	// the request — fallback is the status buffer. Count holds the
	// numeric code so the engine can clear request-tracking state on the
	// "END OF" markers.
	EvNumeric EventType = "numeric"
	// EvMonitor is an IRCv3 MONITOR status reply (730 online / 731 offline):
	// Args lists the affected nicks and Online is their new presence. One event
	// carries a whole numeric's worth of nicks so a connect-time burst is a
	// single state update, not one per friend.
	EvMonitor EventType = "monitor"

	// evSetState is internal: it carries a transient connection-state
	// change (e.g. Connecting) onto the engine loop so all state mutation
	// stays single-threaded. Not dispatched to plugins.
	evSetState EventType = "set_state"
)

// Event is the unit that flows on the engine's bus. Which fields are set
// depends on Type:
//
//	EvMessageIn/Out      → Message
//	EvJoin/EvPart        → Nick, Buffer, Account, Text(reason)
//	EvQuit               → Nick, Text(reason)
//	EvNick               → Nick(old), NewNick
//	EvTopic              → Buffer, Text(topic), Nick(setter)
//	EvConnect            → Nick(our nick)
//	EvDisconnect         → Text(reason)
//	EvCommand            → Buffer, Command, Args, Text(arg string)
//	evSetState           → State
type Event struct {
	Type    EventType
	Network string
	Time    time.Time

	Message *Message

	Nick    string
	NewNick string
	// Buffer is the channel or query the event applies to (the s2c "buffer").
	Buffer  string
	Account string
	Text    string
	State   ConnState
	// Target is a message id the event refers to (EvReact / EvRedact).
	Target string

	Command     string       // EvCommand: the command name (without leading slash)
	Args        []string     // EvCommand: whitespace-split arguments
	Members     []Member     // EvNames: the listed channel members
	MemberModes []MemberMode // EvMode: the membership prefix changes
	Away        bool         // EvAway: whether Nick is now away
	Online      bool         // EvMonitor: whether the Args nicks are now online
	Count       int          // EvListItem: user count
}

// MemberMode is one membership-prefix change carried by EvMode: Nick gains
// (Add) or loses the channel prefix Symbol, e.g. "@" for op, "+" for voice.
type MemberMode struct {
	Nick   string
	Symbol string
	Add    bool
}

// eqFold is a small ASCII case-insensitive compare used for channel/nick
// matching. (IRC casemapping is server-defined; rfc1459 mapping lands with
// ISUPPORT handling in a later phase.)
func eqFold(a, b string) bool { return strings.EqualFold(a, b) }
