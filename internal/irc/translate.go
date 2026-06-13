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

// channelModeEvent translates a channel MODE line into an EvMode carrying the
// membership-prefix changes (op/voice/half-op/...) it makes. prefix and
// chanmodes are the server's ISUPPORT PREFIX and CHANMODES, used only to
// consume mode arguments correctly so prefix-mode nicks pair to the right
// argument. ok is false for a user-mode MODE (target is our nick, not a
// channel) or a line that makes no membership change.
func channelModeEvent(network string, e *girc.Event, prefix, chanmodes string) (core.Event, bool) {
	if len(e.Params) < 2 || !isChannel(e.Params[0]) {
		return core.Event{}, false
	}
	when := e.Timestamp
	if when.IsZero() {
		when = time.Now()
	}
	from := ""
	if e.Source != nil {
		from = e.Source.Name
	}
	flags := e.Params[1]
	args := e.Params[2:]
	mods := membershipModeChanges(prefix, chanmodes, flags, args)
	if len(mods) == 0 {
		return core.Event{}, false
	}
	return core.Event{
		Type: core.EvMode, Network: network, Time: when,
		Buffer: e.Params[0], Nick: from,
		Text:        strings.TrimSpace(flags + " " + strings.Join(args, " ")),
		MemberModes: mods,
	}, true
}

// membershipModeChanges parses a channel MODE flags/args pair and returns the
// membership-prefix changes it makes, ignoring list/setting modes (+b, +m, …).
// Argument consumption follows the CHANMODES argument classes so a prefix-mode
// nick pairs to the correct argument even when the line mixes in other
// arg-taking modes. Unknown modes are assumed argument-less (the common
// client convention).
func membershipModeChanges(prefix, chanmodes, flags string, args []string) []core.MemberMode {
	letters, symbols := splitPrefixSpec(prefix)
	listArgs, alwaysArgs, setArgs, _ := splitChanmodes(chanmodes)
	var out []core.MemberMode
	add := true
	ai := 0
	for i := 0; i < len(flags); i++ {
		switch c := flags[i]; c {
		case '+':
			add = true
		case '-':
			add = false
		default:
			takesArg := strings.IndexByte(letters, c) >= 0 ||
				strings.IndexByte(listArgs, c) >= 0 ||
				strings.IndexByte(alwaysArgs, c) >= 0 ||
				(add && strings.IndexByte(setArgs, c) >= 0)
			arg := ""
			if takesArg && ai < len(args) {
				arg = args[ai]
				ai++
			}
			if pi := strings.IndexByte(letters, c); pi >= 0 && pi < len(symbols) && arg != "" {
				out = append(out, core.MemberMode{Nick: arg, Symbol: string(symbols[pi]), Add: add})
			}
		}
	}
	return out
}

// splitPrefixSpec splits an ISUPPORT PREFIX value "(modes)symbols" into its
// positionally-aligned mode letters and prefix symbols, e.g. "(qaohv)~&@%+" →
// ("qaohv", "~&@%+"). Falls back to the RFC default on a malformed value.
func splitPrefixSpec(prefix string) (letters, symbols string) {
	if strings.HasPrefix(prefix, "(") {
		if i := strings.IndexByte(prefix, ')'); i > 1 {
			letters, symbols = prefix[1:i], prefix[i+1:]
			if len(letters) == len(symbols) {
				return letters, symbols
			}
		}
	}
	return "ov", "@+"
}

