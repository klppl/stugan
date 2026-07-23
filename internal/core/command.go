package core

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

// runBuiltinCommand handles a /command that no plugin claimed. It covers the
// common slash-commands; anything unrecognized is sent as a raw IRC line
// (uppercased), matching the weechat/irssi habit of /FOO being raw FOO.
func (e *Engine) runBuiltinCommand(ev Event) {
	conn := e.connFor(ev.Network)
	if conn == nil {
		return
	}
	name := strings.ToLower(ev.Command)
	switch name {
	case "me":
		e.API().Action(ev.Network, ev.Buffer, ev.Text)
	case "msg":
		target, text, _ := strings.Cut(ev.Text, " ")
		if target != "" && text != "" {
			e.API().Message(ev.Network, target, text)
		}
	case "notice":
		target, text, _ := strings.Cut(ev.Text, " ")
		if target != "" && text != "" {
			e.API().Notice(ev.Network, target, text)
		}
	case "join":
		// Pass the full argument string so channel keys come along:
		// /join #chan key  →  JOIN #chan key (also /join #a,#b k1,k2).
		if ev.Text != "" {
			e.recordPendingKeys(ev.Network, ev.Text)
			conn.SendRaw("JOIN " + ev.Text)
		}
	case "part":
		ch := ev.Buffer
		if len(ev.Args) > 0 {
			ch = ev.Args[0]
		}
		conn.SendRaw("PART " + ch)
	case "topic":
		// /topic <text> sets the current channel's topic; /topic alone
		// queries it.
		if ev.Text == "" {
			conn.SendRaw("TOPIC " + ev.Buffer)
		} else {
			conn.SendRaw("TOPIC " + ev.Buffer + " :" + ev.Text)
		}
	case "nick":
		if len(ev.Args) > 0 {
			conn.SendRaw("NICK " + ev.Args[0])
		}
	case "quit":
		conn.SendRaw("QUIT :" + ev.Text)
	case "chathistory":
		// IRCv3 server-side history (bouncers/ergo). On servers without it,
		// our SQLite backlog already covers history.
		if !slices.Contains(conn.Caps(), "draft/chathistory") && !slices.Contains(conn.Caps(), "chathistory") {
			e.inject(Message{
				Network: ev.Network, Buffer: ev.Buffer, Time: time.Now(),
				Kind: MsgSystem, Text: "this server does not support chathistory",
			})
			return
		}
		n := 50
		if len(ev.Args) > 0 {
			if v, err := strconv.Atoi(ev.Args[0]); err == nil && v > 0 {
				n = v
			}
		}
		conn.SendRaw(fmt.Sprintf("CHATHISTORY LATEST %s * %d", ev.Buffer, n))
	case "raw", "quote":
		conn.SendRaw(ev.Text)

	// --- Lookups (replies surface via EvNumeric routed back to ev.Buffer)
	case "whois":
		e.startNumeric(ev, "WHOIS")
	case "whowas":
		e.startNumeric(ev, "WHOWAS")
	case "who":
		e.startNumeric(ev, "WHO")
	case "names":
		// /names defaults to the current channel; with an arg, that channel.
		target := ev.Buffer
		if len(ev.Args) > 0 {
			target = ev.Args[0]
		}
		if target != "" {
			e.pendingWhois[whoisKey(ev.Network, target)] = ev.Buffer
			conn.SendRaw("NAMES " + target)
		}

	// --- Modes & moderation
	case "mode":
		// /mode <target> [modes] [args…]; on a channel buffer with no args,
		// /mode alone queries the current modes.
		target := ev.Buffer
		rest := ev.Text
		if len(ev.Args) > 0 {
			target = ev.Args[0]
			_, rest, _ = strings.Cut(ev.Text, " ")
		}
		if rest == "" {
			conn.SendRaw("MODE " + target)
		} else {
			conn.SendRaw("MODE " + target + " " + rest)
		}
	case "op", "deop", "voice", "devoice", "halfop", "dehalfop":
		// Channel-mode shorthand: /op nick [nick…] → MODE #chan +ooo nick nick nick.
		// Built from ev.Buffer so it only makes sense in a channel buffer.
		applyModeShorthand(conn, ev, name)
	case "ban":
		if ev.Buffer != "" && len(ev.Args) > 0 {
			conn.SendRaw("MODE " + ev.Buffer + " +b " + strings.Join(ev.Args, " "))
		}
	case "unban":
		if ev.Buffer != "" && len(ev.Args) > 0 {
			conn.SendRaw("MODE " + ev.Buffer + " -b " + strings.Join(ev.Args, " "))
		}
	case "kick":
		kickCmd(conn, ev)
	case "invite":
		// /invite <nick> [#channel] — default channel is the current buffer.
		if len(ev.Args) == 0 {
			return
		}
		nick := ev.Args[0]
		ch := ev.Buffer
		if len(ev.Args) > 1 {
			ch = ev.Args[1]
		}
		if ch != "" {
			conn.SendRaw("INVITE " + nick + " " + ch)
		}

	// --- Presence
	case "away":
		// /away with an arg sets away; bare /away clears.
		if ev.Text == "" {
			conn.SendRaw("AWAY")
		} else {
			conn.SendRaw("AWAY :" + ev.Text)
		}
	case "back":
		conn.SendRaw("AWAY")

	// --- Buffers
	case "query":
		// /query <nick> [text] opens a query buffer and optionally sends
		// the first line. Opening = injecting a system marker so the
		// buffer exists in the snapshot; the actual send goes via the
		// normal message path so plugins (e.g. fish.lua) can encrypt.
		if len(ev.Args) == 0 {
			return
		}
		target := ev.Args[0]
		e.inject(Message{
			Network: ev.Network, Buffer: target, Time: time.Now(),
			Kind: MsgSystem, Text: "opened query with " + target,
		})
		if len(ev.Args) > 1 {
			_, rest, _ := strings.Cut(ev.Text, " ")
			if rest != "" {
				e.sendInput(ev.Network, target, rest, 0)
			}
		}

	default:
		// Anything we don't recognize is passed through as a raw IRC line
		// (uppercased command, args joined). Matches the weechat/irssi habit
		// of /FOO behaving like raw FOO — covers server-specific commands
		// (/SAJOIN, /STATS, /OPER, /KNOCK, …) without us enumerating them.
		conn.SendRaw(strings.ToUpper(ev.Command) + " " + ev.Text)
	}
}

