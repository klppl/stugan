package core

import (
	"slices"
	"strings"
	"testing"
)

func TestParseBouncerAttrs(t *testing.T) {
	b := ParseBouncerAttrs("12", `name=Lib\sera;host=irc.libera.chat;port=6697;tls=1;nickname=al\:ex;state=connected;error=a\\b;extra=1`)
	want := BouncerNetwork{ID: "12", Name: "Lib era", Host: "irc.libera.chat", Port: 6697, TLS: true, Nick: "al;ex", State: "connected", Error: `a\b`}
	if b != want {
		t.Fatalf("got %+v, want %+v", b, want)
	}
	if b := ParseBouncerAttrs("1", "host=h;tls=0"); b.TLS || b.Port != 0 {
		t.Errorf("defaults wrong: %+v", b)
	}
}

func newBouncerEngine(t *testing.T) (*Engine, *fakeRuntimeConn) {
	t.Helper()
	conn := &fakeConnector{caps: []string{"soju.im/bouncer-networks"}}
	e := New(Options{Sink: &captureSink{}, Connector: conn})
	err := e.AddNetworkLive(NetworkParams{
		ID: "soju", Name: "soju", Addr: "bnc.example:6697", TLS: true, Nick: "me",
		SASLUser: "alex@laptop", SASLPass: "pw", ServerPass: "sp",
		Channels: []string{"#x"}, Perform: []string{"/msg x y"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return e, conn.conns[0]
}

func TestBouncerNetworkList(t *testing.T) {
	e, _ := newBouncerEngine(t)
	apply := func(ev Event) { ev.Network = "soju"; e.apply(ev) }
	list := func() []BouncerNetwork {
		e.mu.RLock()
		defer e.mu.RUnlock()
		return slices.Clone(e.user.Network("soju").Bouncer)
	}

	apply(Event{Type: EvBouncerNetwork, Bouncer: &BouncerNetwork{ID: "1", Name: "libera", State: "connecting"}})
	apply(Event{Type: EvBouncerNetwork, Bouncer: &BouncerNetwork{ID: "2", Name: "oftc"}})
	apply(Event{Type: EvBouncerNetwork, Bouncer: &BouncerNetwork{ID: "1", Name: "libera", State: "connected"}}) // upsert
	if got := list(); len(got) != 2 || got[0].State != "connected" {
		t.Fatalf("after upsert: %+v", got)
	}
	apply(Event{Type: EvBouncerNetwork, Target: "2"}) // deleted
	if got := list(); len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("after delete: %+v", got)
	}
	// The snapshot is a copy, not a view into live state.
	snap := e.SnapshotNetwork("soju")
	apply(Event{Type: EvBouncerNetwork, Bouncer: &BouncerNetwork{ID: "1", Name: "renamed"}})
	if snap.Bouncer[0].Name != "libera" {
		t.Error("snapshot aliases live state")
	}
	apply(Event{Type: EvBouncerNetwork}) // a new LISTNETWORKS reply starts
	if got := list(); len(got) != 0 {
		t.Fatalf("after reset: %+v", got)
	}

	apply(Event{Type: EvBouncerNetwork, Bouncer: &BouncerNetwork{ID: "1", Name: "libera"}})
	apply(Event{Type: EvDisconnect})
	if got := list(); len(got) != 0 {
		t.Fatalf("list survived a disconnect: %+v", got)
	}
}

func TestBouncerAddDelete(t *testing.T) {
	e, conn := newBouncerEngine(t)

	if err := e.BouncerAdd("soju", BouncerNetwork{Name: "my net", Host: "irc.example", Port: 6697, TLS: true, Nick: "al"}, "p;w d"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`BOUNCER ADDNETWORK host=irc.example;name=my\snet;port=6697;tls=1;nickname=al;pass=p\:w\sd`,
		"BOUNCER LISTNETWORKS",
	}
	if got := conn.rawsSnap(); !slices.Equal(got, want) {
		t.Fatalf("sent %q, want %q", got, want)
	}
	if err := e.BouncerAdd("soju", BouncerNetwork{}, ""); err == nil {
		t.Error("added a network without a host")
	}
	// Attribute injection is neutralised by escaping.
	conn.mu.Lock()
	conn.raws = nil
	conn.mu.Unlock()
	_ = e.BouncerAdd("soju", BouncerNetwork{Host: "h", Name: "x;pass=evil\r\nQUIT"}, "")
	if got := conn.rawsSnap()[0]; strings.ContainsAny(got, "\r\n") || strings.Contains(got, ";pass=") {
		t.Errorf("unescaped attribute: %q", got)
	}

	// Delete only known ids.
	e.apply(Event{Type: EvBouncerNetwork, Network: "soju", Bouncer: &BouncerNetwork{ID: "7", Name: "libera"}})
	if err := e.BouncerDelete("soju", "7 evil"); err == nil {
		t.Error("deleted an unknown id")
	}
	if err := e.BouncerDelete("soju", "7"); err != nil {
		t.Fatal(err)
	}
	if got := conn.rawsSnap(); got[len(got)-2] != "BOUNCER DELNETWORK 7" {
		t.Errorf("sent %q", got)
	}
}

func TestBouncerNeedsCapability(t *testing.T) {
	conn := &fakeConnector{}
	e := New(Options{Sink: &captureSink{}, Connector: conn})
	_ = e.AddNetworkLive(NetworkParams{ID: "n", Name: "n", Addr: "a:1", Nick: "me"})
	if err := e.BouncerAdd("n", BouncerNetwork{Host: "h"}, ""); err == nil {
		t.Error("BouncerAdd without the cap succeeded")
	}
	if err := e.BouncerDelete("n", "1"); err == nil {
		t.Error("BouncerDelete without the cap succeeded")
	}
	if err := e.BouncerAdd("nope", BouncerNetwork{Host: "h"}, ""); err == nil {
		t.Error("BouncerAdd on an unknown network succeeded")
	}
	e.handle(t.Context(), Event{Type: EvConnect, Network: "n", Nick: "me"})
	for _, r := range conn.conns[0].rawsSnap() {
		if strings.HasPrefix(r, "BOUNCER") {
			t.Errorf("sent %q without the cap", r)
		}
	}
}

func TestBouncerOpen(t *testing.T) {
	e, conn := newBouncerEngine(t)
	e.apply(Event{Type: EvBouncerNetwork, Network: "soju", Bouncer: &BouncerNetwork{ID: "3", Name: "libera"}})

	// Listing is requested when the control connection registers.
	e.handle(t.Context(), Event{Type: EvConnect, Network: "soju", Nick: "me"})
	if !slices.Contains(conn.rawsSnap(), "BOUNCER LISTNETWORKS") {
		t.Errorf("no LISTNETWORKS on connect: %v", conn.rawsSnap())
	}

	if err := e.BouncerOpen("soju", "3"); err != nil {
		t.Fatal(err)
	}
	p, ok := e.NetworkConfig("soju-libera")
	if !ok {
		t.Fatal("network not created")
	}
	if p.SASLUser != "alex/libera@laptop" || p.SASLPass != "pw" || p.ServerPass != "sp" ||
		p.Addr != "bnc.example:6697" || !p.TLS || p.Nick != "me" {
		t.Errorf("params = %+v", p)
	}
	if len(p.Channels) != 0 || len(p.Perform) != 0 {
		t.Errorf("control network's channels/perform leaked: %+v", p)
	}
	if err := e.BouncerOpen("soju", "3"); err == nil || !strings.Contains(err.Error(), "already open") {
		t.Errorf("second open: %v", err)
	}
	if err := e.BouncerOpen("soju", "99"); err == nil {
		t.Error("opened an unknown id")
	}
	// The control network itself is untouched.
	if c, _ := e.NetworkConfig("soju"); c.SASLUser != "alex@laptop" || len(c.Channels) != 1 {
		t.Errorf("control network changed: %+v", c)
	}
}
