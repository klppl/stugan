package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReconnectDelay(t *testing.T) {
	tests := []struct {
		name      string
		backoff   time.Duration
		lasted    time.Duration
		wantSleep time.Duration
		wantNext  time.Duration
	}{
		{"first drop", baseBackoff, 0, time.Second, 2 * time.Second},
		{"grows while flapping", 4 * time.Second, time.Second, 4 * time.Second, 8 * time.Second},
		{"caps at max", maxBackoff, time.Second, maxBackoff, maxBackoff},
		{"doubling clamps to max", 20 * time.Second, time.Second, 20 * time.Second, maxBackoff},
		{"stable connection resets", maxBackoff, stableFor, baseBackoff, 2 * time.Second},
		{"just-under-stable does not reset", maxBackoff, stableFor - time.Millisecond, maxBackoff, maxBackoff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sleep, next := reconnectDelay(tt.backoff, tt.lasted)
			if sleep != tt.wantSleep || next != tt.wantNext {
				t.Errorf("reconnectDelay(%v, %v) = (%v, %v), want (%v, %v)",
					tt.backoff, tt.lasted, sleep, next, tt.wantSleep, tt.wantNext)
			}
		})
	}
}

// captureSink records printed lines and network snapshots for assertions.
type captureSink struct {
	msgs      []Message
	nets      []*Network
	removed   []string
	reordered [][]string
	lists     [][]ChannelListItem
	reacts    [][5]string // network, buffer, target, nick, reaction
	redacts   [][5]string // network, buffer, target, nick, reason
}

func (c *captureSink) Print(m Message)                { c.msgs = append(c.msgs, m) }
func (c *captureSink) NetworkChanged(n *Network)      { c.nets = append(c.nets, n) }
func (c *captureSink) NetworkRemoved(id string)       { c.removed = append(c.removed, id) }
func (c *captureSink) NetworksReordered(ids []string) { c.reordered = append(c.reordered, ids) }
func (c *captureSink) ChannelList(_ string, items []ChannelListItem) {
	c.lists = append(c.lists, items)
}
func (c *captureSink) Typing(string, string, string, string) {}
func (c *captureSink) React(network, buffer, target, nick, reaction string) {
	c.reacts = append(c.reacts, [5]string{network, buffer, target, nick, reaction})
}
func (c *captureSink) Redact(network, buffer, target, nick, reason string) {
	c.redacts = append(c.redacts, [5]string{network, buffer, target, nick, reason})
}

// fakeConnector / fakeRuntimeConn back runtime AddNetworkLive in tests.
type fakeConnector struct {
	dialed int
	caps   []string // stamped onto dialed connections
	conns  []*fakeRuntimeConn
}

func (f *fakeConnector) Dial(NetworkParams, ConnHandler) (IRCConn, error) {
	f.dialed++
	c := &fakeRuntimeConn{caps: f.caps}
	f.conns = append(f.conns, c)
	return c, nil
}

type fakeRuntimeConn struct {
	mu   sync.Mutex
	raws []string
	caps []string
}

func (c *fakeRuntimeConn) Connect(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
func (c *fakeRuntimeConn) SendRaw(s string) error {
	c.mu.Lock()
	c.raws = append(c.raws, s)
	c.mu.Unlock()
	return nil
}
func (c *fakeRuntimeConn) Message(target, text string) error {
	c.mu.Lock()
	c.raws = append(c.raws, "PRIVMSG "+target+" :"+text)
	c.mu.Unlock()
	return nil
}
func (c *fakeRuntimeConn) Caps() []string      { return c.caps }
func (c *fakeRuntimeConn) CurrentNick() string { return "" }
func (c *fakeRuntimeConn) Close() error        { return nil }

// rawsSnap returns a copy of the sent raw lines, safe to read while the
// engine loop may still be writing (used by tests that run the loop).
func (c *fakeRuntimeConn) rawsSnap() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.raws...)
}

// recordingNetStore records SaveNetwork/DeleteNetwork calls.
type recordingNetStore struct {
	saved   []NetworkParams
	deleted []string
}

func (r *recordingNetStore) SaveNetwork(p NetworkParams) error {
	r.saved = append(r.saved, p)
	return nil
}
func (r *recordingNetStore) DeleteNetwork(id string) error {
	r.deleted = append(r.deleted, id)
	return nil
}

// newTestEngine returns an engine with one registered network and a
// capture sink, without starting the run loop (apply is exercised directly).
func newTestEngine(t *testing.T) (*Engine, *captureSink) {
	t.Helper()
	sink := &captureSink{}
	e := New(Options{Sink: sink})
	e.AddNetwork(NetworkParams{ID: "net", Name: "net", Nick: "me"}, nil)
	return e, sink
}

func net0(e *Engine) *Network { return e.user.Network("net") }

func TestApplyConnectDisconnect(t *testing.T) {
	e, _ := newTestEngine(t)

	e.apply(Event{Type: evSetState, Network: "net", State: StateConnecting})
	if got := net0(e).State; got != StateConnecting {
		t.Fatalf("state = %q, want connecting", got)
	}

	e.apply(Event{Type: EvConnect, Network: "net", Nick: "me2"})
	if got := net0(e).State; got != StateRegistered {
		t.Fatalf("state = %q, want registered", got)
	}
	if got := net0(e).Nick; got != "me2" {
		t.Fatalf("nick = %q, want me2", got)
	}

	e.apply(Event{Type: EvDisconnect, Network: "net", Text: "bye"})
	if got := net0(e).State; got != StateDisconnected {
		t.Fatalf("state = %q, want disconnected", got)
	}
}

func TestApplyJoinPartQuit(t *testing.T) {
	e, _ := newTestEngine(t)

	e.apply(Event{Type: EvJoin, Network: "net", Nick: "alice", Buffer: "#go"})
	c := net0(e).Channel("#go")
	if c == nil {
		t.Fatal("channel #go not created on join")
	}
	if c.Kind != KindChannel {
		t.Errorf("kind = %q, want channel", c.Kind)
	}
	if _, ok := c.Members["alice"]; !ok {
		t.Fatalf("alice not a member: %+v", c.Members)
	}

	// Case-insensitive: joining with different case must not duplicate.
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "bob", Buffer: "#GO"})
	if len(net0(e).Channels) != 1 {
		t.Fatalf("channel duplicated by case: %d channels", len(net0(e).Channels))
	}
	if len(net0(e).Channel("#go").Members) != 2 {
		t.Fatalf("members = %d, want 2", len(net0(e).Channel("#go").Members))
	}

	e.apply(Event{Type: EvPart, Network: "net", Nick: "alice", Buffer: "#go", Text: "later"})
	if _, ok := net0(e).Channel("#go").Members["alice"]; ok {
		t.Error("alice still present after part")
	}

	e.apply(Event{Type: EvQuit, Network: "net", Nick: "bob", Text: "ping timeout"})
	if _, ok := net0(e).Channel("#go").Members["bob"]; ok {
		t.Error("bob still present after quit")
	}
}

// A reconnect must not leave phantom members behind. While disconnected we miss
// the QUIT/PART traffic that retires members (including our own ghost nick), and
// the NAMES burst on rejoin only adds, so EvDisconnect has to clear the lists.
func TestDisconnectClearsMembers(t *testing.T) {
	e, _ := newTestEngine(t)

	e.apply(Event{Type: EvJoin, Network: "net", Nick: "alice", Buffer: "#go"})
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "bob", Buffer: "#go"})
	if got := len(net0(e).Channel("#go").Members); got != 2 {
		t.Fatalf("members = %d, want 2 before disconnect", got)
	}

	e.apply(Event{Type: EvDisconnect, Network: "net", Text: "ping timeout"})
	if got := len(net0(e).Channel("#go").Members); got != 0 {
		t.Fatalf("members = %d, want 0 after disconnect", got)
	}

	// Rejoin: the NAMES burst rebuilds the list authoritatively. bob is gone,
	// so it must not reappear; the carried-over entry would have been a phantom.
	e.apply(Event{Type: EvNames, Network: "net", Buffer: "#go", Members: []Member{
		{Nick: "alice"}, {Nick: "carol"},
	}})
	c := net0(e).Channel("#go")
	if _, ok := c.Members["bob"]; ok {
		t.Error("phantom bob survived the reconnect")
	}
	if got := len(c.Members); got != 2 {
		t.Fatalf("members = %d, want 2 after rejoin", got)
	}
}

