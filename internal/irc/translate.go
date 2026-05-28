package irc

import (
	"fmt"
	"maps"
	"strconv"
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
		// An echo-message of our own line is an inbound display copy (Self),
		// never a new outbound — EvMessageOut is reserved for user input.
		return core.Event{Type: core.EvMessageIn, Network: network, Time: when, Message: msg}, true

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

	case girc.RPL_LIST:
		// 322: <me> <channel> <#users> :<topic>
		if len(e.Params) < 3 {
			return core.Event{}, false
		}
		users, _ := strconv.Atoi(e.Params[2])
		return core.Event{
			Type: core.EvListItem, Network: network, Time: when,
			Channel: e.Params[1], Count: users, Text: e.Last(),
		}, true

	case girc.RPL_LISTEND:
		return core.Event{Type: core.EvListEnd, Network: network, Time: when}, true

	case girc.CAP_TAGMSG:
		// Typing indicators ride on TAGMSG via the +typing client tag. Ignore
		// our own echo and tagless TAGMSGs.
		state, ok := e.Tags.Get("+typing")
		if !ok || from == "" || from == self || len(e.Params) == 0 {
			return core.Event{}, false
		}
		target := e.Params[0]
		buffer := target
		if !isChannel(target) {
			buffer = from // a direct typing notice → the sender's query
		}
		return core.Event{
			Type: core.EvTyping, Network: network, Time: when,
			Nick: from, Channel: buffer, Text: state,
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
	}

	// Server numeric replies (WHOIS, WHOWAS, WHO, errors, …). Each formats
	// into a system-style line and pairs to a routing nick/target so the
	// engine can deliver it back to the buffer that issued the request.
	if text, subject, code, ok := formatNumeric(e); ok {
		return core.Event{
			Type: core.EvNumeric, Network: network, Time: when,
			Nick: subject, Text: text, Count: code,
		}, true
	}

	return core.Event{}, false
}

// formatNumeric renders a server numeric reply into a human-readable system
// line. Returns (text, subject, code, ok); subject is the nick or channel
// the reply concerns (used by the engine to route the message back to the
// buffer that issued the request — see applyNumeric).
func formatNumeric(e *girc.Event) (text, subject string, code int, ok bool) {
	// All standard numerics share the shape "<me> <subject> …", so the
	// subject is e.Params[1] when present.
	param := func(i int) string {
		if i < len(e.Params) {
			return e.Params[i]
		}
		return ""
	}
	subject = param(1)
	code, _ = strconv.Atoi(e.Command)

	switch e.Command {
	// --- WHOIS replies ----------------------------------------------------
	case girc.RPL_AWAY: // 301: <me> <nick> :<away message>
		return fmt.Sprintf("%s is away: %s", subject, e.Last()), subject, code, true
	case girc.RPL_WHOISREGNICK: // 307
		return fmt.Sprintf("%s is a registered nick", subject), subject, code, true
	case girc.RPL_WHOISUSER: // 311: <me> <nick> <user> <host> * :<realname>
		return fmt.Sprintf("%s (%s@%s): %s", subject, param(2), param(3), e.Last()), subject, code, true
	case girc.RPL_WHOISSERVER: // 312: <me> <nick> <server> :<info>
		return fmt.Sprintf("%s on %s (%s)", subject, param(2), e.Last()), subject, code, true
	case girc.RPL_WHOISOPERATOR: // 313
		return fmt.Sprintf("%s %s", subject, e.Last()), subject, code, true
	case girc.RPL_WHOISIDLE: // 317: <me> <nick> <idle> <signon> :seconds idle, signon time
		return fmt.Sprintf("%s: idle %ss, signon %s", subject, param(2), param(3)), subject, code, true
	case girc.RPL_WHOISCHANNELS: // 319: <me> <nick> :<channels>
		return fmt.Sprintf("%s on: %s", subject, e.Last()), subject, code, true
	case girc.RPL_WHOISSPECIAL: // 320
		return fmt.Sprintf("%s %s", subject, e.Last()), subject, code, true
	case girc.RPL_WHOISACCOUNT: // 330: <me> <nick> <account> :is logged in as
		return fmt.Sprintf("%s is logged in as %s", subject, param(2)), subject, code, true
	case girc.RPL_WHOISACTUALLY: // 338
		// Variants exist (real host / IP); print whatever the server sent.
		return fmt.Sprintf("%s %s %s", subject, strings.Join(e.Params[2:len(e.Params)-1], " "), e.Last()), subject, code, true
	case girc.RPL_WHOISHOST: // 378
		return fmt.Sprintf("%s %s", subject, e.Last()), subject, code, true
	case girc.RPL_WHOISMODES: // 379
		return fmt.Sprintf("%s %s", subject, e.Last()), subject, code, true
	case "671": // RPL_WHOISSECURE (no girc constant)
		return fmt.Sprintf("%s %s", subject, e.Last()), subject, code, true
	case girc.RPL_WHOISCERTFP: // 276
		return fmt.Sprintf("%s %s", subject, e.Last()), subject, code, true
	case girc.RPL_ENDOFWHOIS: // 318
		return "End of WHOIS for " + subject, subject, code, true

	// --- WHOWAS -----------------------------------------------------------
	case girc.RPL_WHOWASUSER: // 314: same shape as 311
		return fmt.Sprintf("(was) %s (%s@%s): %s", subject, param(2), param(3), e.Last()), subject, code, true
	case girc.RPL_ENDOFWHOWAS: // 369
		return "End of WHOWAS for " + subject, subject, code, true

	// --- WHO --------------------------------------------------------------
	case girc.RPL_WHOREPLY: // 352
		// <me> <chan> <user> <host> <server> <nick> <flags> :<hopcount> <realname>
		// We key routing by the channel argument so /who #foo lands in #foo's
		// pending entry. The nick is reported in the line.
		ch, user, host, srv, nick, flags := param(1), param(2), param(3), param(4), param(5), param(6)
		return fmt.Sprintf("%s [%s] (%s@%s) on %s via %s: %s", nick, flags, user, host, ch, srv, e.Last()), ch, code, true
	case girc.RPL_WHOSPCRPL: // 354 — variable WHOX shape; just print everything
		return strings.Join(e.Params[1:], " ") + " " + e.Last(), subject, code, true
	case girc.RPL_ENDOFWHO: // 315
		return "End of WHO for " + subject, subject, code, true
	case girc.RPL_ENDOFNAMES: // 366 — paired with /names; end of pendingWhois
		return "End of NAMES for " + subject, subject, code, true

	// --- Invite -----------------------------------------------------------
	case girc.RPL_INVITING: // 341: <me> <nick> <chan>
		return fmt.Sprintf("inviting %s to %s", subject, param(2)), subject, code, true

	// --- Common errors ----------------------------------------------------
	case girc.ERR_NOSUCHNICK: // 401
		return "no such nick: " + subject, subject, code, true
	case girc.ERR_NOSUCHSERVER: // 402
		return "no such server: " + subject, subject, code, true
	case girc.ERR_NOSUCHCHANNEL: // 403
		return "no such channel: " + subject, subject, code, true
	case girc.ERR_CANNOTSENDTOCHAN: // 404
		return "cannot send to " + subject + ": " + e.Last(), subject, code, true
	case girc.ERR_NICKNAMEINUSE: // 433
		return "nickname in use: " + subject, subject, code, true
	case girc.ERR_USERNOTINCHANNEL: // 441
		return fmt.Sprintf("%s is not in %s", subject, param(2)), subject, code, true
	case girc.ERR_NOTONCHANNEL: // 442
		return "not on channel: " + subject, subject, code, true
	case girc.ERR_NEEDMOREPARAMS: // 461
		return subject + ": " + e.Last(), subject, code, true
	case girc.ERR_INVITEONLYCHAN: // 473
		return subject + ": invite-only channel", subject, code, true
	case girc.ERR_BANNEDFROMCHAN: // 474
		return subject + ": banned from channel", subject, code, true
	case girc.ERR_BADCHANNELKEY: // 475
		return subject + ": bad channel key", subject, code, true
	case girc.ERR_CHANOPRIVSNEEDED: // 482
		return subject + ": you're not a channel operator", subject, code, true
	}
	return "", "", 0, false
}