// startNumeric records the issuing buffer for a lookup command so the
// engine can route the server's numeric replies back to it (see
// applyNumeric). raw is the IRC verb (WHOIS / WHOWAS / WHO).
func (e *Engine) startNumeric(ev Event, raw string) {
	conn := e.connFor(ev.Network)
	if conn == nil || len(ev.Args) == 0 {
		return
	}
	target := ev.Args[0]
	e.pendingWhois[whoisKey(ev.Network, target)] = ev.Buffer
	conn.SendRaw(raw + " " + target)
}

// applyModeShorthand expands /op /deop /voice /devoice /halfop /dehalfop
// nick [nick…] into a single MODE line. Server limits typically cap at 4-6
// mode args per line; we don't chunk here (callers giving 10 ops at once
// is rare, and the server will reject neatly).
func applyModeShorthand(conn IRCConn, ev Event, name string) {
	if ev.Buffer == "" || len(ev.Args) == 0 {
		return
	}
	var flag byte
	sign := "+"
	switch name {
	case "op":
		flag = 'o'
	case "deop":
		flag, sign = 'o', "-"
	case "voice":
		flag = 'v'
	case "devoice":
		flag, sign = 'v', "-"
	case "halfop":
		flag = 'h'
	case "dehalfop":
		flag, sign = 'h', "-"
	}
	modes := sign + strings.Repeat(string(flag), len(ev.Args))
	conn.SendRaw("MODE " + ev.Buffer + " " + modes + " " + strings.Join(ev.Args, " "))
}

// kickCmd handles /kick <nick> [reason] in the current channel, and
// /kick #chan <nick> [reason] anywhere.
func kickCmd(conn IRCConn, ev Event) {
	if len(ev.Args) == 0 {
		return
	}
	chan_ := ev.Buffer
	nickIdx := 0
	// If the first arg looks like a channel, treat it as the target.
	first := ev.Args[0]
	if first != "" && (first[0] == '#' || first[0] == '&' || first[0] == '+' || first[0] == '!') {
		chan_ = first
		nickIdx = 1
	}
	if chan_ == "" || nickIdx >= len(ev.Args) {
		return
	}
	nick := ev.Args[nickIdx]
	reason := strings.Join(ev.Args[nickIdx+1:], " ")
	if reason != "" {
		conn.SendRaw("KICK " + chan_ + " " + nick + " :" + reason)
	} else {
		conn.SendRaw("KICK " + chan_ + " " + nick)
	}
}