func TestApplyNames(t *testing.T) {
	e, sink := newTestEngine(t)
	e.apply(Event{Type: EvNames, Network: "net", Buffer: "#go", Members: []Member{
		{Nick: "alice"}, {Nick: "bob", Modes: "@"}, {Nick: "carol", Modes: "+"},
	}})
	c := net0(e).Channel("#go")
	if c == nil || len(c.Members) != 3 {
		t.Fatalf("members not populated from NAMES: %+v", c)
	}
	if c.Members["bob"].Modes != "@" {
		t.Errorf("bob modes = %q, want @", c.Members["bob"].Modes)
	}
	// NAMES must not emit join system lines.
	for _, m := range sink.msgs {
		if m.Kind == MsgSystem {
			t.Errorf("NAMES emitted a system line: %q", m.Text)
		}
	}
}

func TestApplyMode(t *testing.T) {
	e, sink := newTestEngine(t)
	e.apply(Event{Type: EvNames, Network: "net", Buffer: "#go", Members: []Member{
		{Nick: "me"}, {Nick: "alice", Modes: "+"},
	}})

	// Gain op: "@" is inserted ahead of the existing voice "+".
	e.apply(Event{Type: EvMode, Network: "net", Buffer: "#go", Nick: "alice",
		Text: "+o me", MemberModes: []MemberMode{{Nick: "me", Symbol: "@", Add: true}}})
	if got := net0(e).Channel("#go").Members["me"].Modes; got != "@" {
		t.Errorf("me modes after +o = %q, want @", got)
	}
	e.apply(Event{Type: EvMode, Network: "net", Buffer: "#go", Nick: "x",
		Text: "+o alice", MemberModes: []MemberMode{{Nick: "alice", Symbol: "@", Add: true}}})
	if got := net0(e).Channel("#go").Members["alice"].Modes; got != "@+" {
		t.Errorf("alice modes after +o = %q, want @+ (op before voice)", got)
	}

	// Lose op: "@" is removed, voice survives.
	e.apply(Event{Type: EvMode, Network: "net", Buffer: "#go", Nick: "x",
		Text: "-o alice", MemberModes: []MemberMode{{Nick: "alice", Symbol: "@", Add: false}}})
	if got := net0(e).Channel("#go").Members["alice"].Modes; got != "+" {
		t.Errorf("alice modes after -o = %q, want +", got)
	}

	// Each mode change leaves a system line in the channel buffer.
	var sysLines int
	for _, m := range sink.msgs {
		if m.Kind == MsgSystem && m.Buffer == "#go" {
			sysLines++
		}
	}
	if sysLines != 3 {
		t.Errorf("mode system lines = %d, want 3", sysLines)
	}
}

func chanNames(n *Network) []string {
	out := make([]string, len(n.Channels))
	for i, c := range n.Channels {
		out[i] = c.Name
	}
	return out
}

func TestReorderBuffers(t *testing.T) {
	e, _ := newTestEngine(t)
	for _, ch := range []string{"#a", "#b", "#c"} {
		e.apply(Event{Type: EvJoin, Network: "net", Nick: "me", Buffer: ch})
	}

	if err := e.ReorderBuffers("net", []string{"#c", "#a", "#b"}); err != nil {
		t.Fatalf("ReorderBuffers: %v", err)
	}
	// BufferOrder is stored lowercased on the live params...
	if got := net0(e).Params.BufferOrder; !slices.Equal(got, []string{"#c", "#a", "#b"}) {
		t.Errorf("BufferOrder = %v, want [#c #a #b]", got)
	}
	// ...and the snapshot reflects it (the live slice is left untouched).
	if got := chanNames(e.SnapshotNetwork("net")); !slices.Equal(got, []string{"#c", "#a", "#b"}) {
		t.Errorf("snapshot order = %v, want [#c #a #b]", got)
	}

	// A buffer absent from the order (here #a, #c) sorts after the listed one,
	// keeping its live relative order. Case-insensitive match too.
	if err := e.ReorderBuffers("net", []string{"#B"}); err != nil {
		t.Fatalf("ReorderBuffers: %v", err)
	}
	if got := chanNames(e.SnapshotNetwork("net")); !slices.Equal(got, []string{"#b", "#a", "#c"}) {
		t.Errorf("snapshot order = %v, want [#b #a #c]", got)
	}

	if e.ReorderBuffers("nope", nil) == nil {
		t.Error("ReorderBuffers on unknown network: want error")
	}
}

func TestReorderNetworks(t *testing.T) {
	sink := &captureSink{}
	e := New(Options{Sink: sink})
	for _, id := range []string{"a", "b", "c"} {
		e.AddNetwork(NetworkParams{ID: id, Name: id, Nick: "me"}, nil)
	}

	e.ReorderNetworks([]string{"c", "a", "b"})

	var ids []string
	for _, n := range e.Snapshot().Networks {
		ids = append(ids, n.ID)
	}
	if !slices.Equal(ids, []string{"c", "a", "b"}) {
		t.Fatalf("network order = %v, want [c a b]", ids)
	}
	// Pos is rewritten to the new index so the order survives a restart.
	for i, id := range []string{"c", "a", "b"} {
		if p, _ := e.NetworkConfig(id); p.Pos != i {
			t.Errorf("network %q Pos = %d, want %d", id, p.Pos, i)
		}
	}
	// The full new order is fanned out to sinks.
	if n := len(sink.reordered); n == 0 {
		t.Fatal("no NetworksReordered emitted")
	} else if got := sink.reordered[n-1]; !slices.Equal(got, []string{"c", "a", "b"}) {
		t.Errorf("NetworksReordered = %v, want [c a b]", got)
	}

	// Networks omitted from the request keep their relative order at the end;
	// unknown ids are ignored.
	e.ReorderNetworks([]string{"b", "zzz"})
	ids = nil
	for _, n := range e.Snapshot().Networks {
		ids = append(ids, n.ID)
	}
	if !slices.Equal(ids, []string{"b", "c", "a"}) {
		t.Fatalf("network order = %v, want [b c a]", ids)
	}
}

// TestEventKindsDistinct asserts membership churn is emitted under its own
// MsgKind (not MsgSystem), so the client can recognize and fold it.
func TestEventKindsDistinct(t *testing.T) {
	e, sink := newTestEngine(t)
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "alice", Buffer: "#go"})
	e.apply(Event{Type: EvPart, Network: "net", Nick: "alice", Buffer: "#go"})
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "bob", Buffer: "#go"})
	e.apply(Event{Type: EvQuit, Network: "net", Nick: "bob", Text: "bye"})
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "carol", Buffer: "#go"})
	e.apply(Event{Type: EvNick, Network: "net", Nick: "carol", NewNick: "carol2"})

	want := []MsgKind{MsgJoin, MsgPart, MsgJoin, MsgQuit, MsgJoin, MsgNick}
	if len(sink.msgs) != len(want) {
		t.Fatalf("emitted %d lines, want %d: %+v", len(sink.msgs), len(want), sink.msgs)
	}
	for i, k := range want {
		if sink.msgs[i].Kind != k {
			t.Errorf("line %d kind = %q, want %q (%q)", i, sink.msgs[i].Kind, k, sink.msgs[i].Text)
		}
	}
}

func TestApplyReactRedact(t *testing.T) {
	e, sink := newTestEngine(t)
	e.apply(Event{Type: EvReact, Network: "net", Buffer: "#go", Target: "m1", Nick: "alice", Text: "👍"})
	e.apply(Event{Type: EvRedact, Network: "net", Buffer: "#go", Target: "m2", Nick: "bob", Text: "spam"})
	if len(sink.reacts) != 1 || sink.reacts[0] != [5]string{"net", "#go", "m1", "alice", "👍"} {
		t.Fatalf("reacts = %+v", sink.reacts)
	}
	if len(sink.redacts) != 1 || sink.redacts[0] != [5]string{"net", "#go", "m2", "bob", "spam"} {
		t.Fatalf("redacts = %+v", sink.redacts)
	}
}

