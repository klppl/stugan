package plugin

import (
	"context"
	"strings"
	"testing"

	"github.com/klippelism/stugan/internal/core"
)

// loadFish mounts the bundled fish.lua (internal/scripts/fish.lua) into a
// host so we can drive it through the engine seams (hook_input /
// hook_message / hook_command). This covers the script as it ships, so any
// regression in either the stugan.crypto bindings or fish.lua itself fails
// this test.
func loadFish(t *testing.T) (*Host, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{nickVal: "me"}
	h, err := New(Options{API: api, Dir: "../scripts"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })

	// Confirm fish actually loaded and claimed its commands.
	cmds := h.Commands()
	want := map[string]bool{"setkey": false, "setkey-ecb": false, "delkey": false, "key": false, "keyx": false}
	for _, c := range cmds {
		if _, ok := want[c]; ok {
			want[c] = true
		}
	}
	for c, seen := range want {
		if !seen {
			t.Fatalf("fish.lua did not register /%s (loaded commands: %v)", c, cmds)
		}
	}
	return h, api
}

// setKey drives the /setkey command through the host and waits for it to
// commit (the fake API records the local print).
func setKey(t *testing.T, h *Host, network, target, key, mode string) {
	t.Helper()
	cmd := "setkey"
	if mode == "ecb" {
		cmd = "setkey-ecb"
	}
	ev := core.Event{
		Type: core.EvCommand, Network: network, Channel: target,
		Command: cmd, Args: []string{target, key},
	}
	if _, keep := h.Dispatch(context.Background(), ev); keep {
		t.Fatalf("/setkey was not consumed by fish.lua")
	}
}

func TestFishPublishesBufferState(t *testing.T) {
	h, api := loadFish(t)
	setKey(t, h, "n", "#chan", "hunter2", "cbc")
	st := api.bufferState("n", "#chan")
	if st["encrypted"] != "cbc" {
		t.Errorf("after /setkey: state=%v, want encrypted=cbc", st)
	}

	// ECB on a different buffer surfaces as encrypted=ecb.
	setKey(t, h, "n", "#old", "k", "ecb")
	if got := api.bufferState("n", "#old")["encrypted"]; got != "ecb" {
		t.Errorf("after /setkey-ecb: encrypted=%q, want ecb", got)
	}

	// /delkey clears state entirely.
	h.Dispatch(context.Background(), core.Event{
		Type: core.EvCommand, Network: "n", Channel: "#chan",
		Command: "delkey", Args: []string{"#chan"},
	})
	if got := api.bufferState("n", "#chan"); got != nil {
		t.Errorf("after /delkey: state=%v, want nil", got)
	}
}

func TestFishCBCRoundTrip(t *testing.T) {
	h, _ := loadFish(t)
	setKey(t, h, "n", "#chan", "hunter2", "cbc")

	// Encrypt the outgoing line.
	out, keep := h.Dispatch(context.Background(), core.Event{
		Type: core.EvMessageOut, Network: "n",
		Message: &core.Message{Network: "n", Buffer: "#chan", Text: "hello world", Self: true},
	})
	if !keep {
		t.Fatal("hook_input dropped the outgoing line")
	}
	cipher := out.Message.Text
	if !strings.HasPrefix(cipher, "+OK *") {
		t.Fatalf("encrypted line missing +OK * prefix: %q", cipher)
	}
	if cipher == "hello world" {
		t.Fatal("hook_input did not encrypt (no key applied?)")
	}

	// Feed the same ciphertext back as an inbound message (this is what
	// echo-message delivers, and what a peer would send).
	in, keep := h.Dispatch(context.Background(), core.Event{
		Type: core.EvMessageIn, Network: "n",
		Message: &core.Message{
			Network: "n", Buffer: "#chan", From: "peer",
			Kind: core.MsgPrivmsg, Text: cipher,
		},
	})
	if !keep {
		t.Fatal("hook_message dropped the inbound encrypted line")
	}
	if in.Message.Text != "hello world" {
		t.Errorf("decrypt: got %q, want %q", in.Message.Text, "hello world")
	}
}

