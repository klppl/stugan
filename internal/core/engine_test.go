package core

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// captureSink records printed lines and network snapshots for assertions.
type captureSink struct {
	msgs    []Message
	nets    []*Network
	removed []string
	lists   [][]ChannelListItem
}

func (c *captureSink) Print(m Message)           { c.msgs = append(c.msgs, m) }
func (c *captureSink) NetworkChanged(n *Network) { c.nets = append(c.nets, n) }
func (c *captureSink) NetworkRemoved(id string)  { c.removed = append(c.removed, id) }
func (c *captureSink) ChannelList(_ string, items []ChannelListItem) {
	c.lists = append(c.lists, items)
}

// fakeConnector / fakeRuntimeConn back runtime AddNetworkLive in tests.
type fakeConnector struct {
	dialed int
	conns  []*fakeRuntimeConn
}

func (f *fakeConnector) Dial(NetworkParams, ConnHandler) (IRCConn, error) {
	f.dialed++
	c := &fakeRuntimeConn{}
	f.conns = append(f.conns, c)
	return c, nil
}

type fakeRuntimeConn struct{ raws []string }

func (c *fakeRuntimeConn) Connect(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
func (c *fakeRuntimeConn) SendRaw(s string) error            { c.raws = append(c.raws, s); return nil }
func (c *fakeRuntimeConn) Message(string, string) error      { return nil }
func (c *fakeRuntimeConn) Caps() []string                    { return nil }
func (c *fakeRuntimeConn) CurrentNick() string               { return "" }
func (c *fakeRuntimeConn) Close() error                      { return nil }

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

	e.apply(Event{Type: EvJoin, Network: "net", Nick: "alice", Channel: "#go"})
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
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "bob", Channel: "#GO"})
	if len(net0(e).Channels) != 1 {
		t.Fatalf("channel duplicated by case: %d channels", len(net0(e).Channels))
	}
	if len(net0(e).Channel("#go").Members) != 2 {
		t.Fatalf("members = %d, want 2", len(net0(e).Channel("#go").Members))
	}

	e.apply(Event{Type: EvPart, Network: "net", Nick: "alice", Channel: "#go", Text: "later"})
	if _, ok := net0(e).Channel("#go").Members["alice"]; ok {
		t.Error("alice still present after part")
	}

	e.apply(Event{Type: EvQuit, Network: "net", Nick: "bob", Text: "ping timeout"})
	if _, ok := net0(e).Channel("#go").Members["bob"]; ok {
		t.Error("bob still present after quit")
	}
}

func TestApplyNames(t *testing.T) {
	e, sink := newTestEngine(t)
	e.apply(Event{Type: EvNames, Network: "net", Channel: "#go", Members: []Member{
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

func TestApplyNickRename(t *testing.T) {
	e, _ := newTestEngine(t)
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "alice", Channel: "#go"})

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
	e.apply(Event{Type: EvTopic, Network: "net", Channel: "#go", Text: "hello world", Nick: "op"})
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

func TestNetworkChangedEmitted(t *testing.T) {
	e, sink := newTestEngine(t)

	// A join is a structural change: a network snapshot is emitted carrying
	// the new channel and member.
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "alice", Channel: "#go"})
	if len(sink.nets) != 1 {
		t.Fatalf("got %d network snapshots, want 1", len(sink.nets))
	}
	snap := sink.nets[0]
	ch := snap.Channel("#go")
	if ch == nil || ch.Members["alice"] == nil {
		t.Fatalf("snapshot missing #go/alice: %+v", snap)
	}

	// The snapshot is a copy: later mutation must not leak into it.
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "bob", Channel: "#go"})
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
	e.apply(Event{Type: EvJoin, Network: "net", Nick: "me", Channel: "#go"})
	if net0(e).Channel("#go") == nil {
		t.Fatal("we did not join #go")
	}
	e.apply(Event{Type: EvPart, Network: "net", Nick: "me", Channel: "#go"})
	if net0(e).Channel("#go") != nil {
		t.Error("our own part did not remove the buffer")
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
	e.apply(Event{Type: EvListItem, Network: "net", Channel: "#a", Count: 10, Text: "topic a"})
	e.apply(Event{Type: EvListItem, Network: "net", Channel: "#b", Count: 5, Text: "topic b"})
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
	e.apply(Event{Type: EvJoin, Network: "ghost", Nick: "x", Channel: "#y"})
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
func (dropHost) Commands() []string { return nil }
func (dropHost) Close() error       { return nil }

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
		e.HandleEvent(Event{Type: EvJoin, Network: "net", Nick: "x", Channel: "#y"})
		close(doneCh)
	}()
	select {
	case <-doneCh:
	case <-time.After(time.Second):
		t.Fatal("HandleEvent blocked after shutdown")
	}
}