func TestSendReactionRedactCapGated(t *testing.T) {
	// With the caps negotiated, the right raw lines go out.
	conn := &fakeConnector{caps: []string{"message-tags", "draft/message-redaction"}}
	e := New(Options{Sink: &captureSink{}, Connector: conn})
	if err := e.AddNetworkLive(NetworkParams{ID: "n", Name: "n", Addr: "a:1", Nick: "me"}); err != nil {
		t.Fatal(err)
	}
	e.SendReaction("n", "#go", "m1", "👍")
	e.SendRedact("n", "#go", "m1", "oops")
	raws := conn.conns[0].rawsSnap()
	if !slices.Contains(raws, "@+draft/react=👍;+draft/reply=m1 TAGMSG #go") {
		t.Errorf("react raw missing: %v", raws)
	}
	if !slices.Contains(raws, "REDACT #go m1 :oops") {
		t.Errorf("redact raw missing: %v", raws)
	}

	// Without the caps, both are silently dropped.
	bare := &fakeConnector{}
	e2 := New(Options{Sink: &captureSink{}, Connector: bare})
	_ = e2.AddNetworkLive(NetworkParams{ID: "n", Name: "n", Addr: "a:1", Nick: "me"})
	e2.SendReaction("n", "#go", "m1", "👍")
	e2.SendRedact("n", "#go", "m1", "oops")
	if got := bare.conns[0].rawsSnap(); len(got) != 0 {
		t.Errorf("expected no raws without caps, got %v", got)
	}
}

func TestNeedsReconnect(t *testing.T) {
	base := NetworkParams{ID: "n", Addr: "a:1", Nick: "me", Channels: []string{"#a"}}
	// Live-applyable changes must not force a reconnect.
	for _, p := range []NetworkParams{
		base, // identical
		{ID: "n", Addr: "a:1", Nick: "you", Channels: []string{"#a"}}, // nick
		{ID: "n", Addr: "a:1", Nick: "me", Channels: []string{"#b"}},  // channels
	} {
		if needsReconnect(base, p) {
			t.Errorf("needsReconnect(base, %+v) = true, want false", p)
		}
	}
	// Connection-level changes must force a reconnect.
	for name, p := range map[string]NetworkParams{
		"addr":          {ID: "n", Addr: "b:1", Nick: "me"},
		"server_pass":   {ID: "n", Addr: "a:1", Nick: "me", ServerPass: "x"},
		"sasl_external": {ID: "n", Addr: "a:1", Nick: "me", SASLExternal: true},
		"cert_pem":      {ID: "n", Addr: "a:1", Nick: "me", CertPEM: "PEM"},
	} {
		if !needsReconnect(base, p) {
			t.Errorf("needsReconnect for %s change = false, want true", name)
		}
	}
}

