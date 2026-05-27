package irc

import (
	"maps"
	"strings"
	"time"

	"github.com/klippelism/stugan/internal/core"
	"github.com/lrstanley/girc"
)

// membershipPrefixes are the IRC channel-membership prefix characters
// (owner/admin/op/halfop/voice). With multi-prefix a nick may carry several.
const membershipPrefixes = "~&@%+"

// splitPrefixes separates leading membership prefixes from the rest of a
// NAMES token, e.g. "@+nick" → ("@+", "nick").
func splitPrefixes(tok string) (modes, rest string) {
	i := 0
	for i < len(tok) && strings.IndexByte(membershipPrefixes, tok[i]) >= 0 {
		i++
	}
	return tok[:i], tok[i:]
}

// toEvent maps a parsed girc wire event into a normalized core.Event for
// the given network. self is our current nick, used to route direct
// messages into a query buffer. ok is false for commands stugan does not
// model as events. This is a pure function so it can be table-tested
// against raw IRC lines without a live connection.
func toEvent(network string, e *girc.Event, self string) (core.Event, bool) {
	when := e.Timestamp
	if when.IsZero() {
		when = time.Now()
	}
	from := ""
	if e.Source != nil {
		from = e.Source.Name
	}

	switch e.Command {
	case girc.PRIVMSG, girc.NOTICE:
		if len(e.Params) == 0 {
			return core.Event{}, false
		}
		target := e.Params[0]
		text := e.Last()
		kind := core.MsgPrivmsg
		if e.Command == girc.NOTICE {
			kind = core.MsgNotice
		}
		if e.IsAction() {
			kind = core.MsgAction
			text = e.StripAction()
		}

		// Channel target → channel buffer. A message from the server itself
		// (e.g. pre-registration notices) → the status buffer. Otherwise it
		// is a query: keyed by the other party — the sender for an inbound
		// DM, but the target for our own echoed outbound DM.
		buffer := target
		switch {
		case isChannel(target):
			buffer = target
		case e.Source != nil && e.Source.IsServer():
			buffer = core.StatusBuffer
		default:
			outbound := e.Echo || (self != "" && from == self)
			if !outbound && from != "" {
				buffer = from
			}
		}

		msg := &core.Message{
			ID:      tag(e, "msgid"),
			Network: network,
			Buffer:  buffer,
			Time:    when,
			From:    from,
			Account: tag(e, "account"),
			Kind:    kind,
			Text:    text,
			Tags:    copyTags(e.Tags),
			Self:    e.Echo,
		}
		typ := core.EvMessageIn
		if e.Echo {
			typ = core.EvMessageOut
		}
		return core.Event{Type: typ, Network: network, Time: when, Message: msg}, true

	case girc.JOIN:
		// With extended-join the params are [channel, account, realname],
		// so the channel is the first param, not the trailing one.
		ch := ""
		if len(e.Params) > 0 {
			ch = e.Params[0]
		}
		account := tag(e, "account")
		if account == "" && len(e.Params) >= 3 && e.Params[1] != "*" {
			account = e.Params[1] // extended-join account field
		}
		return core.Event{
			Type: core.EvJoin, Network: network, Time: when,
			Nick: from, Channel: ch, Account: account,
		}, true

	case girc.PART:
		ch := ""
		if len(e.Params) > 0 {
			ch = e.Params[0]
		}
		reason := ""
		if len(e.Params) > 1 {
			reason = e.Last()
		}
		return core.Event{
			Type: core.EvPart, Network: network, Time: when,
			Nick: from, Channel: ch, Text: reason,
		}, true

	case girc.QUIT:
		return core.Event{
			Type: core.EvQuit, Network: network, Time: when,
			Nick: from, Text: e.Last(),
		}, true

	case girc.NICK:
		return core.Event{
			Type: core.EvNick, Network: network, Time: when,
			Nick: from, NewNick: e.Last(),
		}, true

	case girc.AWAY:
		// away-notify: a trailing message means now-away; empty means back.
		return core.Event{
			Type: core.EvAway, Network: network, Time: when,
			Nick: from, Away: e.Last() != "",
		}, true

	case girc.RPL_NAMREPLY:
		// 353: <me> <=|*|@> <channel> :[prefix]nick[!user@host] ...
		// (multi-prefix and userhost-in-names may both be active).
		if len(e.Params) < 4 {
			return core.Event{}, false
		}
		channel := e.Params[2]
		var members []core.Member
		for tok := range strings.FieldsSeq(e.Last()) {
			modes, rest := splitPrefixes(tok)
			nick := rest
			if i := strings.IndexByte(nick, '!'); i >= 0 {
				nick = nick[:i] // strip userhost-in-names hostmask
			}
			if nick != "" {
				members = append(members, core.Member{Nick: nick, Modes: modes})
			}
		}
		if len(members) == 0 {
			return core.Event{}, false
		}
		return core.Event{Type: core.EvNames, Network: network, Time: when, Channel: channel, Members: members}, true

	case girc.TOPIC:
		ch := ""
		if len(e.Params) > 0 {
			ch = e.Params[0]
		}
		return core.Event{
			Type: core.EvTopic, Network: network, Time: when,
			Channel: ch, Text: e.Last(), Nick: from,
		}, true

	default:
		return core.Event{}, false
	}
}

// isChannel reports whether target names a channel (vs a nick/query).
func isChannel(target string) bool {
	if target == "" {
		return false
	}
	switch target[0] {
	case '#', '&', '+', '!':
		return true
	default:
		return false
	}
}

// tag returns the value of an IRCv3 message tag, or "".
func tag(e *girc.Event, key string) string {
	if e.Tags == nil {
		return ""
	}
	v, _ := e.Tags.Get(key)
	return v
}

// copyTags snapshots message tags into a plain map (nil if none).
func copyTags(t girc.Tags) map[string]string {
	if len(t) == 0 {
		return nil
	}
	m := make(map[string]string, len(t))
	maps.Copy(m, t)
	return m
}