func TestFishECBRoundTrip(t *testing.T) {
	h, _ := loadFish(t)
	setKey(t, h, "n", "#legacy", "hunter2", "ecb")

	out, _ := h.Dispatch(context.Background(), core.Event{
		Type: core.EvMessageOut, Network: "n",
		Message: &core.Message{Network: "n", Buffer: "#legacy", Text: "legacy hi", Self: true},
	})
	cipher := out.Message.Text
	if !strings.HasPrefix(cipher, "+OK ") || strings.HasPrefix(cipher, "+OK *") {
		t.Fatalf("ECB ciphertext should start with %q (not %q) — got %q", "+OK ", "+OK *", cipher)
	}

	in, _ := h.Dispatch(context.Background(), core.Event{
		Type: core.EvMessageIn, Network: "n",
		Message: &core.Message{
			Network: "n", Buffer: "#legacy", From: "peer",
			Kind: core.MsgPrivmsg, Text: cipher,
		},
	})
	if in.Message.Text != "legacy hi" {
		t.Errorf("ECB decrypt: got %q, want %q", in.Message.Text, "legacy hi")
	}
}

func TestFishPassesThroughWithoutKey(t *testing.T) {
	h, _ := loadFish(t)

	// No key set for #plain: outgoing text must be unchanged.
	out, _ := h.Dispatch(context.Background(), core.Event{
		Type: core.EvMessageOut, Network: "n",
		Message: &core.Message{Network: "n", Buffer: "#plain", Text: "raw"},
	})
	if out.Message.Text != "raw" {
		t.Errorf("untouched: got %q, want %q", out.Message.Text, "raw")
	}

	// Inbound ciphertext from a peer for a buffer we don't have a key for:
	// passes through verbatim (we can't decrypt it, and we mustn't drop it).
	in, _ := h.Dispatch(context.Background(), core.Event{
		Type: core.EvMessageIn, Network: "n",
		Message: &core.Message{
			Network: "n", Buffer: "#plain", From: "peer",
			Kind: core.MsgPrivmsg, Text: "+OK *garbage",
		},
	})
	if in.Message.Text != "+OK *garbage" {
		t.Errorf("inbound passthrough: got %q", in.Message.Text)
	}
}

func TestFishDelkeyClearsKey(t *testing.T) {
	h, _ := loadFish(t)
	setKey(t, h, "n", "#chan", "hunter2", "cbc")

	// Verify a key is active first.
	out, _ := h.Dispatch(context.Background(), core.Event{
		Type: core.EvMessageOut, Network: "n",
		Message: &core.Message{Network: "n", Buffer: "#chan", Text: "before"},
	})
	if !strings.HasPrefix(out.Message.Text, "+OK *") {
		t.Fatal("precondition: key should be active before /delkey")
	}

	// Now drop the key and confirm passthrough resumes.
	h.Dispatch(context.Background(), core.Event{
		Type: core.EvCommand, Network: "n", Channel: "#chan",
		Command: "delkey", Args: []string{"#chan"},
	})
	out, _ = h.Dispatch(context.Background(), core.Event{
		Type: core.EvMessageOut, Network: "n",
		Message: &core.Message{Network: "n", Buffer: "#chan", Text: "after"},
	})
	if out.Message.Text != "after" {
		t.Errorf("after /delkey: got %q, want %q", out.Message.Text, "after")
	}
}

// IRC has a per-line limit; long plaintexts shouldn't crash the encrypt
// path. The plugin chunks at 220 bytes plaintext — well under what fits in
// a 510-byte wire line after ciphertext expansion + server framing.
func TestFishHandlesMultiBlockPlaintext(t *testing.T) {
	h, _ := loadFish(t)
	setKey(t, h, "n", "#chan", "k", "cbc")

	plaintext := strings.Repeat("abcdefgh", 20) // 160 bytes → still one chunk
	out, _ := h.Dispatch(context.Background(), core.Event{
		Type: core.EvMessageOut, Network: "n",
		Message: &core.Message{Network: "n", Buffer: "#chan", Text: plaintext},
	})
	if !strings.HasPrefix(out.Message.Text, "+OK *") {
		t.Fatalf("multi-block encrypt failed: %q", out.Message.Text)
	}
	in, _ := h.Dispatch(context.Background(), core.Event{
		Type: core.EvMessageIn, Network: "n",
		Message: &core.Message{
			Network: "n", Buffer: "#chan", From: "peer",
			Kind: core.MsgPrivmsg, Text: out.Message.Text,
		},
	})
	if in.Message.Text != plaintext {
		t.Errorf("multi-block round-trip mismatch (len got=%d want=%d)",
			len(in.Message.Text), len(plaintext))
	}
}