// TestRunPerformOnConnect verifies a network's perform lines replay through
// the input path on registration, reaching IRC as the expected raw commands.
func TestRunPerformOnConnect(t *testing.T) {
	conn := &fakeConnector{}
	e := New(Options{Sink: &captureSink{}, Connector: conn})
	if err := e.AddNetworkLive(NetworkParams{
		ID: "n", Name: "TestNet", Addr: "irc.example:6697", Nick: "preferred-nick",
		User: "alice", Realname: "Alice Example",
		Perform: []string{
			"/join #ops opskey",
			"/raw TEST $me $nick $network $server $user :$realname",
		},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = e.Run(ctx) }()

	// The server may select a different nick from the configured preference.
	// Perform variables must use that live nick.
	e.HandleEvent(Event{Type: EvConnect, Network: "n", Nick: "server-nick"})

	deadline := time.After(2 * time.Second)
	for {
		raws := conn.conns[0].rawsSnap()
		if slices.Contains(raws, "JOIN #ops opskey") &&
			slices.Contains(raws, "TEST server-nick server-nick TestNet irc.example:6697 alice :Alice Example") {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("perform commands not sent; raws=%v", conn.conns[0].rawsSnap())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestExpandPerformVariables(t *testing.T) {
	variables := map[string]string{
		"me": "alice_", "network": "Libera", "server": "irc.libera.chat:6697",
	}
	for _, tc := range []struct {
		line string
		want string
	}{
		{"/mode $me +B", "/mode alice_ +B"},
		{"/msg $me hello, $me!", "/msg alice_ hello, alice_!"},
		{"/raw TEST ${me}suffix", "/raw TEST alice_suffix"},
		{"/raw TEST $network $server", "/raw TEST Libera irc.libera.chat:6697"},
		{"/raw TEST $$me", "/raw TEST $me"},
		{"/raw TEST $missing", "/raw TEST $missing"},
		{"/raw TEST ${missing}", "/raw TEST ${missing}"},
		{"/raw TEST $member", "/raw TEST $member"},
		{"/raw TEST $", "/raw TEST $"},
	} {
		if got := expandPerformVariables(tc.line, variables); got != tc.want {
			t.Errorf("expandPerformVariables(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

// TestSetAliasesRuntime verifies an alias installed at runtime (the settings
// UI path) expands through the input path, and that Aliases() hands back a copy
// the caller can't use to mutate engine state.
func TestSetAliasesRuntime(t *testing.T) {
	conn := &fakeConnector{}
	e := New(Options{Sink: &captureSink{}, Connector: conn})
	if err := e.AddNetworkLive(NetworkParams{ID: "n", Name: "n", Addr: "a:1", Nick: "me"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = e.Run(ctx) }()

	e.SetAliases(map[string]string{"j": "/join $*"})

	// The returned table is a clone: mutating it must not change the engine's.
	got := e.Aliases()
	if got["j"] != "/join $*" {
		t.Fatalf("Aliases() = %v, want j -> /join $*", got)
	}
	got["j"] = "/part"
	if again := e.Aliases(); again["j"] != "/join $*" {
		t.Fatalf("Aliases() returned a live map; engine mutated to %v", again)
	}

	e.SendInput("n", "#ops", "/j #ops opskey")

	deadline := time.After(2 * time.Second)
	for {
		if slices.Contains(conn.conns[0].rawsSnap(), "JOIN #ops opskey") {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("alias did not expand to a JOIN; raws=%v", conn.conns[0].rawsSnap())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestMonitorFriends covers the friends-list lifecycle: add updates and dedups
// the persisted list, a 730/731 reply (EvMonitor) flips presence for listed
// nicks only, and remove clears both the list and the presence.
func TestMonitorFriends(t *testing.T) {
	conn := &fakeConnector{}
	e := New(Options{Sink: &captureSink{}, Connector: conn})
	if err := e.AddNetworkLive(NetworkParams{ID: "n", Name: "n", Addr: "a:1", Nick: "me"}); err != nil {
		t.Fatal(err)
	}

	if err := e.AddMonitor("n", "Bob"); err != nil {
		t.Fatal(err)
	}
	if got := e.SnapshotNetwork("n").Params.Monitor; !slices.Equal(got, []string{"bob"}) {
		t.Fatalf("Monitor = %v, want [bob]", got)
	}
	_ = e.AddMonitor("n", "bob") // duplicate (case-insensitive) is a no-op
	if got := e.SnapshotNetwork("n").Params.Monitor; len(got) != 1 {
		t.Fatalf("duplicate add grew the list: %v", got)
	}

	// A 730 reply marks a listed friend online; a non-friend is ignored.
	e.apply(Event{Type: EvMonitor, Network: "n", Online: true, Args: []string{"bob"}})
	e.apply(Event{Type: EvMonitor, Network: "n", Online: true, Args: []string{"eve"}})
	snap := e.SnapshotNetwork("n")
	if !snap.MonitorOnline["bob"] {
		t.Fatal("bob not online after 730")
	}
	if _, ok := snap.MonitorOnline["eve"]; ok {
		t.Fatal("non-friend eve was tracked")
	}

	// Remove clears both the list and the presence.
	if err := e.RemoveMonitor("n", "BOB"); err != nil {
		t.Fatal(err)
	}
	snap = e.SnapshotNetwork("n")
	if len(snap.Params.Monitor) != 0 {
		t.Fatalf("Monitor not cleared: %v", snap.Params.Monitor)
	}
	if _, ok := snap.MonitorOnline["bob"]; ok {
		t.Fatal("presence not cleared on remove")
	}
}

func TestApplyNickRename(t *testing.T) {
	e, _ := newTestEngine(t)
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "alice", Buffer: "#go"})

	e.apply(Event{Type: EvNick, Network: "net", Nick: "alice", NewNick: "alice2"})
	c := net0(e).Channel("#go")
	if _, ok := c.Members["alice"]; ok {
		t.Error("old nick alice still present")
	}
	mem, ok := c.Members["alice2"]
	if !ok || mem.Nick != "alice2" {
		t.Fatalf("alice2 not present/renamed: %+v", c.Members)
	}

	// Our own nick tracks rename.
	e.apply(Event{Type: EvNick, Network: "net", Nick: "me", NewNick: "newme"})
	if net0(e).Nick != "newme" {
		t.Errorf("our nick = %q, want newme", net0(e).Nick)
	}
}

func TestApplyTopic(t *testing.T) {
	e, _ := newTestEngine(t)
	e.apply(Event{Type: EvTopic, Network: "net", Buffer: "#go", Text: "hello world", Nick: "op"})
	if got := net0(e).Channel("#go").Topic; got != "hello world" {
		t.Errorf("topic = %q", got)
	}
}

func TestApplyMessageRoutesBuffer(t *testing.T) {
	e, sink := newTestEngine(t)

	// Channel message → channel buffer.
	e.apply(Event{Type: EvMessageIn, Network: "net", Message: &Message{
		Network: "net", Buffer: "#go", From: "alice", Kind: MsgPrivmsg, Text: "hi",
	}})
	if c := net0(e).Channel("#go"); c == nil || c.Kind != KindChannel {
		t.Fatalf("channel buffer not created as channel: %+v", c)
	}

	// Query message → query buffer.
	e.apply(Event{Type: EvMessageIn, Network: "net", Message: &Message{
		Network: "net", Buffer: "alice", From: "alice", Kind: MsgPrivmsg, Text: "psst",
	}})
	if c := net0(e).Channel("alice"); c == nil || c.Kind != KindQuery {
		t.Fatalf("query buffer not created as query: %+v", c)
	}

	if len(sink.msgs) != 2 {
		t.Fatalf("printed %d messages, want 2", len(sink.msgs))
	}
}

// A message that arrives without an IRCv3 msgid tag must still be printed with
// a stable, unique id. The same id reaches every sink (store + live), so the
// client can dedup the backlog copy against the live tail and jump to the
// line. A real msgid is left untouched. Regression for messages doubling when
// a buffer was opened after accumulating live lines.
func TestApplyMessageSynthesizesID(t *testing.T) {
	e, sink := newTestEngine(t)

	e.apply(Event{Type: EvMessageIn, Network: "net", Message: &Message{
		Network: "net", Buffer: "#go", From: "alice", Kind: MsgPrivmsg, Text: "one",
	}})
	e.apply(Event{Type: EvMessageIn, Network: "net", Message: &Message{
		Network: "net", Buffer: "#go", From: "bob", Kind: MsgPrivmsg, Text: "two",
	}})
	e.apply(Event{Type: EvMessageIn, Network: "net", Message: &Message{
		ID: "server-given", Network: "net", Buffer: "#go", From: "carol", Kind: MsgPrivmsg, Text: "three",
	}})

	if len(sink.msgs) != 3 {
		t.Fatalf("printed %d messages, want 3", len(sink.msgs))
	}
	if sink.msgs[0].ID == "" || sink.msgs[1].ID == "" {
		t.Fatalf("msgid-less messages got empty ids: %q, %q", sink.msgs[0].ID, sink.msgs[1].ID)
	}
	if sink.msgs[0].ID == sink.msgs[1].ID {
		t.Fatalf("synthesized ids collide: %q", sink.msgs[0].ID)
	}
	if sink.msgs[2].ID != "server-given" {
		t.Fatalf("real msgid was overwritten: %q", sink.msgs[2].ID)
	}
}

func TestCloseBuffer(t *testing.T) {
	e, sink := newTestEngine(t)

	// A query (from an inbound DM) and a channel.
	e.apply(Event{Type: EvMessageIn, Network: "net", Message: &Message{
		Network: "net", Buffer: "alice", From: "alice", Kind: MsgPrivmsg, Text: "psst",
	}})
	e.apply(Event{Type: EvMessageIn, Network: "net", Message: &Message{
		Network: "net", Buffer: "#go", From: "bob", Kind: MsgPrivmsg, Text: "hi",
	}})

	// Closing a query drops the buffer and re-broadcasts the network.
	before := len(sink.nets)
	if err := e.CloseBuffer("net", "alice"); err != nil {
		t.Fatalf("CloseBuffer(query) = %v, want nil", err)
	}
	if c := net0(e).Channel("alice"); c != nil {
		t.Fatalf("query buffer still present after close: %+v", c)
	}
	if len(sink.nets) <= before {
		t.Fatalf("CloseBuffer did not re-broadcast the network (nets %d → %d)", before, len(sink.nets))
	}

	// Channels can't be closed (use /part); the status buffer can't either.
	if err := e.CloseBuffer("net", "#go"); err == nil {
		t.Fatal("CloseBuffer(channel) = nil, want error")
	}
	if c := net0(e).Channel("#go"); c == nil {
		t.Fatal("channel buffer was removed by a rejected close")
	}

	// Unknown network / buffer are errors, not panics.
	if err := e.CloseBuffer("nope", "alice"); err == nil {
		t.Fatal("CloseBuffer(unknown network) = nil, want error")
	}
	if err := e.CloseBuffer("net", "ghost"); err == nil {
		t.Fatal("CloseBuffer(unknown buffer) = nil, want error")
	}
}

func TestNetworkChangedEmitted(t *testing.T) {
	e, sink := newTestEngine(t)

	// A join is a structural change: a network snapshot is emitted carrying
	// the new channel and member.
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "alice", Buffer: "#go"})
	if len(sink.nets) != 1 {
		t.Fatalf("got %d network snapshots, want 1", len(sink.nets))
	}
	snap := sink.nets[0]
	ch := snap.Channel("#go")
	if ch == nil || ch.Members["alice"] == nil {
		t.Fatalf("snapshot missing #go/alice: %+v", snap)
	}

	// The snapshot is a copy: later mutation must not leak into it.
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "bob", Buffer: "#go"})
	if _, leaked := snap.Channel("#go").Members["bob"]; leaked {
		t.Error("earlier snapshot was mutated by a later event")
	}

	// A message to an existing buffer is not structural: no new snapshot.
	before := len(sink.nets)
	e.apply(Event{Type: EvMessageIn, Network: "net", Message: &Message{
		Network: "net", Buffer: "#go", From: "alice", Kind: MsgPrivmsg, Text: "hi",
	}})
	if len(sink.nets) != before {
		t.Errorf("message to existing buffer emitted a snapshot")
	}

	// A message to a new buffer is structural.
	e.apply(Event{Type: EvMessageIn, Network: "net", Message: &Message{
		Network: "net", Buffer: "bob", From: "bob", Kind: MsgPrivmsg, Text: "dm",
	}})
	if len(sink.nets) != before+1 {
		t.Errorf("message creating a buffer did not emit a snapshot")
	}
}

func TestPartSelfRemovesBuffer(t *testing.T) {
	e, _ := newTestEngine(t)
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "me", Buffer: "#go"})
	if net0(e).Channel("#go") == nil {
		t.Fatal("we did not join #go")
	}
	e.apply(Event{Type: EvPart, Network: "net", Nick: "me", Buffer: "#go"})
	if net0(e).Channel("#go") != nil {
		t.Error("our own part did not remove the buffer")
	}
}

func TestJoinPartPersistsAutojoin(t *testing.T) {
	netStore := &recordingNetStore{}
	e := New(Options{Sink: &captureSink{}, Networks: netStore})
	e.AddNetwork(NetworkParams{ID: "net", Name: "net", Nick: "me"}, nil)
	baseSaves := len(netStore.saved) // AddNetwork persists once

	// We join a channel ourselves → it lands in the persisted auto-join list.
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "me", Buffer: "#go"})
	if got := net0(e).Params.Channels; len(got) != 1 || got[0] != "#go" {
		t.Fatalf("autojoin = %v, want [#go]", got)
	}
	if len(netStore.saved) != baseSaves+1 {
		t.Fatalf("self-join saves = %d, want %d", len(netStore.saved), baseSaves+1)
	}
	if last := netStore.saved[len(netStore.saved)-1]; len(last.Channels) != 1 || last.Channels[0] != "#go" {
		t.Errorf("persisted channels = %v, want [#go]", last.Channels)
	}

	// Re-joining the same channel (e.g. the server echo of the initial
	// auto-join) must not duplicate it or write again.
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "me", Buffer: "#GO"})
	if got := net0(e).Params.Channels; len(got) != 1 {
		t.Errorf("autojoin after re-join = %v, want one entry", got)
	}
	if len(netStore.saved) != baseSaves+1 {
		t.Errorf("re-join wrote to store; saves = %d, want %d", len(netStore.saved), baseSaves+1)
	}

	// Someone else joining must not touch the auto-join list or persist.
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "bob", Buffer: "#go"})
	if len(netStore.saved) != baseSaves+1 {
		t.Errorf("other join wrote to store; saves = %d", len(netStore.saved))
	}

	// We part → the channel is dropped from the persisted list.
	e.apply(Event{Type: EvPart, Network: "net", Nick: "me", Buffer: "#go"})
	if got := net0(e).Params.Channels; len(got) != 0 {
		t.Errorf("autojoin after part = %v, want empty", got)
	}
	if len(netStore.saved) != baseSaves+2 {
		t.Fatalf("self-part saves = %d, want %d", len(netStore.saved), baseSaves+2)
	}
}