// expandAlias substitutes $1..$9, $* (all args), and $N- (args from N
// onward) in an alias template. An unmatched placeholder expands to empty.
func expandAlias(tmpl string, args []string) string {
	arg := func(i int) string {
		if i >= 1 && i <= len(args) {
			return args[i-1]
		}
		return ""
	}
	var b strings.Builder
	for i := 0; i < len(tmpl); i++ {
		if tmpl[i] != '$' || i == len(tmpl)-1 {
			b.WriteByte(tmpl[i])
			continue
		}
		next := tmpl[i+1]
		switch {
		case next == '*':
			b.WriteString(strings.Join(args, " "))
			i++
		case next >= '1' && next <= '9':
			n := int(next - '0')
			// "$N-" means args from N to the end.
			if i+2 < len(tmpl) && tmpl[i+2] == '-' {
				if n <= len(args) {
					b.WriteString(strings.Join(args[n-1:], " "))
				}
				i += 2
			} else {
				b.WriteString(arg(n))
				i++
			}
		default:
			b.WriteByte('$')
		}
	}
	return b.String()
}

// engineAPI adapts *Engine to the core.API surface handed to the plugin host.
type engineAPI struct{ e *Engine }

// API returns the plugin-facing API for this engine.
func (e *Engine) API() API { return engineAPI{e} }

func (a engineAPI) conn(network string) IRCConn { return a.e.connFor(network) }

func (a engineAPI) Send(network, raw string) error {
	c := a.conn(network)
	if c == nil {
		return fmt.Errorf("unknown network %q", network)
	}
	return c.SendRaw(raw)
}

// sendSelf sends an outbound line via send and, unless the network echoes our
// own messages back (echo-message negotiated), injects a local self-copy into
// buffer so the sender still sees their line; kind tags that injected copy.
// A nil connection for network is an error and nothing is sent.
func (a engineAPI) sendSelf(network, target, text string, kind MsgKind, send func(IRCConn) error) error {
	c := a.conn(network)
	if c == nil {
		return fmt.Errorf("unknown network %q", network)
	}
	if err := send(c); err != nil {
		return err
	}
	if !a.e.echoMessage(network) {
		a.e.inject(Message{
			Network: network, Buffer: target, Time: time.Now(),
			From: a.Nick(network), Kind: kind, Text: text, Self: true,
		})
	}
	return nil
}

func (a engineAPI) Message(network, target, text string) error {
	return a.sendSelf(network, target, text, MsgPrivmsg, func(c IRCConn) error {
		return c.Message(target, text)
	})
}

func (a engineAPI) Notice(network, target, text string) error {
	return a.sendSelf(network, target, text, MsgNotice, func(c IRCConn) error {
		return c.SendRaw(fmt.Sprintf("NOTICE %s :%s", target, text))
	})
}

func (a engineAPI) Action(network, target, text string) error {
	return a.sendSelf(network, target, text, MsgAction, func(c IRCConn) error {
		return c.SendRaw(fmt.Sprintf("PRIVMSG %s :\x01ACTION %s\x01", target, text))
	})
}

func (a engineAPI) Join(network, channel string) error {
	c := a.conn(network)
	if c == nil {
		return fmt.Errorf("unknown network %q", network)
	}
	return c.SendRaw("JOIN " + channel)
}

func (a engineAPI) Part(network, channel string) error {
	c := a.conn(network)
	if c == nil {
		return fmt.Errorf("unknown network %q", network)
	}
	return c.SendRaw("PART " + channel)
}

func (a engineAPI) HoldJoins(network string) error    { return a.e.HoldJoins(network) }
func (a engineAPI) ReleaseJoins(network string) error { return a.e.ReleaseJoins(network) }

func (a engineAPI) Print(network, buffer, text string) {
	// Inject directly rather than via HandleEvent. A hook runs on the engine
	// loop goroutine (it blocks inside Dispatch), and that goroutine is the
	// sole consumer of e.events; routing Print back through that channel
	// deadlocks once the 256-deep buffer fills (the loop can't drain it while
	// parked in the hook). inject takes e.mu briefly and broadcasts, so it is
	// safe to call re-entrantly. evPrint bypasses hooks anyway (see handle),
	// so direct injection is behaviourally identical to the queued path.
	a.e.inject(Message{
		Network: network, Buffer: buffer, Time: time.Now(),
		Kind: MsgSystem, Text: text,
	})
}

