package core

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedSink is a captureSink safe to read while the loop runs.
type lockedSink struct {
	mu sync.Mutex
	captureSink
}

func (s *lockedSink) Print(m Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.captureSink.Print(m)
}

func (s *lockedSink) NetworkChanged(n *Network) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.captureSink.NetworkChanged(n)
}

func (s *lockedSink) texts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.msgs {
		out = append(out, m.Text)
	}
	return out
}

func waitTrue(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestResumedConnectSkipsPerformAndAutojoin(t *testing.T) {
	conn := &fakeConnector{}
	sink := &lockedSink{}
	e := New(Options{Sink: sink, Connector: conn})
	if err := e.AddNetworkLive(NetworkParams{
		ID: "n", Name: "Net", Addr: "irc.example:6697", Nick: "me",
		Perform: []string{"/msg NickServ IDENTIFY secret"}, Channels: []string{"#a"},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = e.Run(ctx) }()
	waitForNetworkState(t, e, "n", StateConnecting)

	e.HandleEvent(Event{Type: EvConnect, Network: "n", Nick: "me_", Resumed: true, Args: []string{"#a", "#b", ""}})
	waitForNetworkState(t, e, "n", StateRegistered)

	n := e.SnapshotNetwork("n")
	if n.Nick != "me_" {
		t.Fatalf("nick %q", n.Nick)
	}
	var names []string
	for _, c := range n.Channels {
		names = append(names, c.Name)
	}
	slices.Sort(names)
	if !slices.Contains(names, "#a") || !slices.Contains(names, "#b") {
		t.Fatalf("channels %v, want #a and #b recreated", names)
	}
	waitTrue(t, "resume line", func() bool { return slices.Contains(sink.texts(), "resumed Net as me_") })
	for _, txt := range sink.texts() {
		if strings.Contains(txt, "has joined") || strings.HasPrefix(txt, "connected to") {
			t.Fatalf("resume emitted %q", txt)
		}
	}
	// Perform waits performCommandDelay between lines; give it longer.
	time.Sleep(performCommandDelay + 300*time.Millisecond)
	if raws := conn.conns[0].rawsSnap(); len(raws) != 0 {
		t.Fatalf("resume sent %v; perform/autojoin must not run", raws)
	}
}

func TestResumeCancelsPendingPerform(t *testing.T) {
	conn := &fakeConnector{}
	e := New(Options{Sink: &lockedSink{}, Connector: conn})
	if err := e.AddNetworkLive(NetworkParams{
		ID: "n", Name: "Net", Addr: "irc.example:6697", Nick: "me",
		Perform: []string{"/raw FIRST", "/raw SECOND"},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = e.Run(ctx) }()
	waitForNetworkState(t, e, "n", StateConnecting)

	e.HandleEvent(Event{Type: EvConnect, Network: "n", Nick: "me"})
	waitTrue(t, "first perform line", func() bool { return slices.Contains(conn.conns[0].rawsSnap(), "FIRST") })
	// The connection is replaced by a resumed one before the second line.
	e.HandleEvent(Event{Type: EvConnect, Network: "n", Nick: "me", Resumed: true})
	time.Sleep(performCommandDelay + 300*time.Millisecond)
	if slices.Contains(conn.conns[0].rawsSnap(), "SECOND") {
		t.Fatal("a stale perform sequence ran on into the resumed connection")
	}
}

func TestSyncRunsAfterQueuedEvents(t *testing.T) {
	sink := &lockedSink{}
	e := New(Options{Sink: sink})
	e.AddNetwork(NetworkParams{ID: "n", Name: "n", Nick: "me"}, &fakeRuntimeConn{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = e.Run(ctx) }()

	const n = 200
	for i := range n {
		e.HandleEvent(Event{Type: EvMessageIn, Network: "n", Message: &Message{
			Network: "n", Buffer: "#c", From: "bob", Kind: MsgPrivmsg, Text: "m" + string(rune('a'+i%26)),
		}})
	}
	got := make(chan int, 1)
	e.Sync(func() {
		sink.mu.Lock()
		got <- len(sink.msgs)
		sink.mu.Unlock()
	})
	select {
	case c := <-got:
		if c < n {
			t.Fatalf("Sync ran with %d of %d earlier events committed", c, n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Sync callback never ran")
	}
}

// detachConn is a connection that can outlive the process. On shutdown its
// Connect emits one last message (a line read just before it stopped) and a
// disconnect, as girc does.
type detachConn struct {
	fakeRuntimeConn
	h        ConnHandler
	mu       sync.Mutex
	detached int
	closed   int
}

func (c *detachConn) Connect(ctx context.Context) error {
	<-ctx.Done()
	c.h.HandleEvent(Event{Type: EvMessageIn, Network: "n", Message: &Message{
		Network: "n", Buffer: "#c", From: "bob", Kind: MsgPrivmsg, Text: "last words",
	}})
	c.h.HandleEvent(Event{Type: EvDisconnect, Network: "n", Text: "irc.example:6697"})
	return ctx.Err()
}

func (c *detachConn) Detach() error {
	c.mu.Lock()
	c.detached++
	c.mu.Unlock()
	return nil
}

func (c *detachConn) Close() error {
	c.mu.Lock()
	c.closed++
	c.mu.Unlock()
	return nil
}

type detachConnector struct{ conn *detachConn }

func (d *detachConnector) Dial(_ NetworkParams, h ConnHandler) (IRCConn, error) {
	d.conn.h = h
	return d.conn, nil
}

func TestShutdownDetachesAndCommitsFinalEvents(t *testing.T) {
	dc := &detachConn{}
	sink := &lockedSink{}
	e := New(Options{Sink: sink, Connector: &detachConnector{conn: dc}})
	if err := e.AddNetworkLive(NetworkParams{ID: "n", Name: "n", Addr: "irc.example:6697", Nick: "me"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = e.Run(ctx) }()
	waitForNetworkState(t, e, "n", StateConnecting)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}

	dc.mu.Lock()
	detached, closed := dc.detached, dc.closed
	dc.mu.Unlock()
	if detached != 1 || closed != 0 {
		t.Fatalf("shutdown: Detach %d, Close %d; want a detach only", detached, closed)
	}
	texts := sink.texts()
	if !slices.Contains(texts, "last words") {
		t.Fatalf("an event queued during shutdown was dropped: %v", texts)
	}
	for _, txt := range texts {
		if strings.HasPrefix(txt, "disconnected") {
			t.Fatalf("shutdown recorded %q", txt)
		}
	}
	// HandleEvent after shutdown must not block.
	returned := make(chan struct{})
	go func() {
		e.HandleEvent(Event{Type: EvMessageIn, Network: "n"})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("HandleEvent blocked after shutdown")
	}
}

func TestDisconnectStillRecordedWhileRunning(t *testing.T) {
	sink := &lockedSink{}
	e := New(Options{Sink: sink})
	e.AddNetwork(NetworkParams{ID: "n", Name: "n", Nick: "me"}, &fakeRuntimeConn{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = e.Run(ctx) }()
	e.HandleEvent(Event{Type: EvDisconnect, Network: "n", Text: "ping timeout"})
	waitTrue(t, "disconnect line", func() bool { return slices.Contains(sink.texts(), "disconnected: ping timeout") })
}