func TestJoinKeyPersists(t *testing.T) {
	netStore := &recordingNetStore{}
	conn := &fakeRuntimeConn{}
	e := New(Options{Sink: &captureSink{}, Networks: netStore})
	e.AddNetwork(NetworkParams{ID: "net", Name: "net", Nick: "me"}, conn)

	// /join #secret hunter2 records the key; the self-JOIN commits it.
	e.runBuiltinCommand(Event{Type: EvCommand, Network: "net", Command: "join", Text: "#secret hunter2"})
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "me", Buffer: "#secret"})

	if got := net0(e).Params.ChannelKeys["#secret"]; got != "hunter2" {
		t.Fatalf("stored key = %q, want hunter2", got)
	}
	last := netStore.saved[len(netStore.saved)-1]
	if last.ChannelKeys["#secret"] != "hunter2" {
		t.Errorf("persisted key = %q, want hunter2", last.ChannelKeys["#secret"])
	}

	// A plain (keyless) re-join must not wipe the stored key.
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "me", Buffer: "#secret"})
	if got := net0(e).Params.ChannelKeys["#secret"]; got != "hunter2" {
		t.Errorf("keyless re-join wiped key: %q", got)
	}

	// Parting drops both the channel and its key.
	e.apply(Event{Type: EvPart, Network: "net", Nick: "me", Buffer: "#secret"})
	if _, ok := net0(e).Params.ChannelKeys["#secret"]; ok {
		t.Error("part did not drop the join key")
	}
}

func TestAddRemoveNetworkLive(t *testing.T) {
	sink := &captureSink{}
	conn := &fakeConnector{}
	netStore := &recordingNetStore{}
	e := New(Options{Sink: sink, Connector: conn, Networks: netStore})

	// Add a network at runtime.
	if err := e.AddNetworkLive(NetworkParams{ID: "libera", Name: "libera", Addr: "irc.libera.chat:6697", Nick: "me"}); err != nil {
		t.Fatalf("AddNetworkLive: %v", err)
	}
	if conn.dialed != 1 {
		t.Errorf("connector dialed %d times, want 1", conn.dialed)
	}
	if e.user.Network("libera") == nil {
		t.Fatal("network not registered")
	}
	if len(netStore.saved) != 1 || netStore.saved[0].ID != "libera" {
		t.Errorf("network not persisted: %+v", netStore.saved)
	}
	if len(sink.nets) == 0 {
		t.Error("no net:update emitted on add")
	}

	// Duplicate is rejected.
	if err := e.AddNetworkLive(NetworkParams{ID: "libera", Addr: "x:1"}); err == nil {
		t.Error("duplicate network accepted")
	}

	// Remove it.
	if err := e.RemoveNetwork("libera"); err != nil {
		t.Fatalf("RemoveNetwork: %v", err)
	}
	if e.user.Network("libera") != nil {
		t.Error("network still present after remove")
	}
	if len(netStore.deleted) != 1 || netStore.deleted[0] != "libera" {
		t.Errorf("removal not persisted: %+v", netStore.deleted)
	}
	if len(sink.removed) != 1 || sink.removed[0] != "libera" {
		t.Errorf("no net:remove emitted: %+v", sink.removed)
	}
}