// SetBufferState mutates the named buffer's State map under the engine
// lock and re-broadcasts the network snapshot so every sink (terminal log,
// store, every connected client) sees the change. Calling with a nil or
// empty map clears state entirely.
//
// The state is also remembered (setPendingStateLocked) and re-applied whenever
// the buffer is (re)created. So a plugin can set state for a buffer that does
// not exist yet — at script load, before JOIN — and it survives a daemon
// restart, where the live buffer is gone but is recreated on rejoin. fish.lua
// relies on this to keep the encrypted-buffer lock icon across reboots.
func (a engineAPI) SetBufferState(network, buffer string, state map[string]string) {
	a.e.mu.Lock()
	a.e.setPendingStateLocked(network, buffer, state)
	n := a.e.user.Network(network)
	if n == nil {
		a.e.mu.Unlock()
		return
	}
	c := n.Channel(buffer)
	if c == nil {
		a.e.mu.Unlock()
		return
	}
	if len(state) == 0 {
		c.State = nil
	} else {
		c.State = maps.Clone(state)
	}
	a.e.mu.Unlock()
	a.e.notifyNetwork(network)
}

// setPendingStateLocked records (state non-empty) or clears (state empty) the
// buffer state a plugin wants for network+buffer, so applyPendingStateLocked
// can apply it when the buffer is created. Buffer names are keyed
// case-insensitively, mirroring Network.Channel. Caller holds e.mu.
func (e *Engine) setPendingStateLocked(network, buffer string, state map[string]string) {
	bk := lower(buffer)
	if len(state) == 0 {
		if m := e.pendingState[network]; m != nil {
			delete(m, bk)
			if len(m) == 0 {
				delete(e.pendingState, network)
			}
		}
		return
	}
	m := e.pendingState[network]
	if m == nil {
		m = map[string]map[string]string{}
		e.pendingState[network] = m
	}
	m[bk] = maps.Clone(state)
}

// applyPendingStateLocked copies any remembered state onto a freshly created
// channel. Caller holds e.mu.
func (e *Engine) applyPendingStateLocked(network string, c *Channel) {
	if c == nil {
		return
	}
	st := e.pendingState[network][lower(c.Name)]
	if len(st) == 0 {
		return
	}
	c.State = maps.Clone(st)
}

func (a engineAPI) Networks() []NetworkInfo {
	u := a.e.Snapshot()
	out := make([]NetworkInfo, 0, len(u.Networks))
	for _, n := range u.Networks {
		out = append(out, NetworkInfo{ID: n.ID, Name: n.Name, Nick: n.Nick, State: string(n.State)})
	}
	return out
}

func (a engineAPI) Channels(network string) []ChannelInfo {
	n := a.e.SnapshotNetwork(network)
	if n == nil {
		return nil
	}
	out := make([]ChannelInfo, 0, len(n.Channels))
	for _, c := range n.Channels {
		out = append(out, ChannelInfo{Name: c.Name, Kind: string(c.Kind), Topic: c.Topic})
	}
	return out
}

func (a engineAPI) Members(network, channel string) []MemberInfo {
	n := a.e.SnapshotNetwork(network)
	if n == nil {
		return nil
	}
	c := n.Channel(channel)
	if c == nil {
		return nil
	}
	out := make([]MemberInfo, 0, len(c.Members))
	for _, m := range c.Members {
		out = append(out, MemberInfo{Nick: m.Nick, Account: m.Account, Modes: m.Modes, Away: m.Away})
	}
	return out
}

func (a engineAPI) Nick(network string) string {
	if n := a.e.SnapshotNetwork(network); n != nil {
		return n.Nick
	}
	return ""
}

func (a engineAPI) Backlog(network, buffer string, limit int) []MessageInfo {
	if a.e.history == nil {
		return nil
	}
	msgs, _, err := a.e.history.Backlog(context.Background(), network, buffer, 0, limit)
	if err != nil || len(msgs) == 0 {
		return nil
	}
	out := make([]MessageInfo, len(msgs))
	for i, m := range msgs {
		out[i] = MessageInfo{From: m.From, Text: m.Text, Time: m.Time}
	}
	return out
}
