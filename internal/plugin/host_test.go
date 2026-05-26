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

	ev := core.Event{Type: core.EvCommand, Network: "n", Channel: "#c", Command: "greet", Args: []string{"bob"}}
	if _, keep := h.Dispatch(context.Background(), ev); keep {
		t.Error("registered command was not consumed (keep=true)")
	}
	msgs := api.sentMsgs()
	if len(msgs) != 1 || msgs[0] != [3]string{"n", "bob", "hi bob"} {
		t.Fatalf("command sent %v", msgs)
	}

	// Unregistered command is not consumed.
	other := core.Event{Type: core.EvCommand, Network: "n", Channel: "#c", Command: "nope"}
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

	ev := core.Event{Type: core.EvJoin, Network: "n", Nick: "carol", Channel: "#c"}
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
