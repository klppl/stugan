package plugin

import (
	"context"
	"testing"

	"github.com/klippelism/stugan/internal/core"
)

// loadIgnore mounts the bundled ignore.lua (internal/scripts/ignore.lua) so we
// can drive its commands and hook_message drop through the engine seams. This
// covers the script as it ships.
func loadIgnore(t *testing.T) (*Host, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{nickVal: "me"}
	h, err := New(Options{API: api, Dir: "../scripts"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })

	cmds := h.Commands()
	want := map[string]bool{"ignore": false, "unignore": false}
	for _, c := range cmds {
		if _, ok := want[c]; ok {
			want[c] = true
		}
	}
	for c, seen := range want {
		if !seen {
			t.Fatalf("ignore.lua did not register /%s (loaded commands: %v)", c, cmds)
		}
	}
	return h, api
}

// ignoreCmd runs /ignore or /unignore with args through the host.
func ignoreCmd(t *testing.T, h *Host, network, cmd string, args ...string) {
	t.Helper()
	if _, keep := h.Dispatch(context.Background(), core.Event{
		Type: core.EvCommand, Network: network, Channel: "#c",
		Command: cmd, Args: args,
	}); keep {
		t.Fatalf("/%s was not consumed by ignore.lua", cmd)
	}
}

// inFrom builds an incoming PRIVMSG event from a given sender.
func inFrom(network, from, text string) core.Event {
	return core.Event{
		Type: core.EvMessageIn, Network: network,
		Message: &core.Message{Network: network, Buffer: "#c", From: from, Kind: "privmsg", Text: text},
	}
}

func TestIgnoreDropsAndRestores(t *testing.T) {
	h, _ := loadIgnore(t)

	// Before ignoring: the message passes through.
	if _, keep := h.Dispatch(context.Background(), inFrom("n", "spammer", "hi")); !keep {
		t.Fatal("message dropped before /ignore")
	}

	ignoreCmd(t, h, "n", "ignore", "Spammer") // case-insensitive

	// Now their messages are dropped...
	if _, keep := h.Dispatch(context.Background(), inFrom("n", "spammer", "hi")); keep {
		t.Error("message from ignored nick was not dropped")
	}
	// ...but only on this network.
	if _, keep := h.Dispatch(context.Background(), inFrom("other", "spammer", "hi")); !keep {
		t.Error("ignore leaked across networks")
	}
	// ...and not from other nicks.
	if _, keep := h.Dispatch(context.Background(), inFrom("n", "friend", "hi")); !keep {
		t.Error("dropped a message from a non-ignored nick")
	}

	ignoreCmd(t, h, "n", "unignore", "spammer")

	// Restored.
	if _, keep := h.Dispatch(context.Background(), inFrom("n", "spammer", "hi")); !keep {
		t.Error("message still dropped after /unignore")
	}
}

func TestIgnoreNeverDropsOwnMessages(t *testing.T) {
	h, _ := loadIgnore(t)
	ignoreCmd(t, h, "n", "ignore", "me")
	ev := core.Event{
		Type: core.EvMessageIn, Network: "n",
		Message: &core.Message{Network: "n", Buffer: "#c", From: "me", Kind: "privmsg", Text: "yo", Self: true},
	}
	if _, keep := h.Dispatch(context.Background(), ev); !keep {
		t.Error("our own (echo) message was dropped by ignore")
	}
}

func TestIgnoreListing(t *testing.T) {
	h, api := loadIgnore(t)
	ignoreCmd(t, h, "n", "ignore", "bob", "alice")
	api.mu.Lock()
	api.prints = nil
	api.mu.Unlock()

	ignoreCmd(t, h, "n", "ignore") // no args → list

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.prints) == 0 {
		t.Fatal("/ignore with no args printed nothing")
	}
	last := api.prints[len(api.prints)-1][2]
	// Sorted, comma-joined.
	if last != "ignore: alice, bob" {
		t.Errorf("listing = %q, want %q", last, "ignore: alice, bob")
	}
}
