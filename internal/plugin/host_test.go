package plugin

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/klippelism/stugan/internal/core"
)

// fakeAPI records the actions scripts take.
type fakeAPI struct {
	mu      sync.Mutex
	msgs    [][3]string
	prints  [][3]string
	sends   [][2]string
	states  map[string]map[string]string // "<network>\t<buffer>" → state
	nickVal string
}

func (a *fakeAPI) Send(network, raw string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sends = append(a.sends, [2]string{network, raw})
	return nil
}
func (a *fakeAPI) Message(network, target, text string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.msgs = append(a.msgs, [3]string{network, target, text})
	return nil
}
func (a *fakeAPI) Notice(network, target, text string) error { return a.Message(network, target, text) }
func (a *fakeAPI) Action(network, target, text string) error { return a.Message(network, target, text) }
func (a *fakeAPI) Join(string, string) error                 { return nil }
func (a *fakeAPI) Part(string, string) error                 { return nil }
func (a *fakeAPI) Print(network, buffer, text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.prints = append(a.prints, [3]string{network, buffer, text})
}
func (a *fakeAPI) SetBufferState(network, buffer string, state map[string]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.states == nil {
		a.states = map[string]map[string]string{}
	}
	k := network + "\t" + buffer
	if len(state) == 0 {
		delete(a.states, k)
		return
	}
	clone := make(map[string]string, len(state))
	for kk, vv := range state {
		clone[kk] = vv
	}
	a.states[k] = clone
}
func (a *fakeAPI) bufferState(network, buffer string) map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.states[network+"\t"+buffer]
}
func (a *fakeAPI) Networks() []core.NetworkInfo {
	return []core.NetworkInfo{{ID: "n", Nick: a.nickVal}}
}
func (a *fakeAPI) Channels(string) []core.ChannelInfo       { return nil }
func (a *fakeAPI) Members(string, string) []core.MemberInfo { return nil }
func (a *fakeAPI) Nick(string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.nickVal
}

func (a *fakeAPI) sentMsgs() [][3]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([][3]string(nil), a.msgs...)
}
func (a *fakeAPI) sentRaw() [][2]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([][2]string(nil), a.sends...)
}

// newHost writes scripts to a temp dir and starts a host over them.
func newHost(t *testing.T, api core.API, scripts map[string]string, settings map[string]map[string]any) *Host {
	t.Helper()
	dir := t.TempDir()
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h, err := New(Options{API: api, Dir: dir, Settings: settings})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

func TestPluginManagement(t *testing.T) {
	api := &fakeAPI{nickVal: "me"}
	h := newHost(t, api, map[string]string{
		"greet.lua": `
			stugan.describe("say hi")
			stugan.hook_command("greet", function() end)`,
		"filter.lua": `stugan.hook_message(function(msg) return msg end)`,
	}, nil)

	byName := func() map[string]core.PluginInfo {
		m := map[string]core.PluginInfo{}
		for _, p := range h.Plugins() {
			m[p.Name] = p
		}
		return m
	}

	ps := byName()
	if len(ps) != 2 {
		t.Fatalf("Plugins() = %d entries, want 2: %+v", len(ps), ps)
	}
	if g := ps["greet"]; !g.Loaded || g.Description != "say hi" || len(g.Commands) != 1 || g.Commands[0] != "greet" {
		t.Errorf("greet info = %+v", g)
	}
	if f := ps["filter"]; !f.Loaded || f.Hooks != 1 {
		t.Errorf("filter info = %+v (want loaded, 1 hook)", f)
	}

	// Unload → still listed (file on disk) but not loaded, and its command gone.
	if err := h.UnloadPlugin("greet"); err != nil {
		t.Fatalf("UnloadPlugin: %v", err)
	}
	if g := byName()["greet"]; g.Loaded {
		t.Errorf("greet still loaded after unload: %+v", g)
	}
	if cmds := h.Commands(); len(cmds) != 0 {
		t.Errorf("Commands() = %v after unload, want none", cmds)
	}

	// Unloading again errors; reload brings it back.
	if err := h.UnloadPlugin("greet"); err == nil {
		t.Error("UnloadPlugin twice should error")
	}
	if err := h.LoadPlugin("greet"); err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}
	if g := byName()["greet"]; !g.Loaded {
		t.Errorf("greet not loaded after load: %+v", g)
	}
	if err := h.ReloadPlugin("filter"); err != nil {
		t.Fatalf("ReloadPlugin: %v", err)
	}

	// Bad names and missing files are rejected.
	if err := h.LoadPlugin("../escape"); err == nil {
		t.Error("LoadPlugin with path separator should error")
	}
	if err := h.LoadPlugin("nope"); err == nil {
		t.Error("LoadPlugin of missing file should error")
	}
}