func TestUpdateNetwork(t *testing.T) {
	sink := &captureSink{}
	conn := &fakeConnector{}
	netStore := &recordingNetStore{}
	e := New(Options{Sink: sink, Connector: conn, Networks: netStore})

	if err := e.AddNetworkLive(NetworkParams{ID: "libera", Name: "libera", Addr: "old:6697", Nick: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := e.UpdateNetwork(NetworkParams{ID: "libera", Name: "libera", Addr: "new:6697", Nick: "newnick", SASLUser: "acct"}); err != nil {
		t.Fatalf("UpdateNetwork: %v", err)
	}
	if conn.dialed != 2 {
		t.Errorf("dialed %d times, want 2 (add + reconnect)", conn.dialed)
	}
	got, ok := e.NetworkConfig("libera")
	if !ok || got.Addr != "new:6697" || got.Nick != "newnick" || got.SASLUser != "acct" {
		t.Fatalf("config not updated: %+v", got)
	}
	// Persisted with the new values.
	last := netStore.saved[len(netStore.saved)-1]
	if last.Addr != "new:6697" || last.SASLUser != "acct" {
		t.Errorf("update not persisted: %+v", last)
	}
}

func TestUpdateNetworkPreservesJoinKeys(t *testing.T) {
	conn := &fakeConnector{}
	netStore := &recordingNetStore{}
	e := New(Options{Sink: &captureSink{}, Connector: conn, Networks: netStore})
	if err := e.AddNetworkLive(NetworkParams{
		ID: "libera", Name: "libera", Addr: "a:6697", Nick: "me",
		Channels: []string{"#a", "#b"}, ChannelKeys: map[string]string{"#a": "k1", "#b": "k2"},
	}); err != nil {
		t.Fatal(err)
	}
	e.apply(Event{Type: EvConnect, Network: "libera", Nick: "me"})

	// A GUI edit (no ChannelKeys on the wire) that drops #b must keep #a's key
	// and forget #b's.
	if err := e.UpdateNetwork(NetworkParams{
		ID: "libera", Name: "libera", Addr: "a:6697", Nick: "me", Channels: []string{"#a"},
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := e.NetworkConfig("libera")
	if got.ChannelKeys["#a"] != "k1" {
		t.Errorf("edit lost #a key: %v", got.ChannelKeys)
	}
	if _, ok := got.ChannelKeys["#b"]; ok {
		t.Errorf("edit kept key for dropped channel #b: %v", got.ChannelKeys)
	}
	last := netStore.saved[len(netStore.saved)-1]
	if last.ChannelKeys["#a"] != "k1" || len(last.ChannelKeys) != 1 {
		t.Errorf("persisted keys after edit = %v, want {#a:k1}", last.ChannelKeys)
	}
}

func TestUpdateNetworkChannelsNoReconnect(t *testing.T) {
	conn := &fakeConnector{}
	e := New(Options{Sink: &captureSink{}, Connector: conn, Networks: &recordingNetStore{}})
	if err := e.AddNetworkLive(NetworkParams{ID: "libera", Name: "libera", Addr: "a:6697", Nick: "me", Channels: []string{"#a"}}); err != nil {
		t.Fatal(err)
	}
	// Mark registered so live JOIN/PART are sent over the connection.
	e.apply(Event{Type: EvConnect, Network: "libera", Nick: "me"})

	// Change only the channel list: must NOT re-dial.
	if err := e.UpdateNetwork(NetworkParams{ID: "libera", Name: "libera", Addr: "a:6697", Nick: "me", Channels: []string{"#a", "#b"}}); err != nil {
		t.Fatal(err)
	}
	if conn.dialed != 1 {
		t.Errorf("channel-only change re-dialed (%d); should stay connected", conn.dialed)
	}
	raws := conn.conns[0].raws
	if !slices.Contains(raws, "JOIN #b") {
		t.Errorf("expected JOIN #b, got %v", raws)
	}

	// Removing a channel parts it, still no reconnect.
	if err := e.UpdateNetwork(NetworkParams{ID: "libera", Name: "libera", Addr: "a:6697", Nick: "me", Channels: []string{"#b"}}); err != nil {
		t.Fatal(err)
	}
	if conn.dialed != 1 {
		t.Errorf("channel removal re-dialed (%d)", conn.dialed)
	}
	if !slices.Contains(conn.conns[0].raws, "PART #a") {
		t.Errorf("expected PART #a, got %v", conn.conns[0].raws)
	}
}

func TestChannelListAccumulation(t *testing.T) {
	e, sink := newTestEngine(t)
	e.apply(Event{Type: EvListItem, Network: "net", Buffer: "#a", Count: 10, Text: "topic a"})
	e.apply(Event{Type: EvListItem, Network: "net", Buffer: "#b", Count: 5, Text: "topic b"})
	if len(sink.lists) != 0 {
		t.Fatal("list emitted before end")
	}
	e.apply(Event{Type: EvListEnd, Network: "net"})
	if len(sink.lists) != 1 || len(sink.lists[0]) != 2 {
		t.Fatalf("list result = %+v", sink.lists)
	}
	if sink.lists[0][0].Name != "#a" || sink.lists[0][0].Users != 10 || sink.lists[0][1].Name != "#b" {
		t.Errorf("list items = %+v", sink.lists[0])
	}
}

func TestChatHistoryCommand(t *testing.T) {
	// With the cap: sends a CHATHISTORY request.
	conn := &fakeConnector{caps: []string{"draft/chathistory"}}
	e := New(Options{Sink: &captureSink{}, Connector: conn})
	if err := e.AddNetworkLive(NetworkParams{ID: "n", Name: "n", Addr: "a:1", Nick: "me"}); err != nil {
		t.Fatal(err)
	}
	e.runBuiltinCommand(Event{Type: EvCommand, Network: "n", Buffer: "#c", Command: "chathistory", Args: []string{"10"}})
	if !slices.Contains(conn.conns[0].raws, "CHATHISTORY LATEST #c * 10") {
		t.Errorf("raws = %v", conn.conns[0].raws)
	}

	// Without the cap: a system notice, no raw.
	conn2 := &fakeConnector{}
	sink := &captureSink{}
	e2 := New(Options{Sink: sink, Connector: conn2})
	_ = e2.AddNetworkLive(NetworkParams{ID: "n", Name: "n", Addr: "a:1", Nick: "me"})
	e2.runBuiltinCommand(Event{Type: EvCommand, Network: "n", Buffer: "#c", Command: "chathistory"})
	if len(conn2.conns[0].raws) != 0 {
		t.Errorf("sent a raw without the cap: %v", conn2.conns[0].raws)
	}
	found := false
	for _, m := range sink.msgs {
		if m.Kind == MsgSystem && strings.Contains(m.Text, "does not support chathistory") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an unsupported notice; msgs=%+v", sink.msgs)
	}
}

func TestWhoisCommandAndReplyRouting(t *testing.T) {
	// Issuing /whois from buffer #c routes the WHOIS reply lines (and the
	// 318 end marker) back to #c, not the status buffer.
	conn := &fakeConnector{}
	sink := &captureSink{}
	e := New(Options{Sink: sink, Connector: conn})
	if err := e.AddNetworkLive(NetworkParams{ID: "n", Name: "n", Addr: "a:1", Nick: "me"}); err != nil {
		t.Fatal(err)
	}

	e.runBuiltinCommand(Event{
		Type: EvCommand, Network: "n", Buffer: "#c",
		Command: "whois", Args: []string{"alice"}, Text: "alice",
	})
	if !slices.Contains(conn.conns[0].raws, "WHOIS alice") {
		t.Fatalf("/whois did not send WHOIS line; raws=%v", conn.conns[0].raws)
	}
	if got := e.pendingWhois["n\talice"]; got != "#c" {
		t.Fatalf("pendingWhois not set: %v", e.pendingWhois)
	}

	// Feed a couple of numeric replies through apply(); each should land
	// in #c as a system message.
	for _, ev := range []Event{
		{Type: EvNumeric, Network: "n", Time: time.Now(), Nick: "alice", Text: "alice (a@h): A", Count: 311},
		{Type: EvNumeric, Network: "n", Time: time.Now(), Nick: "alice", Text: "End of WHOIS for alice", Count: 318},
	} {
		e.apply(ev)
	}

	var inBuf []Message
	for _, m := range sink.msgs {
		if m.Buffer == "#c" && m.Kind == MsgSystem {
			inBuf = append(inBuf, m)
		}
	}
	if len(inBuf) != 2 {
		t.Errorf("expected 2 system lines in #c, got %d: %+v", len(inBuf), sink.msgs)
	}
	if _, ok := e.pendingWhois["n\talice"]; ok {
		t.Errorf("318 should have cleared pendingWhois: %v", e.pendingWhois)
	}
}

func TestWhoisReplyFallsBackToStatus(t *testing.T) {
	// A numeric arriving without a pending entry lands in the status buffer.
	conn := &fakeConnector{}
	sink := &captureSink{}
	e := New(Options{Sink: sink, Connector: conn})
	_ = e.AddNetworkLive(NetworkParams{ID: "n", Name: "n", Addr: "a:1", Nick: "me"})
	e.apply(Event{
		Type: EvNumeric, Network: "n", Time: time.Now(), Nick: "ghost",
		Text: "no such nick: ghost", Count: 401,
	})
	found := false
	for _, m := range sink.msgs {
		if m.Buffer == StatusBuffer && m.Text == "no such nick: ghost" {
			found = true
		}
	}
	if !found {
		t.Errorf("401 didn't land in status buffer; msgs=%+v", sink.msgs)
	}
}

func TestBackgroundWhoAndNamesRepliesAreSilent(t *testing.T) {
	// girc sends WHO/WHOX automatically after a self-JOIN, and every JOIN's
	// NAMES burst ends in 366. With no user-issued lookup pending, none of
	// that protocol bookkeeping should leak into the status buffer.
	sink := &captureSink{}
	e := New(Options{Sink: sink})
	e.AddNetwork(NetworkParams{ID: "n", Name: "n", Nick: "me"}, nil)

	for _, ev := range []Event{
		{Type: EvNumeric, Network: "n", Nick: "#c", Text: "alice [H]", Count: 352},
		{Type: EvNumeric, Network: "n", Nick: "1", Text: "1 #c alice", Count: 354},
		{Type: EvNumeric, Network: "n", Nick: "#c", Text: "End of WHO for #c", Count: 315},
		{Type: EvNumeric, Network: "n", Nick: "#c", Text: "End of NAMES for #c", Count: 366},
	} {
		e.apply(ev)
	}

	if len(sink.msgs) != 0 {
		t.Fatalf("background WHO/NAMES replies leaked into output: %+v", sink.msgs)
	}
}

func TestExplicitWhoRepliesRemainVisible(t *testing.T) {
	conn := &fakeConnector{}
	sink := &captureSink{}
	e := New(Options{Sink: sink, Connector: conn})
	if err := e.AddNetworkLive(NetworkParams{ID: "n", Name: "n", Addr: "a:1", Nick: "me"}); err != nil {
		t.Fatal(err)
	}

	e.runBuiltinCommand(Event{
		Type: EvCommand, Network: "n", Buffer: "#request-buffer",
		Command: "who", Args: []string{"#c"}, Text: "#c",
	})
	if !slices.Contains(conn.conns[0].raws, "WHO #c") {
		t.Fatalf("/who did not send WHO line; raws=%v", conn.conns[0].raws)
	}

	for _, ev := range []Event{
		{Type: EvNumeric, Network: "n", Time: time.Now(), Nick: "#c", Text: "alice [H]", Count: 352},
		{Type: EvNumeric, Network: "n", Time: time.Now(), Nick: "#c", Text: "End of WHO for #c", Count: 315},
	} {
		e.apply(ev)
	}

	var visible []Message
	for _, m := range sink.msgs {
		if m.Buffer == "#request-buffer" {
			visible = append(visible, m)
		}
	}
	if len(visible) != 2 {
		t.Fatalf("explicit WHO replies = %+v, want two lines in requesting buffer", sink.msgs)
	}
	if _, ok := e.pendingWhois["n\t#c"]; ok {
		t.Fatalf("315 should clear pending WHO: %v", e.pendingWhois)
	}
}

func TestModeShorthandCommands(t *testing.T) {
	conn := &fakeConnector{}
	e := New(Options{Sink: &captureSink{}, Connector: conn})
	_ = e.AddNetworkLive(NetworkParams{ID: "n", Name: "n", Addr: "a:1", Nick: "me"})

	// /op alice — one op on the current channel.
	e.runBuiltinCommand(Event{
		Type: EvCommand, Network: "n", Buffer: "#c",
		Command: "op", Args: []string{"alice"}, Text: "alice",
	})
	// /deop alice bob carol — three deops, all in one MODE line.
	e.runBuiltinCommand(Event{
		Type: EvCommand, Network: "n", Buffer: "#c",
		Command: "deop", Args: []string{"alice", "bob", "carol"}, Text: "alice bob carol",
	})
	// /voice alice
	e.runBuiltinCommand(Event{
		Type: EvCommand, Network: "n", Buffer: "#c",
		Command: "voice", Args: []string{"alice"}, Text: "alice",
	})

	want := []string{
		"MODE #c +o alice",
		"MODE #c -ooo alice bob carol",
		"MODE #c +v alice",
	}
	for _, w := range want {
		if !slices.Contains(conn.conns[0].raws, w) {
			t.Errorf("missing %q from raws=%v", w, conn.conns[0].raws)
		}
	}
}

func TestKickAndBanCommands(t *testing.T) {
	conn := &fakeConnector{}
	e := New(Options{Sink: &captureSink{}, Connector: conn})
	_ = e.AddNetworkLive(NetworkParams{ID: "n", Name: "n", Addr: "a:1", Nick: "me"})

	// /kick alice (current channel).
	e.runBuiltinCommand(Event{
		Type: EvCommand, Network: "n", Buffer: "#c",
		Command: "kick", Args: []string{"alice"}, Text: "alice",
	})
	// /kick alice spam (with reason).
	e.runBuiltinCommand(Event{
		Type: EvCommand, Network: "n", Buffer: "#c",
		Command: "kick", Args: []string{"alice", "spam"}, Text: "alice spam",
	})
	// /kick #other alice (explicit channel).
	e.runBuiltinCommand(Event{
		Type: EvCommand, Network: "n", Buffer: "#c",
		Command: "kick", Args: []string{"#other", "alice"}, Text: "#other alice",
	})
	// /ban alice!*@*
	e.runBuiltinCommand(Event{
		Type: EvCommand, Network: "n", Buffer: "#c",
		Command: "ban", Args: []string{"alice!*@*"}, Text: "alice!*@*",
	})

	for _, want := range []string{
		"KICK #c alice",
		"KICK #c alice :spam",
		"KICK #other alice",
		"MODE #c +b alice!*@*",
	} {
		if !slices.Contains(conn.conns[0].raws, want) {
			t.Errorf("missing %q from raws=%v", want, conn.conns[0].raws)
		}
	}
}

func TestUnknownCommandPassesThroughAsRaw(t *testing.T) {
	// The default branch in runBuiltinCommand now upper-cases the command
	// and sends it raw — the documented weechat/irssi behavior. That makes
	// server-specific commands (/sajoin, /stats, /knock, …) work without
	// us enumerating them.
	conn := &fakeConnector{}
	e := New(Options{Sink: &captureSink{}, Connector: conn})
	_ = e.AddNetworkLive(NetworkParams{ID: "n", Name: "n", Addr: "a:1", Nick: "me"})
	e.runBuiltinCommand(Event{
		Type: EvCommand, Network: "n", Buffer: "#c",
		Command: "knock", Args: []string{"#secret"}, Text: "#secret",
	})
	if !slices.Contains(conn.conns[0].raws, "KNOCK #secret") {
		t.Errorf("unknown command not passed through raw; raws=%v", conn.conns[0].raws)
	}
}

func TestSetConnected(t *testing.T) {
	conn := &fakeConnector{}
	e := New(Options{Sink: &captureSink{}, Connector: conn})
	if err := e.AddNetworkLive(NetworkParams{ID: "n", Name: "n", Addr: "a:1", Nick: "me"}); err != nil {
		t.Fatal(err)
	}
	// Disconnect keeps the network but marks it down.
	if err := e.SetConnected("n", false); err != nil {
		t.Fatal(err)
	}
	if e.user.Network("n") == nil {
		t.Fatal("network was removed by disconnect")
	}
	if got, _ := e.NetworkConfig("n"); got.ID != "n" {
		t.Fatal("config lost after disconnect")
	}
	// Reconnect dials a fresh connection.
	if err := e.SetConnected("n", true); err != nil {
		t.Fatal(err)
	}
	if conn.dialed != 2 {
		t.Errorf("dialed %d, want 2 (add + reconnect)", conn.dialed)
	}
}

func TestAddNetworkLiveNoConnector(t *testing.T) {
	e := New(Options{Sink: &captureSink{}}) // no connector
	if err := e.AddNetworkLive(NetworkParams{ID: "n", Addr: "x:1"}); err == nil {
		t.Error("expected error without a connector")
	}
}

func TestUnknownNetworkIgnored(t *testing.T) {
	e, sink := newTestEngine(t)
	e.apply(Event{Type: EvJoin, Network: "ghost", Nick: "x", Buffer: "#y"})
	if len(sink.msgs) != 0 {
		t.Errorf("event for unknown network produced output: %+v", sink.msgs)
	}
}

// dropHost drops any message whose text contains "spoiler" — exercising the
// plugin-hook drop path through the engine loop.
type dropHost struct{}

func (dropHost) Dispatch(_ context.Context, ev Event) (Event, bool) {
	if ev.Message != nil && strings.Contains(ev.Message.Text, "spoiler") {
		return ev, false
	}
	return ev, true
}
func (dropHost) Commands() []string                            { return nil }
func (dropHost) Complete(_, _, _ string) []string              { return nil }
func (dropHost) Plugins() []PluginInfo                         { return nil }
func (dropHost) LoadPlugin(string) error                       { return nil }
func (dropHost) UnloadPlugin(string) error                     { return nil }
func (dropHost) ReloadPlugin(string) error                     { return nil }
func (dropHost) SetPluginSetting(string, string, string) error { return nil }
func (dropHost) Close() error                                  { return nil }

func TestHostCanDropMessage(t *testing.T) {
	sink := &captureSink{}
	e := New(Options{Sink: sink, Host: dropHost{}})
	e.AddNetwork(NetworkParams{ID: "net", Name: "net", Nick: "me"}, nil)

	ctx := context.Background()
	e.handle(ctx, Event{Type: EvMessageIn, Network: "net", Message: &Message{
		Network: "net", Buffer: "#go", From: "a", Kind: MsgPrivmsg, Text: "big spoiler here",
	}})
	e.handle(ctx, Event{Type: EvMessageIn, Network: "net", Message: &Message{
		Network: "net", Buffer: "#go", From: "a", Kind: MsgPrivmsg, Text: "harmless",
	}})

	if len(sink.msgs) != 1 || sink.msgs[0].Text != "harmless" {
		t.Fatalf("expected only the harmless line, got %+v", sink.msgs)
	}
}

func TestHandleEventAfterShutdown(t *testing.T) {
	e, _ := newTestEngine(t)
	close(e.done)
	// Must not block or panic once done is closed.
	doneCh := make(chan struct{})
	go func() {
		e.HandleEvent(Event{Type: EvJoin, Network: "net", Nick: "x", Buffer: "#y"})
		close(doneCh)
	}()
	select {
	case <-doneCh:
	case <-time.After(time.Second):
		t.Fatal("HandleEvent blocked after shutdown")
	}
}

// TestParamsIsolation guards the clone discipline around NetworkParams: values
// crossing the Engine boundary (in via AddNetwork*/UpdateNetwork, out via
// NetworkConfig) must not share slice/map backing with the live tree, or a
// server goroutine reads them racily while the loop goroutine mutates them.
func TestParamsIsolation(t *testing.T) {
	conn := &fakeConnector{}
	e := New(Options{Sink: &captureSink{}, Connector: conn, Networks: &recordingNetStore{}})
	in := NetworkParams{
		ID: "net", Name: "net", Addr: "a:6697", Nick: "me",
		Channels: []string{"#a", "#b"}, ChannelKeys: map[string]string{"#a": "k"},
	}
	if err := e.AddNetworkLive(in); err != nil {
		t.Fatal(err)
	}
	// Mutating the caller's params after the call must not reach the engine.
	in.Channels[0] = "#hacked"
	in.ChannelKeys["#a"] = "hacked"
	got, _ := e.NetworkConfig("net")
	if got.Channels[0] != "#a" || got.ChannelKeys["#a"] != "k" {
		t.Fatalf("AddNetworkLive aliased caller params into the tree: %+v", got)
	}
	// Mutating a returned config must not reach the engine either.
	got.Channels[0] = "#hacked"
	got.ChannelKeys["#a"] = "hacked"
	again, _ := e.NetworkConfig("net")
	if again.Channels[0] != "#a" || again.ChannelKeys["#a"] != "k" {
		t.Fatalf("NetworkConfig returned a live alias: %+v", again)
	}
	// Same for the live-update path of UpdateNetwork (no reconnect).
	upd := NetworkParams{
		ID: "net", Name: "net", Addr: "a:6697", Nick: "me",
		Channels: []string{"#a", "#c"},
	}
	if err := e.UpdateNetwork(upd); err != nil {
		t.Fatal(err)
	}
	upd.Channels[1] = "#hacked"
	final, _ := e.NetworkConfig("net")
	if final.Channels[1] != "#c" {
		t.Fatalf("UpdateNetwork aliased caller params into the tree: %+v", final)
	}
}

func TestKick(t *testing.T) {
	e, sink := newTestEngine(t)
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "me", Buffer: "#go"})
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "alice", Buffer: "#go"})

	// Someone else kicked: just drop the member, with a system line.
	e.apply(Event{Type: EvKick, Network: "net", Nick: "alice", Kicker: "op", Buffer: "#go", Text: "spam"})
	c := net0(e).Channel("#go")
	if _, ok := c.Members["alice"]; ok {
		t.Error("alice still a member after kick")
	}
	last := sink.msgs[len(sink.msgs)-1]
	if last.Kind != MsgPart || last.Text != "alice was kicked from #go by op (spam)" {
		t.Errorf("kick line = %q (%s)", last.Text, last.Kind)
	}

	// Self-kick: buffer and autojoin survive (unlike a self-part), members
	// are cleared, and the reason lands in the buffer.
	net0(e).Params.Channels = []string{"#go"}
	e.apply(Event{Type: EvKick, Network: "net", Nick: "ME", Kicker: "op", Buffer: "#go", Text: "bye"})
	if net0(e).Channel("#go") == nil {
		t.Fatal("self-kick removed the buffer")
	}
	if len(net0(e).Channel("#go").Members) != 0 {
		t.Error("members not cleared on self-kick")
	}
	if len(net0(e).Params.Channels) != 1 {
		t.Error("self-kick dropped the autojoin entry")
	}
	last = sink.msgs[len(sink.msgs)-1]
	if last.Text != "you were kicked from #go by op (bye)" {
		t.Errorf("self-kick line = %q", last.Text)
	}
}