// numericReplies are the server-reply numerics the engine surfaces as
// system messages (WHOIS / WHOWAS / WHO families, plus common errors).
// They have to be registered with girc by name so its handler dispatcher
// invokes our callback for them — girc only routes the commands we ask for.
var numericReplies = []string{
	girc.RPL_AWAY, girc.RPL_WHOISREGNICK,
	girc.RPL_WHOISUSER, girc.RPL_WHOISSERVER, girc.RPL_WHOISOPERATOR,
	girc.RPL_WHOISIDLE, girc.RPL_ENDOFWHOIS, girc.RPL_WHOISCHANNELS,
	girc.RPL_WHOISSPECIAL, girc.RPL_WHOISACCOUNT, girc.RPL_WHOISACTUALLY,
	girc.RPL_WHOISHOST, girc.RPL_WHOISMODES,
	"671", // RPL_WHOISSECURE — no girc constant
	girc.RPL_WHOISCERTFP,
	girc.RPL_WHOWASUSER, girc.RPL_ENDOFWHOWAS,
	girc.RPL_WHOREPLY, girc.RPL_WHOSPCRPL, girc.RPL_ENDOFWHO,
	girc.RPL_ENDOFNAMES,
	girc.RPL_INVITING,
	girc.ERR_NOSUCHNICK, girc.ERR_NOSUCHSERVER, girc.ERR_NOSUCHCHANNEL,
	girc.ERR_CANNOTSENDTOCHAN,
	girc.ERR_NICKNAMEINUSE,
	girc.ERR_USERNOTINCHANNEL, girc.ERR_NOTONCHANNEL,
	girc.ERR_NEEDMOREPARAMS,
	girc.ERR_INVITEONLYCHAN, girc.ERR_BANNEDFROMCHAN, girc.ERR_BADCHANNELKEY,
	girc.ERR_CHANOPRIVSNEEDED,
}

// NumericReplies returns the numeric commands the IRC connection layer must
// register handlers for so server replies surface as core events.
func NumericReplies() []string { return numericReplies }

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
