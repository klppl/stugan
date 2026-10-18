package core

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestChatHistoryLine(t *testing.T) {
	now := time.Date(2024, 5, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		buffer string
		args   []string
		want   string
		err    string
	}{
		{"default", "#c", nil, "CHATHISTORY LATEST #c * 50", ""},
		{"legacy count", "#c", []string{"10"}, "CHATHISTORY LATEST #c * 10", ""},
		{"latest count", "#c", []string{"latest", "7"}, "CHATHISTORY LATEST #c * 7", ""},
		{"latest ref", "#c", []string{"latest", "msgid=abc", "5"}, "CHATHISTORY LATEST #c msgid=abc 5", ""},
		{"before msgid", "#c", []string{"before", "msgid=abc"}, "CHATHISTORY BEFORE #c msgid=abc 50", ""},
		{"after date", "bob", []string{"after", "2024-05-01", "20"}, "CHATHISTORY AFTER bob timestamp=2024-05-01T00:00:00.000Z 20", ""},
		{"around rfc3339", "#c", []string{"AROUND", "2024-05-01T10:30:00+02:00"}, "CHATHISTORY AROUND #c timestamp=2024-05-01T08:30:00.000Z 50", ""},
		{"between", "#c", []string{"between", "2024-05-01", "msgid=z", "9"}, "CHATHISTORY BETWEEN #c timestamp=2024-05-01T00:00:00.000Z msgid=z 9", ""},
		{"limit clamped", "#c", []string{"latest", "99999"}, "CHATHISTORY LATEST #c * 1000", ""},
		{"targets default", "*status", []string{"targets"}, "CHATHISTORY TARGETS timestamp=2024-05-03T12:00:00.000Z timestamp=2024-05-10T12:01:00.000Z 50", ""},
		{"targets range", "#c", []string{"targets", "2024-05-01", "2024-05-02", "5"}, "CHATHISTORY TARGETS timestamp=2024-05-01T00:00:00.000Z timestamp=2024-05-02T00:00:00.000Z 5", ""},
		{"status buffer", "*status", []string{"latest"}, "", "works in a channel or query"},
		{"bad ref", "#c", []string{"before", "yesterday"}, "", "bad reference"},
		{"missing ref", "#c", []string{"before"}, "", "usage"},
		{"between needs two", "#c", []string{"between", "2024-05-01"}, "", "usage"},
		{"zero limit", "#c", []string{"latest", "0"}, "", "at least 1"},
		{"unknown", "#c", []string{"sideways"}, "", "usage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := chatHistoryLine(tt.buffer, tt.args, now)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("err = %v, want containing %q", err, tt.err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

// historyStub is a HistoryReader that knows each buffer's newest stored line.
type historyStub struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (h *historyStub) Backlog(context.Context, string, string, int64, int) ([]Message, bool, error) {
	return nil, false, nil
}
func (h *historyStub) LastChatTime(_ context.Context, _, buffer string) (time.Time, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.last[FoldIRC(buffer)], nil
}

func waitRaw(t *testing.T, c *fakeRuntimeConn, want string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if slices.Contains(c.rawsSnap(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never sent %q; sent %v", want, c.rawsSnap())
}

func newCatchUpEngine(t *testing.T, last map[string]time.Time) (*Engine, *fakeRuntimeConn, *captureSink) {
	t.Helper()
	conn := &fakeConnector{caps: []string{"draft/chathistory"}}
	sink := &captureSink{}
	e := New(Options{Sink: sink, Connector: conn, History: &historyStub{last: last}})
	if err := e.AddNetworkLive(NetworkParams{ID: "n", Name: "n", Addr: "a:1", Nick: "me"}); err != nil {
		t.Fatal(err)
	}
	return e, conn.conns[0], sink
}

func TestCatchUpOnJoinPagesForward(t *testing.T) {
	t0 := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	e, conn, _ := newCatchUpEngine(t, map[string]time.Time{"#c": t0})
	ctx := context.Background()

	e.handle(ctx, Event{Type: EvJoin, Network: "n", Nick: "someone", Buffer: "#c"})
	if raws := conn.rawsSnap(); len(raws) != 0 {
		t.Fatalf("someone else's join triggered %v", raws)
	}

	e.handle(ctx, Event{Type: EvJoin, Network: "n", Nick: "me", Buffer: "#c"})
	waitRaw(t, conn, "CHATHISTORY AFTER #c timestamp=2024-05-01T10:00:00.000Z 100")

	// A full page continues from the newest line it carried…
	t1 := t0.Add(time.Hour)
	e.handle(ctx, Event{Type: EvHistoryBatch, Network: "n", Buffer: "#c", Count: catchUpLimit, Time: t1})
	waitRaw(t, conn, "CHATHISTORY AFTER #c timestamp=2024-05-01T11:00:00.000Z 100")

	// …and a short page ends the chain.
	n := len(conn.rawsSnap())
	e.handle(ctx, Event{Type: EvHistoryBatch, Network: "n", Buffer: "#c", Count: 3, Time: t1.Add(time.Minute)})
	e.handle(ctx, Event{Type: EvHistoryBatch, Network: "n", Buffer: "#c", Count: catchUpLimit, Time: t1.Add(time.Hour)})
	if got := len(conn.rawsSnap()); got != n {
		t.Fatalf("chain continued after a short page: %v", conn.rawsSnap()[n:])
	}
}

func TestCatchUpStopsWithoutProgressOrAtCap(t *testing.T) {
	t0 := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	e, conn, _ := newCatchUpEngine(t, map[string]time.Time{"#c": t0})
	ctx := context.Background()
	e.handle(ctx, Event{Type: EvJoin, Network: "n", Nick: "me", Buffer: "#c"})
	waitRaw(t, conn, "CHATHISTORY AFTER #c timestamp=2024-05-01T10:00:00.000Z 100")

	// A full page that does not advance past the reference must not loop.
	e.handle(ctx, Event{Type: EvHistoryBatch, Network: "n", Buffer: "#c", Count: catchUpLimit, Time: t0})
	if got := len(conn.rawsSnap()); got != 1 {
		t.Fatalf("looped without progress: %v", conn.rawsSnap())
	}

	// Pages are capped.
	e.handle(ctx, Event{Type: EvJoin, Network: "n", Nick: "me", Buffer: "#d"}) // no stored lines: nothing sent
	e.mu.Lock()
	e.catchUps[catchUpKey("n", "#c")] = &catchUp{pages: 1, since: t0}
	e.mu.Unlock()
	for i := 1; i <= catchUpMaxPages+2; i++ {
		e.handle(ctx, Event{Type: EvHistoryBatch, Network: "n", Buffer: "#c", Count: catchUpLimit, Time: t0.Add(time.Duration(i) * time.Minute)})
	}
	if got, want := len(conn.rawsSnap()), 1+catchUpMaxPages-1; got != want {
		t.Fatalf("sent %d requests, want %d: %v", got, want, conn.rawsSnap())
	}
}

func TestCatchUpTargets(t *testing.T) {
	t0 := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	e, conn, sink := newCatchUpEngine(t, map[string]time.Time{
		"#c":    t0,
		"alice": t0.Add(-time.Hour),
	})
	ctx := context.Background()
	e.mu.Lock()
	n := e.user.Network("n")
	n.getOrCreate("#c", KindChannel)
	n.getOrCreate("alice", KindQuery)
	e.mu.Unlock()

	e.handle(ctx, Event{Type: EvConnect, Network: "n", Nick: "me"})
	// Baseline is the newest line across buffers.
	for i := 0; i < 200 && !slices.ContainsFunc(conn.rawsSnap(), func(s string) bool {
		return strings.HasPrefix(s, "CHATHISTORY TARGETS timestamp=2024-05-01T10:00:00.000Z ")
	}); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !slices.ContainsFunc(conn.rawsSnap(), func(s string) bool {
		return strings.HasPrefix(s, "CHATHISTORY TARGETS timestamp=2024-05-01T10:00:00.000Z timestamp=") && strings.HasSuffix(s, " 100")
	}) {
		t.Fatalf("no TARGETS request: %v", conn.rawsSnap())
	}

	// A known query with news is caught up from its own newest line.
	e.handle(ctx, Event{Type: EvHistoryTarget, Network: "n", Buffer: "alice", Time: t0})
	waitRaw(t, conn, "CHATHISTORY AFTER alice timestamp=2024-05-01T09:00:00.000Z 100")
	// One with nothing new is not requested; neither is a channel or ourselves.
	e.handle(ctx, Event{Type: EvHistoryTarget, Network: "n", Buffer: "alice", Time: t0.Add(-2 * time.Hour)})
	e.handle(ctx, Event{Type: EvHistoryTarget, Network: "n", Buffer: "#c", Time: t0.Add(time.Hour)})
	e.handle(ctx, Event{Type: EvHistoryTarget, Network: "n", Buffer: "me", Time: t0.Add(time.Hour)})
	// A brand-new conversation gets a buffer and the TARGETS baseline.
	e.handle(ctx, Event{Type: EvHistoryTarget, Network: "n", Buffer: "carol", Time: t0.Add(time.Hour)})
	waitRaw(t, conn, "CHATHISTORY AFTER carol timestamp=2024-05-01T10:00:00.000Z 100")

	e.mu.RLock()
	carol := e.user.Network("n").Channel("carol")
	me := e.user.Network("n").Channel("me")
	e.mu.RUnlock()
	if carol == nil || carol.Kind != KindQuery {
		t.Errorf("carol buffer = %+v", carol)
	}
	if me != nil {
		t.Error("opened a query with ourselves")
	}
	for _, r := range conn.rawsSnap() {
		if strings.HasPrefix(r, "CHATHISTORY AFTER #c") || strings.HasPrefix(r, "CHATHISTORY AFTER me") {
			t.Errorf("unexpected request %q", r)
		}
	}
	_ = sink
}

func TestCatchUpNeedsCapAndBaseline(t *testing.T) {
	// No stored lines anywhere: no TARGETS. No cap: nothing at all.
	e, conn, _ := newCatchUpEngine(t, nil)
	e.handle(context.Background(), Event{Type: EvConnect, Network: "n", Nick: "me"})
	e.handle(context.Background(), Event{Type: EvJoin, Network: "n", Nick: "me", Buffer: "#c"})
	time.Sleep(50 * time.Millisecond)
	for _, r := range conn.rawsSnap() {
		if strings.HasPrefix(r, "CHATHISTORY") {
			t.Errorf("sent %q with nothing stored", r)
		}
	}

	c2 := &fakeConnector{}
	e2 := New(Options{Sink: &captureSink{}, Connector: c2, History: &historyStub{last: map[string]time.Time{"#c": time.Now()}}})
	_ = e2.AddNetworkLive(NetworkParams{ID: "n", Name: "n", Addr: "a:1", Nick: "me"})
	e2.handle(context.Background(), Event{Type: EvJoin, Network: "n", Nick: "me", Buffer: "#c"})
	time.Sleep(50 * time.Millisecond)
	if raws := c2.conns[0].rawsSnap(); len(raws) != 0 {
		t.Errorf("sent %v without the cap", raws)
	}
}