// authFailConn fails Connect with ErrAuthFailed and counts the attempts.
type authFailConn struct {
	fakeRuntimeConn
	mu       sync.Mutex
	attempts int
}

func (c *authFailConn) Connect(context.Context) error {
	c.mu.Lock()
	c.attempts++
	c.mu.Unlock()
	return fmt.Errorf("irc test: %w: SASL PLAIN failed", ErrAuthFailed)
}

func (c *authFailConn) tries() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attempts
}

// TestAuthFailureStopsRetrying: bad credentials must park the network (one
// dial, no reconnect loop hammering services with the same wrong password),
// with a status line telling the user what to fix.
func TestAuthFailureStopsRetrying(t *testing.T) {
	sink := &captureSink{}
	e := New(Options{Sink: sink})
	conn := &authFailConn{}
	e.AddNetwork(NetworkParams{ID: "net", Name: "net", Nick: "me"}, conn)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = e.Run(ctx); close(done) }()

	deadline := time.After(3 * time.Second)
	for {
		if s := e.SnapshotNetwork("net"); s != nil && s.State == StateDisconnected && conn.tries() == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("network not parked: tries=%d", conn.tries())
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Give a would-be retry loop time to strike, then confirm it didn't.
	time.Sleep(150 * time.Millisecond)
	if got := conn.tries(); got != 1 {
		t.Errorf("Connect attempts = %d, want 1 (no retries on auth failure)", got)
	}
	cancel()
	<-done // engine stopped — sink is safe to read now
	found := false
	for _, m := range sink.msgs {
		if strings.Contains(m.Text, "authentication failed") {
			found = true
		}
	}
	if !found {
		t.Error("no explanatory status line emitted")
	}
}

