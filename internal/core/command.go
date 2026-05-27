package core

import (
	"fmt"
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
		e.API().Action(ev.Network, ev.Channel, ev.Text)
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
		if len(ev.Args) > 0 {
			conn.SendRaw("JOIN " + ev.Args[0])
		}
	case "part":
		ch := ev.Channel
		if len(ev.Args) > 0 {
			ch = ev.Args[0]
		}
		conn.SendRaw("PART " + ch)
	case "topic":
		// /topic <text> sets the current channel's topic; /topic alone
		// queries it.
		if ev.Text == "" {
			conn.SendRaw("TOPIC " + ev.Channel)
		} else {
			conn.SendRaw("TOPIC " + ev.Channel + " :" + ev.Text)
		}
	case "nick":
		if len(ev.Args) > 0 {
			conn.SendRaw("NICK " + ev.Args[0])
		}
	case "quit":
		conn.SendRaw("QUIT :" + ev.Text)
	case "raw", "quote":
		conn.SendRaw(ev.Text)
	default:
		e.inject(Message{
			Network: ev.Network, Buffer: ev.Channel, Time: time.Now(),
			Kind: MsgSystem, Text: "unknown command: " + ev.Command,
		})
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

func (a engineAPI) Message(network, target, text string) error {
	c := a.conn(network)
	if c == nil {
		return fmt.Errorf("unknown network %q", network)
	}
	if err := c.Message(target, text); err != nil {
		return err
	}
	a.e.inject(Message{
		Network: network, Buffer: target, Time: time.Now(),
		From: a.Nick(network), Kind: MsgPrivmsg, Text: text, Self: true,
	})
	return nil
}

func (a engineAPI) Notice(network, target, text string) error {
	c := a.conn(network)
	if c == nil {
		return fmt.Errorf("unknown network %q", network)
	}
	if err := c.SendRaw(fmt.Sprintf("NOTICE %s :%s", target, text)); err != nil {
		return err
	}
	a.e.inject(Message{
		Network: network, Buffer: target, Time: time.Now(),
		From: a.Nick(network), Kind: MsgNotice, Text: text, Self: true,
	})
	return nil
}

func (a engineAPI) Action(network, target, text string) error {
	c := a.conn(network)
	if c == nil {
		return fmt.Errorf("unknown network %q", network)
	}
	if err := c.SendRaw(fmt.Sprintf("PRIVMSG %s :\x01ACTION %s\x01", target, text)); err != nil {
		return err
	}
	a.e.inject(Message{
		Network: network, Buffer: target, Time: time.Now(),
		From: a.Nick(network), Kind: MsgAction, Text: text, Self: true,
	})
	return nil
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

func (a engineAPI) Print(network, buffer, text string) {
	a.e.HandleEvent(Event{Type: evPrint, Network: network, Channel: buffer, Text: text, Time: time.Now()})
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
