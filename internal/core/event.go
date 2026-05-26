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

	// evSetState is internal: it carries a transient connection-state
	// change (e.g. Connecting) onto the engine loop so all state mutation
	// stays single-threaded. Not dispatched to plugins.
	evSetState EventType = "set_state"
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
}

// mutable reports whether plugin hooks may rewrite or drop this event.
func (e Event) mutable() bool {
	return e.Type == EvMessageIn || e.Type == EvMessageOut
}

// eqFold is a small ASCII case-insensitive compare used for channel/nick
// matching. (IRC casemapping is server-defined; rfc1459 mapping lands with
// ISUPPORT handling in a later phase.)
func eqFold(a, b string) bool { return strings.EqualFold(a, b) }