func TestFishSplitsLongLines(t *testing.T) {
	h, api := loadFish(t)
	setKey(t, h, "n", "#chan", "k", "cbc")

	// 700 bytes of plaintext → 4 chunks at 220/220/220/40. The first arrives
	// as the hook_input return value; the remaining three as raw PRIVMSGs.
	plaintext := strings.Repeat("x", 700)
	api.mu.Lock()
	api.sends = nil
	api.mu.Unlock()

	out, _ := h.Dispatch(context.Background(), core.Event{
		Type: core.EvMessageOut, Network: "n",
		Message: &core.Message{Network: "n", Buffer: "#chan", Text: plaintext},
	})
	if !strings.HasPrefix(out.Message.Text, "+OK *") {
		t.Fatalf("first chunk: got %q", out.Message.Text[:40])
	}
	// Other example scripts (auto_away.lua) may emit unrelated raw sends
	// from their timers — filter to PRIVMSGs into our buffer.
	var extras [][2]string
	for _, s := range api.sentRaw() {
		if strings.HasPrefix(s[1], "PRIVMSG #chan :") {
			extras = append(extras, s)
		}
	}
	if len(extras) != 3 {
		t.Fatalf("extra PRIVMSG chunks via stugan.send: got %d, want 3 (%v)", len(extras), extras)
	}
	for i, s := range extras {
		if !strings.HasPrefix(s[1], "PRIVMSG #chan :+OK *") {
			t.Errorf("chunk %d: bad shape %q", i, s[1])
		}
	}
}

func TestFishMeCommand(t *testing.T) {
	h, api := loadFish(t)

	// Without a key, /me delegates to the built-in action path → captured
	// by fakeAPI.Action which the test API aliases onto msgs.
	api.mu.Lock()
	api.msgs = nil
	api.sends = nil
	api.mu.Unlock()
	h.Dispatch(context.Background(), core.Event{
		Type: core.EvCommand, Network: "n", Channel: "#chan",
		Command: "me", Args: []string{"waves"},
	})
	msgs := api.sentMsgs()
	if len(msgs) != 1 || msgs[0] != [3]string{"n", "#chan", "waves"} {
		t.Fatalf("plain /me: got %v", msgs)
	}

	// With a key set, /me must NOT call Action — it must emit a raw
	// PRIVMSG with \x01ACTION <ciphertext>\x01 framing.
	setKey(t, h, "n", "#chan", "k", "cbc")
	api.mu.Lock()
	api.msgs = nil
	api.sends = nil
	api.mu.Unlock()
	h.Dispatch(context.Background(), core.Event{
		Type: core.EvCommand, Network: "n", Channel: "#chan",
		Command: "me", Args: []string{"waves"},
	})
	if len(api.sentMsgs()) != 0 {
		t.Errorf("keyed /me leaked plaintext via Action: %v", api.sentMsgs())
	}
	raw := api.sentRaw()
	if len(raw) != 1 {
		t.Fatalf("keyed /me raw sends: got %d, want 1 (%v)", len(raw), raw)
	}
	if !strings.HasPrefix(raw[0][1], "PRIVMSG #chan :\x01ACTION +OK *") ||
		!strings.HasSuffix(raw[0][1], "\x01") {
		t.Errorf("keyed /me framing: got %q", raw[0][1])
	}
}

