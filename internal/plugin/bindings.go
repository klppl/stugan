package plugin

import (
	"fmt"
	"sort"
	"time"

	lua "github.com/yuin/gopher-lua"
)

const defaultPriority = 500

// newState creates a fresh LState for a script, optionally sandboxed.
func (h *Host) newState() *lua.LState {
	L := lua.NewState()
	if h.sandbox {
		for _, g := range []string{"dofile", "loadfile", "load", "loadstring", "require", "io", "package"} {
			L.SetGlobal(g, lua.LNil)
		}
		if osT, ok := L.GetGlobal("os").(*lua.LTable); ok {
			for _, k := range []string{"execute", "exit", "remove", "rename", "setenv", "tmpname", "getenv"} {
				osT.RawSetString(k, lua.LNil)
			}
		}
	}
	return L
}

// loadScript (re)loads one script file: it tears down any previous version,
// runs the file with a fresh stugan table, and registers whatever hooks the
// file declares. A load error leaves the script unregistered.
func (h *Host) loadScript(path string) {
	name := scriptName(path)
	h.unloadScript(name)

	s := &script{name: name, L: h.newState()}
	s.L.SetGlobal("stugan", h.buildAPI(s))
	if err := s.L.DoFile(path); err != nil {
		h.log.Error("plugin load failed", "script", name, "err", err)
		s.L.Close()
		return
	}
	h.scripts[name] = s
	h.log.Info("plugin loaded", "script", name)
}

// unloadScript removes a script's hooks and timers and closes its LState.
func (h *Host) unloadScript(name string) {
	s := h.scripts[name]
	if s == nil {
		return
	}
	h.msgHooks = dropHooks(h.msgHooks, s)
	h.inputHooks = dropHooks(h.inputHooks, s)
	for k := range h.signalHooks {
		h.signalHooks[k] = dropHooks(h.signalHooks[k], s)
	}
	for cmd, hk := range h.cmdHooks {
		if hk.script == s {
			delete(h.cmdHooks, cmd)
		}
	}
	var kept []*timerHook
	for _, t := range h.timers {
		if t.script == s {
			t.ticker.Stop()
			close(t.stop)
		} else {
			kept = append(kept, t)
		}
	}
	h.timers = kept
	delete(h.scripts, name)
	s.L.Close()
	h.log.Info("plugin unloaded", "script", name)
}

func dropHooks(hooks []*hook, s *script) []*hook {
	kept := hooks[:0:0]
	for _, hk := range hooks {
		if hk.script != s {
			kept = append(kept, hk)
		}
	}
	return kept
}

func (h *Host) newID() int { h.nextID++; return h.nextID }