func inMsg(text string) core.Event {
	return core.Event{Type: core.EvMessageIn, Network: "n", Message: &core.Message{
		Network: "n", Buffer: "#c", From: "alice", Kind: core.MsgPrivmsg, Text: text,
	}}
}

func TestHookMessageMutateAndDrop(t *testing.T) {
	api := &fakeAPI{}
	h := newHost(t, api, map[string]string{
		"filter.lua": `
			stugan.hook_message(function(msg)
			  if msg.text:lower():find("spoiler") then return nil end
			  msg.text = msg.text .. " [seen]"
			  return msg
			end)`,
	}, nil)

	// Dropped.
	if _, keep := h.Dispatch(context.Background(), inMsg("a spoiler!")); keep {
		t.Error("message with spoiler was not dropped")
	}
	// Mutated.
	out, keep := h.Dispatch(context.Background(), inMsg("hello"))
	if !keep {
		t.Fatal("plain message was dropped")
	}
	if out.Message.Text != "hello [seen]" {
		t.Errorf("text = %q, want %q", out.Message.Text, "hello [seen]")
	}
}

func TestHookCommand(t *testing.T) {
	api := &fakeAPI{nickVal: "me"}
	h := newHost(t, api, map[string]string{
		"greet.lua": `
			stugan.hook_command("greet", function(args, ctx)
			  stugan.message(ctx.network, args[1], "hi " .. args[1])
			end)`,
	}, nil)

	cmds := h.Commands()
	if len(cmds) != 1 || cmds[0] != "greet" {
		t.Fatalf("Commands() = %v, want [greet]", cmds)
	}

	ev := core.Event{Type: core.EvCommand, Network: "n", Buffer: "#c", Command: "greet", Args: []string{"bob"}}
	if _, keep := h.Dispatch(context.Background(), ev); keep {
		t.Error("registered command was not consumed (keep=true)")
	}
	msgs := api.sentMsgs()
	if len(msgs) != 1 || msgs[0] != [3]string{"n", "bob", "hi bob"} {
		t.Fatalf("command sent %v", msgs)
	}

	// Unregistered command is not consumed.
	other := core.Event{Type: core.EvCommand, Network: "n", Buffer: "#c", Command: "nope"}
	if _, keep := h.Dispatch(context.Background(), other); !keep {
		t.Error("unknown command was wrongly consumed")
	}
}

func TestHookSignal(t *testing.T) {
	api := &fakeAPI{}
	h := newHost(t, api, map[string]string{
		"welcome.lua": `
			stugan.hook_signal("join", function(s)
			  stugan.print(s.network, s.channel, s.nick .. " arrived")
			end)`,
	}, nil)

	ev := core.Event{Type: core.EvJoin, Network: "n", Nick: "carol", Buffer: "#c"}
	h.Dispatch(context.Background(), ev)
	if len(api.prints) != 1 || api.prints[0][2] != "carol arrived" {
		t.Fatalf("signal prints = %v", api.prints)
	}
}

func TestHookInputRewrite(t *testing.T) {
	api := &fakeAPI{nickVal: "me"}
	h := newHost(t, api, map[string]string{
		"upper.lua": `
			stugan.hook_input(function(input, ctx)
			  if input == "drop" then return nil end
			  return "<" .. ctx.nick .. "> " .. input
			end)`,
	}, nil)

	out, keep := h.Dispatch(context.Background(), core.Event{
		Type: core.EvMessageOut, Network: "n",
		Message: &core.Message{Network: "n", Buffer: "#c", Text: "hi", Self: true},
	})
	if !keep || out.Message.Text != "<me> hi" {
		t.Fatalf("input rewrite: keep=%v text=%q", keep, out.Message.Text)
	}

	if _, keep := h.Dispatch(context.Background(), core.Event{
		Type: core.EvMessageOut, Network: "n",
		Message: &core.Message{Network: "n", Buffer: "#c", Text: "drop"},
	}); keep {
		t.Error("input hook returning nil did not drop the line")
	}
}

func TestHookCompletion(t *testing.T) {
	api := &fakeAPI{nickVal: "me"}
	h := newHost(t, api, map[string]string{
		// One hook that completes "@team" mentions, and a second whose
		// candidates are gathered alongside the first's (flattened).
		"complete.lua": `
			stugan.hook_completion(function(word, ctx)
			  if word == "@te" then return { "@dev", "@ops" } end
			  return nil
			end)
			stugan.hook_completion(function(word, ctx)
			  if word == "@te" then return { "@all" } end
			end)`,
	}, nil)

	got := h.Complete("@te", "n", "#c")
	want := map[string]bool{"@dev": true, "@ops": true, "@all": true}
	if len(got) != len(want) {
		t.Fatalf("Complete returned %v, want 3 items", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected completion %q", g)
		}
	}

	// A word no hook matches yields nothing.
	if c := h.Complete("xyz", "n", "#c"); len(c) != 0 {
		t.Errorf("Complete(xyz) = %v, want empty", c)
	}
}