func TestFishNoticeCommand(t *testing.T) {
	h, api := loadFish(t)
	setKey(t, h, "n", "#chan", "k", "cbc")

	// /notice <chan> hello → encrypted NOTICE on the wire.
	api.mu.Lock()
	api.msgs = nil
	api.sends = nil
	api.mu.Unlock()
	h.Dispatch(context.Background(), core.Event{
		Type: core.EvCommand, Network: "n", Channel: "#chan",
		Command: "notice", Args: []string{"#chan", "hello"},
	})
	raw := api.sentRaw()
	if len(raw) != 1 || !strings.HasPrefix(raw[0][1], "NOTICE #chan :+OK *") {
		t.Errorf("keyed /notice: got %v", raw)
	}
	if len(api.sentMsgs()) != 0 {
		t.Errorf("keyed /notice leaked plaintext: %v", api.sentMsgs())
	}

	// /notice to a buffer without a key falls back to the native notice path.
	api.mu.Lock()
	api.msgs = nil
	api.sends = nil
	api.mu.Unlock()
	h.Dispatch(context.Background(), core.Event{
		Type: core.EvCommand, Network: "n", Channel: "#chan",
		Command: "notice", Args: []string{"nobody", "hi"},
	})
	if got := api.sentMsgs(); len(got) != 1 || got[0] != [3]string{"n", "nobody", "hi"} {
		t.Errorf("plain /notice: got %v", got)
	}
}

// TestFishDH1080FullHandshake spins up two independent hosts running
// fish.lua and walks Alice + Bob through the real INIT/FINISH dance. If
// the prime, generator, exponent length, base64 variant, leading-zero
// trim, or SHA-256 derivation is wrong on either side, both ends end up
// with mismatching keys — caught here by encrypting on Alice's side and
// failing to decrypt on Bob's.
func TestFishDH1080FullHandshake(t *testing.T) {
	// Each side gets an independent host + fakeAPI so we can drain
	// outgoing NOTICEs per-party. loadFish points both at the same
	// internal/scripts directory — that's fine, both Hosts get their own
	// fresh Lua state and their own keystore.
	alice, aAPI := loadFish(t)
	bob, bAPI := loadFish(t)

	// 1) Alice runs /keyx bob.
	aAPI.mu.Lock()
	aAPI.sends = nil
	aAPI.mu.Unlock()
	alice.Dispatch(context.Background(), core.Event{
		Type: core.EvCommand, Network: "n", Channel: "bob",
		Command: "keyx", Args: []string{"bob"},
	})
	initNotice := findSend(aAPI, "NOTICE bob :DH1080_INIT ")
	if initNotice == "" {
		t.Fatalf("alice /keyx did not emit DH1080_INIT (sends=%v)", aAPI.sentRaw())
	}
	initBody := strings.TrimPrefix(initNotice, "NOTICE bob :")

	// 2) Bob receives the INIT as an inbound NOTICE from alice.
	bAPI.mu.Lock()
	bAPI.sends = nil
	bAPI.mu.Unlock()
	bob.Dispatch(context.Background(), core.Event{
		Type: core.EvMessageIn, Network: "n",
		Message: &core.Message{
			Network: "n", Buffer: "alice", From: "alice",
			Kind: core.MsgNotice, Text: initBody,
		},
	})
	finishNotice := findSend(bAPI, "NOTICE alice :DH1080_FINISH ")
	if finishNotice == "" {
		t.Fatalf("bob did not respond with DH1080_FINISH (sends=%v)", bAPI.sentRaw())
	}
	finishBody := strings.TrimPrefix(finishNotice, "NOTICE alice :")

	// 3) Alice receives Bob's FINISH.
	alice.Dispatch(context.Background(), core.Event{
		Type: core.EvMessageIn, Network: "n",
		Message: &core.Message{
			Network: "n", Buffer: "bob", From: "bob",
			Kind: core.MsgNotice, Text: finishBody,
		},
	})

	// Both sides should now have a CBC key for the other party. The
	// definitive test is that a roundtrip works: Alice encrypts a line
	// to bob, Bob decrypts the inbound from alice. (Buffer naming: for
	// outgoing PRIVMSG Alice→bob the buffer is "bob"; Bob receives an
	// inbound PRIVMSG from alice with buffer="alice".)
	out, _ := alice.Dispatch(context.Background(), core.Event{
		Type: core.EvMessageOut, Network: "n",
		Message: &core.Message{Network: "n", Buffer: "bob", Text: "ping"},
	})
	if !strings.HasPrefix(out.Message.Text, "+OK *") {
		t.Fatalf("post-handshake alice→bob not encrypted: %q", out.Message.Text)
	}
	in, _ := bob.Dispatch(context.Background(), core.Event{
		Type: core.EvMessageIn, Network: "n",
		Message: &core.Message{
			Network: "n", Buffer: "alice", From: "alice",
			Kind: core.MsgPrivmsg, Text: out.Message.Text,
		},
	})
	if in.Message.Text != "ping" {
		t.Errorf("DH1080-derived key didn't round-trip: got %q, want %q",
			in.Message.Text, "ping")
	}

	// And the other direction.
	out, _ = bob.Dispatch(context.Background(), core.Event{
		Type: core.EvMessageOut, Network: "n",
		Message: &core.Message{Network: "n", Buffer: "alice", Text: "pong"},
	})
	in, _ = alice.Dispatch(context.Background(), core.Event{
		Type: core.EvMessageIn, Network: "n",
		Message: &core.Message{
			Network: "n", Buffer: "bob", From: "bob",
			Kind: core.MsgPrivmsg, Text: out.Message.Text,
		},
	})
	if in.Message.Text != "pong" {
		t.Errorf("DH1080-derived key (reverse direction): got %q", in.Message.Text)
	}
}