// buildAPI constructs the per-script `stugan` table. Closures capture the
// host and the owning script, so script_name/config/kv resolve correctly
// and hooks register against the right script.
func (h *Host) buildAPI(s *script) *lua.LTable {
	t := s.L.NewTable()

	// Registration --------------------------------------------------------
	t.RawSetString("hook_command", s.L.NewFunction(func(L *lua.LState) int {
		name := L.CheckString(1)
		fn := L.CheckFunction(2)
		hk := &hook{script: s, fn: fn, prio: optPriority(L, 3), id: h.newID()}
		key := lc(name)
		h.cmdHooks[key] = hk
		h.unhookers[hk.id] = func() {
			if h.cmdHooks[key] == hk {
				delete(h.cmdHooks, key)
			}
		}
		L.Push(lua.LNumber(hk.id))
		return 1
	}))
	t.RawSetString("hook_message", s.L.NewFunction(func(L *lua.LState) int {
		return h.registerListHook(L, s, &h.msgHooks)
	}))
	t.RawSetString("hook_input", s.L.NewFunction(func(L *lua.LState) int {
		return h.registerListHook(L, s, &h.inputHooks)
	}))
	t.RawSetString("hook_signal", s.L.NewFunction(func(L *lua.LState) int {
		event := lc(L.CheckString(1))
		fn := L.CheckFunction(2)
		hk := &hook{script: s, fn: fn, prio: optPriority(L, 3), id: h.newID()}
		h.signalHooks[event] = append(h.signalHooks[event], hk)
		sortHooks(h.signalHooks[event])
		h.unhookers[hk.id] = func() { h.signalHooks[event] = removeHook(h.signalHooks[event], hk) }
		L.Push(lua.LNumber(hk.id))
		return 1
	}))
	t.RawSetString("hook_timer", s.L.NewFunction(func(L *lua.LState) int {
		return h.registerTimer(L, s)
	}))
	t.RawSetString("unhook", s.L.NewFunction(func(L *lua.LState) int {
		id := L.CheckInt(1)
		if u := h.unhookers[id]; u != nil {
			u()
			delete(h.unhookers, id)
		}
		return 0
	}))

	// Actions -------------------------------------------------------------
	t.RawSetString("send", s.L.NewFunction(func(L *lua.LState) int {
		return push2(L, h.api.Send(L.CheckString(1), L.CheckString(2)))
	}))
	t.RawSetString("message", s.L.NewFunction(func(L *lua.LState) int {
		return push2(L, h.api.Message(L.CheckString(1), L.CheckString(2), L.CheckString(3)))
	}))
	t.RawSetString("notice", s.L.NewFunction(func(L *lua.LState) int {
		return push2(L, h.api.Notice(L.CheckString(1), L.CheckString(2), L.CheckString(3)))
	}))
	t.RawSetString("action", s.L.NewFunction(func(L *lua.LState) int {
		return push2(L, h.api.Action(L.CheckString(1), L.CheckString(2), L.CheckString(3)))
	}))
	t.RawSetString("join", s.L.NewFunction(func(L *lua.LState) int {
		return push2(L, h.api.Join(L.CheckString(1), L.CheckString(2)))
	}))
	t.RawSetString("part", s.L.NewFunction(func(L *lua.LState) int {
		return push2(L, h.api.Part(L.CheckString(1), L.CheckString(2)))
	}))
	t.RawSetString("print", s.L.NewFunction(func(L *lua.LState) int {
		// print(network, buffer, text) or print(ctx_or_msg_table, text)
		if tbl, ok := L.Get(1).(*lua.LTable); ok {
			network := lvString(tbl.RawGetString("network"))
			buffer := lvString(tbl.RawGetString("buffer"))
			h.api.Print(network, buffer, L.CheckString(2))
			return 0
		}
		h.api.Print(L.CheckString(1), L.CheckString(2), L.CheckString(3))
		return 0
	}))

	// State reads ---------------------------------------------------------
	t.RawSetString("networks", s.L.NewFunction(func(L *lua.LState) int {
		arr := L.NewTable()
		for _, n := range h.api.Networks() {
			e := L.NewTable()
			e.RawSetString("id", lua.LString(n.ID))
			e.RawSetString("name", lua.LString(n.Name))
			e.RawSetString("nick", lua.LString(n.Nick))
			e.RawSetString("state", lua.LString(n.State))
			arr.Append(e)
		}
		L.Push(arr)
		return 1
	}))
	t.RawSetString("channels", s.L.NewFunction(func(L *lua.LState) int {
		arr := L.NewTable()
		for _, c := range h.api.Channels(L.CheckString(1)) {
			e := L.NewTable()
			e.RawSetString("name", lua.LString(c.Name))
			e.RawSetString("kind", lua.LString(c.Kind))
			e.RawSetString("topic", lua.LString(c.Topic))
			arr.Append(e)
		}
		L.Push(arr)
		return 1
	}))
	t.RawSetString("members", s.L.NewFunction(func(L *lua.LState) int {
		arr := L.NewTable()
		for _, m := range h.api.Members(L.CheckString(1), L.CheckString(2)) {
			e := L.NewTable()
			e.RawSetString("nick", lua.LString(m.Nick))
			e.RawSetString("account", lua.LString(m.Account))
			e.RawSetString("modes", lua.LString(m.Modes))
			e.RawSetString("away", lua.LBool(m.Away))
			arr.Append(e)
		}
		L.Push(arr)
		return 1
	}))
	t.RawSetString("nick", s.L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LString(h.api.Nick(L.CheckString(1))))
		return 1
	}))

	// Persistence, config, logging ---------------------------------------
	t.RawSetString("kv", h.buildKV(s))
	t.RawSetString("config", s.L.NewFunction(func(L *lua.LState) int {
		key := L.CheckString(1)
		if v, ok := h.settings[s.name][key]; ok {
			L.Push(goToLua(L, v))
			return 1
		}
		L.Push(L.Get(2)) // the default (or nil)
		return 1
	}))
	t.RawSetString("log", h.buildLog(s))
	t.RawSetString("script_name", lua.LString(s.name))

	return t
}

// registerListHook handles hook_message / hook_input registration.
func (h *Host) registerListHook(L *lua.LState, s *script, list *[]*hook) int {
	fn := L.CheckFunction(1)
	hk := &hook{script: s, fn: fn, prio: optPriority(L, 2), id: h.newID()}
	*list = append(*list, hk)
	sortHooks(*list)
	captured := list
	h.unhookers[hk.id] = func() { *captured = removeHook(*captured, hk) }
	L.Push(lua.LNumber(hk.id))
	return 1
}