func TestApplyInviteAndAccount(t *testing.T) {
	e, sink := newTestEngine(t)
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "me", Buffer: "#go"})
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "alice", Buffer: "#go"})

	// account-notify: login/logout updates the member entry silently.
	e.apply(Event{Type: EvAccount, Network: "net", Nick: "alice", Account: "svc-alice"})
	if got := net0(e).Channel("#go").Members["alice"].Account; got != "svc-alice" {
		t.Errorf("account after login = %q", got)
	}
	e.apply(Event{Type: EvAccount, Network: "net", Nick: "alice", Account: ""})
	if got := net0(e).Channel("#go").Members["alice"].Account; got != "" {
		t.Errorf("account after logout = %q", got)
	}

	// An invite addressed to us lands in the status buffer.
	e.apply(Event{Type: EvInvite, Network: "net", Nick: "op", NewNick: "ME", Buffer: "#secret"})
	last := sink.msgs[len(sink.msgs)-1]
	if last.Buffer != StatusBuffer || !strings.Contains(last.Text, "op invited you to #secret") {
		t.Errorf("self-invite line = %q in %q", last.Text, last.Buffer)
	}
	// invite-notify for someone else lands in the shared channel.
	e.apply(Event{Type: EvInvite, Network: "net", Nick: "op", NewNick: "bob", Buffer: "#go"})
	last = sink.msgs[len(sink.msgs)-1]
	if last.Buffer != "#go" || !strings.Contains(last.Text, "op invited bob") {
		t.Errorf("invite-notify line = %q in %q", last.Text, last.Buffer)
	}
}

// TestRFC1459Fold: []\~ fold to {}|^ — nick[m] and nick{m} are one member.
func TestRFC1459Fold(t *testing.T) {
	e, _ := newTestEngine(t)
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "nick[m]", Buffer: "#go"})
	e.apply(Event{Type: EvAway, Network: "net", Nick: "nick{m}", Away: true})
	m := net0(e).Channel("#go").Members[lower("NICK[M]")]
	if m == nil || !m.Away {
		t.Fatalf("rfc1459 fold broken: %+v", net0(e).Channel("#go").Members)
	}
	if !eqFold(`ABC[]\~`, `abc{}|^`) {
		t.Error(`eqFold(ABC[]\~, abc{}|^) = false`)
	}
}