// findSend returns the first raw send whose payload starts with prefix, or
// "" if none. Helper for the DH1080 test which has to pick a specific line
// out of whatever else the co-loaded example scripts emitted.
func findSend(api *fakeAPI, prefix string) string {
	for _, s := range api.sentRaw() {
		if strings.HasPrefix(s[1], prefix) {
			return s[1]
		}
	}
	return ""
}

// TestFishDH1080RejectsBadPubkey checks the conservative validator: degenerate
// peer pubkeys (0, 1, p) must be rejected before any modexp runs.
func TestFishDH1080RejectsBadPubkey(t *testing.T) {
	bob, bAPI := loadFish(t)
	bAPI.mu.Lock()
	bAPI.sends = nil
	bAPI.mu.Unlock()

	// "DH1080_INIT A" — that's dh-b64-encode of "\0", which validates as
	// y=0. The handler must consume the notice (the prefix is ours), warn,
	// and emit no FINISH.
	bob.Dispatch(context.Background(), core.Event{
		Type: core.EvMessageIn, Network: "n",
		Message: &core.Message{
			Network: "n", Buffer: "alice", From: "alice",
			Kind: core.MsgNotice, Text: "DH1080_INIT A",
		},
	})
	if got := findSend(bAPI, "NOTICE alice :DH1080_FINISH"); got != "" {
		t.Errorf("validator failed to reject y=0: got reply %q", got)
	}
}

func TestFishTopicCommand(t *testing.T) {
	h, api := loadFish(t)

	// No body → query topic, regardless of key state.
	api.mu.Lock()
	api.sends = nil
	api.mu.Unlock()
	h.Dispatch(context.Background(), core.Event{
		Type: core.EvCommand, Network: "n", Channel: "#chan", Command: "topic",
	})
	if raw := api.sentRaw(); len(raw) != 1 || raw[0][1] != "TOPIC #chan" {
		t.Errorf("topic query: got %v", raw)
	}

	// With a key, /topic <text> sends an encrypted body.
	setKey(t, h, "n", "#chan", "k", "cbc")
	api.mu.Lock()
	api.sends = nil
	api.mu.Unlock()
	h.Dispatch(context.Background(), core.Event{
		Type: core.EvCommand, Network: "n", Channel: "#chan",
		Command: "topic", Args: []string{"new", "topic"},
	})
	raw := api.sentRaw()
	if len(raw) != 1 || !strings.HasPrefix(raw[0][1], "TOPIC #chan :+OK *") {
		t.Errorf("keyed /topic set: got %v", raw)
	}
}