// registerTimer handles hook_timer registration and starts its ticker.
func (h *Host) registerTimer(L *lua.LState, s *script) int {
	ms := L.CheckInt(1)
	fn := L.CheckFunction(2)
	if ms < 1 {
		ms = 1
	}
	t := &timerHook{
		script: s, fn: fn, id: h.newID(),
		ticker: time.NewTicker(time.Duration(ms) * time.Millisecond),
		stop:   make(chan struct{}),
	}
	h.timers = append(h.timers, t)
	h.unhookers[t.id] = func() { h.stopTimer(t) }

	h.wg.Go(func() {
		for {
			select {
			case <-t.ticker.C:
				h.do(func() { h.runTimer(t) })
			case <-t.stop:
				return
			case <-h.quit:
				return
			}
		}
	})
	L.Push(lua.LNumber(t.id))
	return 1
}

func (h *Host) stopTimer(t *timerHook) {
	for i, x := range h.timers {
		if x == t {
			t.ticker.Stop()
			close(t.stop)
			h.timers = append(h.timers[:i], h.timers[i+1:]...)
			return
		}
	}
}

func (h *Host) buildKV(s *script) *lua.LTable {
	kv := s.L.NewTable()
	store := func() map[string]string {
		if h.kv[s.name] == nil {
			h.kv[s.name] = map[string]string{}
		}
		return h.kv[s.name]
	}
	kv.RawSetString("set", s.L.NewFunction(func(L *lua.LState) int {
		store()[L.CheckString(1)] = L.ToStringMeta(L.Get(2)).String()
		return 0
	}))
	kv.RawSetString("get", s.L.NewFunction(func(L *lua.LState) int {
		if v, ok := store()[L.CheckString(1)]; ok {
			L.Push(lua.LString(v))
		} else {
			L.Push(lua.LNil)
		}
		return 1
	}))
	kv.RawSetString("delete", s.L.NewFunction(func(L *lua.LState) int {
		delete(store(), L.CheckString(1))
		return 0
	}))
	return kv
}

func (h *Host) buildLog(s *script) *lua.LTable {
	t := s.L.NewTable()
	add := func(name string, fn func(msg string, args ...any)) {
		t.RawSetString(name, s.L.NewFunction(func(L *lua.LState) int {
			fn(L.CheckString(1), "script", s.name)
			return 0
		}))
	}
	add("debug", h.log.Debug)
	add("info", h.log.Info)
	add("warn", h.log.Warn)
	add("error", h.log.Error)
	return t
}

// --- small helpers ---------------------------------------------------------

func optPriority(L *lua.LState, n int) int {
	if opts, ok := L.Get(n).(*lua.LTable); ok {
		if p, ok := opts.RawGetString("priority").(lua.LNumber); ok {
			return int(p)
		}
	}
	return defaultPriority
}

func sortHooks(hooks []*hook) {
	sort.SliceStable(hooks, func(i, j int) bool { return hooks[i].prio < hooks[j].prio })
}

func removeHook(hooks []*hook, target *hook) []*hook {
	for i, hk := range hooks {
		if hk == target {
			return append(hooks[:i], hooks[i+1:]...)
		}
	}
	return hooks
}

// push2 pushes (ok, err) for an action result: (true, nil) or (nil, "err").
func push2(L *lua.LState, err error) int {
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
	} else {
		L.Push(lua.LTrue)
		L.Push(lua.LNil)
	}
	return 2
}

func lvString(v lua.LValue) string {
	if s, ok := v.(lua.LString); ok {
		return string(s)
	}
	return ""
}

func lc(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// goToLua converts a config value (from TOML) into a Lua value.
func goToLua(L *lua.LState, v any) lua.LValue {
	switch x := v.(type) {
	case string:
		return lua.LString(x)
	case bool:
		return lua.LBool(x)
	case int64:
		return lua.LNumber(x)
	case float64:
		return lua.LNumber(x)
	case []any:
		arr := L.NewTable()
		for _, e := range x {
			arr.Append(goToLua(L, e))
		}
		return arr
	case map[string]any:
		t := L.NewTable()
		for k, e := range x {
			t.RawSetString(k, goToLua(L, e))
		}
		return t
	default:
		return lua.LString(fmt.Sprintf("%v", x))
	}
}