// splitChanmodes splits an ISUPPORT CHANMODES value into its four
// comma-separated argument classes (A: list, B: always-arg, C: arg-on-set,
// D: never-arg). Missing trailing classes come back empty.
func splitChanmodes(chanmodes string) (list, always, set, never string) {
	parts := strings.SplitN(chanmodes, ",", 4)
	get := func(i int) string {
		if i < len(parts) {
			return parts[i]
		}
		return ""
	}
	return get(0), get(1), get(2), get(3)
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
		} else if ok, _ := e.IsCTCP(); ok {
			// A non-ACTION CTCP request or reply (VERSION, PING, TIME,
			// CLIENTINFO, …). girc's built-in CTCP handler already answers the
			// standard requests; we drop the message here so the raw \x01-framed
			// payload never surfaces in a buffer as garbled text.
			return core.Event{}, false
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
			Nick: from, Buffer: ch, Account: account,
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
			Nick: from, Buffer: ch, Text: reason,
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
			Buffer: e.Params[1], Count: users, Text: e.Last(),
		}, true

	case girc.RPL_LISTEND:
		return core.Event{Type: core.EvListEnd, Network: network, Time: when}, true

	case girc.CAP_TAGMSG:
		// TAGMSG carries client-only tags. Two ride it: +typing (typing
		// indicators) and +draft/react (emoji reactions referencing a msgid
		// via +draft/reply). Reactions DO include our own echo so the sender
		// sees their reaction; typing ignores self.
		if from == "" || len(e.Params) == 0 {
			return core.Event{}, false
		}
		target := e.Params[0]
		buffer := target
		if !isChannel(target) {
			buffer = from // a direct TAGMSG → the sender's query
		}
		if react, ok := e.Tags.Get("+draft/react"); ok && react != "" {
			msgid, _ := e.Tags.Get("+draft/reply")
			if msgid == "" {
				return core.Event{}, false // a reaction must reference a message
			}
			return core.Event{
				Type: core.EvReact, Network: network, Time: when,
				Nick: from, Buffer: buffer, Target: msgid, Text: react,
			}, true
		}
		state, ok := e.Tags.Get("+typing")
		if !ok || from == self {
			return core.Event{}, false
		}
		return core.Event{
			Type: core.EvTyping, Network: network, Time: when,
			Nick: from, Buffer: buffer, Text: state,
		}, true

	case "REDACT":
		// draft/message-redaction: REDACT <target> <msgid> [:<reason>]
		if len(e.Params) < 2 {
			return core.Event{}, false
		}
		target := e.Params[0]
		buffer := target
		if !isChannel(target) {
			// A redaction in a query is keyed by the other party (the sender,
			// or — for our own echoed redaction — the target).
			if from != "" && (e.Echo || (self != "" && from == self)) {
				buffer = target
			} else if from != "" {
				buffer = from
			}
		}
		reason := ""
		if len(e.Params) > 2 {
			reason = e.Last()
		}
		return core.Event{
			Type: core.EvRedact, Network: network, Time: when,
			Nick: from, Buffer: buffer, Target: e.Params[1], Text: reason,
		}, true

	case "FAIL", "WARN", "NOTE":
		// standard-replies: <FAIL|WARN|NOTE> <command> <code> [context]... :<description>
		return core.Event{
			Type: core.EvNumeric, Network: network, Time: when,
			Nick: "", Text: formatStandardReply(e),
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
		return core.Event{Type: core.EvNames, Network: network, Time: when, Buffer: channel, Members: members}, true

	case girc.TOPIC:
		ch := ""
		if len(e.Params) > 0 {
			ch = e.Params[0]
		}
		return core.Event{
			Type: core.EvTopic, Network: network, Time: when,
			Buffer: ch, Text: e.Last(), Nick: from,
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

// formatStandardReply renders an IRCv3 standard-reply (FAIL/WARN/NOTE) into a
// human-readable system line. Wire shape:
//
//	<FAIL|WARN|NOTE> <command> <code> [context...] :<description>
//
// e.g. "FAIL JOIN CHANNELS_FULL #x :Channel is full" →
// "[FAIL] JOIN CHANNELS_FULL: Channel is full".
func formatStandardReply(e *girc.Event) string {
	head := "[" + e.Command + "]"
	desc := e.Last()
	// Params without the trailing description are command, code, and any
	// context tokens; join them as the label.
	label := e.Params
	if len(label) > 0 && label[len(label)-1] == desc {
		label = label[:len(label)-1]
	}
	if len(label) == 0 {
		return strings.TrimSpace(head + " " + desc)
	}
	return strings.TrimSpace(head + " " + strings.Join(label, " ") + ": " + desc)
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
		// Params between <nick> and the trailing text are the middle fields;
		// guard the slice against short/malformed replies (fewer than 3 params)
		// so a bad line doesn't panic the handler.
		parts := []string{subject}
		if len(e.Params) >= 3 {
			parts = append(parts, e.Params[2:len(e.Params)-1]...)
		}
		if last := e.Last(); last != "" {
			parts = append(parts, last)
		}
		return strings.Join(parts, " "), subject, code, true
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
