package plugin

import (
	lua "github.com/yuin/gopher-lua"

	"github.com/klippelism/stugan/internal/core"
)

// msgToTable renders a core.Message as the Lua table hook_message receives.
func msgToTable(L *lua.LState, m *core.Message) *lua.LTable {
	t := L.NewTable()
	t.RawSetString("id", lua.LString(m.ID))
	t.RawSetString("network", lua.LString(m.Network))
	t.RawSetString("buffer", lua.LString(m.Buffer))
	t.RawSetString("from", lua.LString(m.From))
	t.RawSetString("account", lua.LString(m.Account))
	t.RawSetString("kind", lua.LString(string(m.Kind)))
	t.RawSetString("text", lua.LString(m.Text))
	t.RawSetString("self", lua.LBool(m.Self))
	t.RawSetString("time", lua.LNumber(m.Time.Unix()))
	tags := L.NewTable()
	for k, v := range m.Tags {
		tags.RawSetString(k, lua.LString(v))
	}
	t.RawSetString("tags", tags)
	return t
}

// tableToMsg applies a hook's returned table back onto base, picking up the
// fields a script is allowed to rewrite (text, buffer, kind).
func tableToMsg(t *lua.LTable, base core.Message) core.Message {
	m := base
	if s, ok := t.RawGetString("text").(lua.LString); ok {
		m.Text = string(s)
	}
	if s, ok := t.RawGetString("buffer").(lua.LString); ok {
		m.Buffer = string(s)
	}
	if s, ok := t.RawGetString("kind").(lua.LString); ok {
		m.Kind = core.MsgKind(string(s))
	}
	return m
}

// topicTable builds the table passed to hook_topic. text is threaded through
// separately so a hook sees the topic as rewritten by an earlier hook in the
// chain, not the original ev.Text. nick is empty for the topic delivered on
// join (RPL_TOPIC); set to the setter for a live change.
func topicTable(L *lua.LState, ev core.Event, text string) *lua.LTable {
	t := L.NewTable()
	t.RawSetString("network", lua.LString(ev.Network))
	t.RawSetString("buffer", lua.LString(ev.Buffer))
	t.RawSetString("nick", lua.LString(ev.Nick))
	t.RawSetString("text", lua.LString(text))
	return t
}

// ctxTable builds the context table passed to hook_command / hook_input.
func ctxTable(L *lua.LState, network, buffer, nick string) *lua.LTable {
	t := L.NewTable()
	t.RawSetString("network", lua.LString(network))
	t.RawSetString("buffer", lua.LString(buffer))
	t.RawSetString("kind", lua.LString(bufferKind(buffer)))
	t.RawSetString("nick", lua.LString(nick))
	return t
}

// signalTable builds the table passed to hook_signal callbacks.
func signalTable(L *lua.LState, ev core.Event) *lua.LTable {
	t := L.NewTable()
	t.RawSetString("network", lua.LString(ev.Network))
	t.RawSetString("nick", lua.LString(ev.Nick))
	t.RawSetString("new_nick", lua.LString(ev.NewNick))
	t.RawSetString("channel", lua.LString(ev.Buffer))
	t.RawSetString("account", lua.LString(ev.Account))
	t.RawSetString("text", lua.LString(ev.Text))
	t.RawSetString("kicker", lua.LString(ev.Kicker))
	return t
}

// stringArray turns a Go string slice into a 1-indexed Lua array.
func stringArray(L *lua.LState, ss []string) *lua.LTable {
	t := L.NewTable()
	for _, s := range ss {
		t.Append(lua.LString(s))
	}
	return t
}

// bufferKind classifies a buffer name for the ctx table.
func bufferKind(name string) string {
	if name == "" {
		return "status"
	}
	switch name[0] {
	case '#', '&', '+', '!':
		return "channel"
	default:
		return "query"
	}
}

// signalName maps an event type to the hook_signal name scripts subscribe to.
func signalName(t core.EventType) (string, bool) {
	switch t {
	case core.EvJoin:
		return "join", true
	case core.EvPart:
		return "part", true
	case core.EvKick:
		return "kick", true
	case core.EvQuit:
		return "quit", true
	case core.EvNick:
		return "nick", true
	case core.EvTopic:
		return "topic", true
	case core.EvConnect:
		return "connect", true
	case core.EvDisconnect:
		return "disconnect", true
	case core.EvMode:
		return "mode", true
	default:
		return "", false
	}
}