func TestErrorIsolation(t *testing.T) {
	api := &fakeAPI{}
	h := newHost(t, api, map[string]string{
		"bad.lua": `stugan.hook_message(function(msg) error("boom") end)`,
	}, nil)
	// An erroring hook must not crash the host or drop the message.
	out, keep := h.Dispatch(context.Background(), inMsg("hello"))
	if !keep {
		t.Error("message dropped by an erroring hook")
	}
	if out.Message.Text != "hello" {
		t.Errorf("text changed by erroring hook: %q", out.Message.Text)
	}
}

func TestConfigAccess(t *testing.T) {
	api := &fakeAPI{}
	h := newHost(t, api, map[string]string{
		"cfg.lua": `
			local word = stugan.config("word", "default")
			stugan.hook_message(function(msg)
			  msg.text = word
			  return msg
			end)`,
	}, map[string]map[string]any{
		"cfg": {"word": "configured"},
	})
	out, _ := h.Dispatch(context.Background(), inMsg("x"))
	if out.Message.Text != "configured" {
		t.Errorf("config value = %q, want configured", out.Message.Text)
	}
}

// The scripts shipped in docs/examples must load against the real API.
func TestExampleScriptsLoad(t *testing.T) {
	api := &fakeAPI{nickVal: "me"}
	h, err := New(Options{API: api, Dir: "../../docs/examples"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	cmds := h.Commands()
	found := false
	for _, c := range cmds {
		if c == "greet" {
			found = true
		}
	}
	if !found {
		t.Errorf("example scripts did not register /greet; commands=%v", cmds)
	}
}

func TestHotReload(t *testing.T) {
	api := &fakeAPI{}
	dir := t.TempDir()
	path := filepath.Join(dir, "r.lua")
	write := func(suffix string) {
		body := `stugan.hook_message(function(msg) msg.text = "` + suffix + `"; return msg end)`
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("v1")
	h, err := New(Options{API: api, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })

	out, _ := h.Dispatch(context.Background(), inMsg("x"))
	if out.Message.Text != "v1" {
		t.Fatalf("before reload: %q", out.Message.Text)
	}

	// Overwrite and wait for the fsnotify watcher to reload (debounced).
	write("v2")
	deadline := time.After(4 * time.Second)
	for {
		out, _ := h.Dispatch(context.Background(), inMsg("x"))
		if out.Message.Text == "v2" {
			return // reloaded
		}
		select {
		case <-deadline:
			t.Fatalf("script did not hot-reload; still %q", out.Message.Text)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// fakeKV is an in-memory plugin.KV used to assert write-through and
// lazy-load behavior without bringing the SQLite store into the plugin
// package (which would import a cycle).
type fakeKV struct {
	mu   sync.Mutex
	data map[string]map[string]string
}

func newFakeKV() *fakeKV { return &fakeKV{data: map[string]map[string]string{}} }

func (k *fakeKV) GetAll(script string) map[string]string {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := map[string]string{}
	for kk, vv := range k.data[script] {
		out[kk] = vv
	}
	return out
}
func (k *fakeKV) Set(script, key, value string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.data[script] == nil {
		k.data[script] = map[string]string{}
	}
	k.data[script][key] = value
	return nil
}
func (k *fakeKV) Delete(script, key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.data[script], key)
	return nil
}

func TestPluginKVPersistsAcrossHosts(t *testing.T) {
	api := &fakeAPI{}
	kv := newFakeKV()

	// Host A writes a value through the persistent KV.
	dir := t.TempDir()
	writeScript := func(body string) {
		path := filepath.Join(dir, "p.lua")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeScript(`
		stugan.hook_command("save", function(args)
		  stugan.kv.set("k", args[1])
		end)
		stugan.hook_command("load", function(_, ctx)
		  stugan.print(ctx, stugan.kv.get("k") or "<unset>")
		end)
	`)
	hA, err := New(Options{API: api, Dir: dir, KV: kv})
	if err != nil {
		t.Fatal(err)
	}
	hA.Dispatch(context.Background(), core.Event{
		Type: core.EvCommand, Network: "n", Buffer: "#c",
		Command: "save", Args: []string{"hello"},
	})
	hA.Close()

	// The KV backing must show the write — that proves write-through happened.
	if got := kv.GetAll("p")["k"]; got != "hello" {
		t.Fatalf("write-through: kv=%v", got)
	}

	// Host B starts fresh against the same KV and reads the value via Lua
	// — that proves lazy-load on first kv.get pulls from the backing store.
	api2 := &fakeAPI{}
	hB, err := New(Options{API: api2, Dir: dir, KV: kv})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hB.Close() })
	hB.Dispatch(context.Background(), core.Event{
		Type: core.EvCommand, Network: "n", Buffer: "#c", Command: "load",
	})
	prints := api2.prints
	if len(prints) != 1 || prints[0][2] != "hello" {
		t.Fatalf("reload-from-kv: prints=%v", prints)
	}
}

func TestPluginSettings(t *testing.T) {
	api := &fakeAPI{nickVal: "me"}
	h := newHost(t, api, map[string]string{
		"s.lua": `
			local applied = "?"
			local function apply_n(v) applied = "n=" .. v end
			apply_n(stugan.setting("count", { type = "number", default = 3, label = "Count", help = "how many", apply = apply_n }))
			stugan.setting("mode", { type = "select", options = { "a", "b" }, default = "a", label = "Mode" })
			stugan.setting("token", { type = "text", default = "sekret", secret = true })
			stugan.hook_command("show", function(_, ctx) stugan.print(ctx, applied) end)
		`,
	}, nil)

	smap := func() map[string]core.PluginSetting {
		for _, p := range h.Plugins() {
			if p.Name == "s" {
				m := map[string]core.PluginSetting{}
				for _, s := range p.Settings {
					m[s.Name] = s
				}
				return m
			}
		}
		t.Fatal("script s not found")
		return nil
	}

	// Declared metadata and default values.
	m := smap()
	if len(m) != 3 {
		t.Fatalf("settings: got %d, want 3 (%+v)", len(m), m)
	}
	if s := m["count"]; s.Type != "number" || s.Default != "3" || s.Value != "3" || s.Label != "Count" || s.Help != "how many" {
		t.Fatalf("count setting wrong: %+v", s)
	}
	if s := m["mode"]; s.Type != "select" || len(s.Options) != 2 || s.Options[0] != "a" || s.Value != "a" {
		t.Fatalf("mode setting wrong: %+v", s)
	}
	// A secret value is withheld even though a default was declared.
	if s := m["token"]; !s.Secret || s.Value != "" {
		t.Fatalf("token secret not withheld: %+v", s)
	}

	// The script initialized its own state from the setting's return value.
	showApplied := func() string {
		api.prints = nil
		h.Dispatch(context.Background(), core.Event{Type: core.EvCommand, Network: "n", Buffer: "#c", Command: "show"})
		if len(api.prints) != 1 {
			t.Fatalf("show: prints=%v", api.prints)
		}
		return api.prints[0][2]
	}
	if got := showApplied(); got != "n=3" {
		t.Fatalf("initial apply: got %q want n=3", got)
	}

	// A valid change persists and runs the apply callback.
	if err := h.SetPluginSetting("s", "count", "7"); err != nil {
		t.Fatalf("set count: %v", err)
	}
	if v := smap()["count"].Value; v != "7" {
		t.Fatalf("count value after set: %q", v)
	}
	if got := showApplied(); got != "n=7" {
		t.Fatalf("apply after set: got %q want n=7", got)
	}

	// Validation: non-number, bad select option, unknown setting, unknown script.
	if err := h.SetPluginSetting("s", "count", "abc"); err == nil {
		t.Fatal("expected error for non-number value")
	}
	if err := h.SetPluginSetting("s", "mode", "c"); err == nil {
		t.Fatal("expected error for invalid select option")
	}
	if err := h.SetPluginSetting("s", "nope", "x"); err == nil {
		t.Fatal("expected error for unknown setting")
	}
	if err := h.SetPluginSetting("nosuch", "count", "1"); err == nil {
		t.Fatal("expected error for unknown script")
	}
	// A valid select option is accepted.
	if err := h.SetPluginSetting("s", "mode", "b"); err != nil {
		t.Fatalf("set mode b: %v", err)
	}
}

func TestHookTimer(t *testing.T) {
	api := &fakeAPI{}
	newHost(t, api, map[string]string{
		"tick.lua": `stugan.hook_timer(20, function() stugan.send("n", "TICK") end)`,
	}, nil)

	deadline := time.After(3 * time.Second)
	for {
		if len(api.sentRaw()) > 0 {
			return // timer fired
		}
		select {
		case <-deadline:
			t.Fatal("timer never fired")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
