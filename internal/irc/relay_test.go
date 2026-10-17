package irc

import (
	"bufio"
	"context"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klippelism/stugan/internal/core"
	"github.com/klippelism/stugan/internal/engine"
	"github.com/klippelism/stugan/internal/enginelink"
)

const relaySecret = "0123456789abcdef-irc-relay-test"

// autoIRCd is a fake IRC server that registers girc clients and answers the
// handful of commands the relay tests use. Every line it receives is kept.
type autoIRCd struct {
	t  *testing.T
	ln net.Listener

	mu    sync.Mutex
	conns []*autoConn
}

type autoConn struct {
	c    net.Conn
	mu   sync.Mutex
	got  []string
	eof  chan struct{}
	nick string
}

func newAutoIRCd(t *testing.T) *autoIRCd {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d := &autoIRCd{t: t, ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			ac := &autoConn{c: c, eof: make(chan struct{})}
			d.mu.Lock()
			d.conns = append(d.conns, ac)
			d.mu.Unlock()
			go ac.serve()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, c := range d.conns {
			_ = c.c.Close()
		}
	})
	return d
}

func (d *autoIRCd) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.conns)
}

func (d *autoIRCd) conn(i int) *autoConn {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conns[i]
}

func (c *autoConn) send(lines ...string) {
	for _, l := range lines {
		_, _ = io.WriteString(c.c, l+"\r\n")
	}
}

func (c *autoConn) lines() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.got)
}

func (c *autoConn) has(prefix string) bool {
	for _, l := range c.lines() {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

func (c *autoConn) count(prefix string) int {
	n := 0
	for _, l := range c.lines() {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func (c *autoConn) serve() {
	defer close(c.eof)
	br := bufio.NewReader(c.c)
	for {
		raw, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line := strings.TrimRight(raw, "\r\n")
		c.mu.Lock()
		c.got = append(c.got, line)
		c.mu.Unlock()
		cmd, rest, _ := strings.Cut(line, " ")
		switch strings.ToUpper(cmd) {
		case "CAP":
			switch {
			case strings.HasPrefix(rest, "LS"):
				c.send(":irc.test CAP * LS :multi-prefix server-time message-tags")
			case strings.HasPrefix(rest, "REQ"):
				_, caps, _ := strings.Cut(rest, ":")
				c.send(":irc.test CAP * ACK :" + caps)
			case strings.HasPrefix(rest, "END"):
				n := c.nick
				c.send(
					":irc.test 001 "+n+" :Welcome "+n,
					":irc.test 005 "+n+" PREFIX=(ov)@+ CHANTYPES=# :are supported",
					":irc.test 372 "+n+" :- motd",
					":irc.test 376 "+n+" :End of MOTD",
				)
			}
		case "NICK":
			c.nick = strings.TrimPrefix(rest, ":")
		case "PING":
			c.send(":irc.test PONG irc.test :" + strings.TrimPrefix(rest, ":"))
		case "JOIN":
			ch := strings.TrimPrefix(rest, ":")
			c.send(
				":"+c.nick+"!u@host JOIN "+ch,
				":irc.test 332 "+c.nick+" "+ch+" :the topic",
				":irc.test 353 "+c.nick+" = "+ch+" :"+c.nick+" @bob",
				":irc.test 366 "+c.nick+" "+ch+" :End of /NAMES list.",
			)
		case "NAMES":
			c.send(":irc.test 353 "+c.nick+" = "+rest+" :"+c.nick+" @bob carol", ":irc.test 366 "+c.nick+" "+rest+" :End of /NAMES list.")
		case "TOPIC":
			c.send(":irc.test 332 " + c.nick + " " + rest + " :the topic")
		}
	}
}

// recorder is a ConnHandler with the engine's Sync barrier.
type recorder struct {
	mu     sync.Mutex
	events []core.Event
	syncs  int
}

func (r *recorder) HandleEvent(ev core.Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *recorder) Sync(fn func()) {
	r.mu.Lock()
	r.syncs++
	r.mu.Unlock()
	fn()
}

func (r *recorder) snapshot() []core.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

// waitEvent waits for an event matching pred and returns its index.
func (r *recorder) waitEvent(t *testing.T, what string, pred func(core.Event) bool) (core.Event, int) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		for i, ev := range r.snapshot() {
			if pred(ev) {
				return ev, i
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; got %d events", what, len(r.snapshot()))
	return core.Event{}, -1
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type relayEngine struct {
	srv  *engine.Server
	addr string
}

func startRelayEngine(t *testing.T) *relayEngine {
	t.Helper()
	srv, err := engine.New(engine.Options{Secret: relaySecret})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ctx, ln) }()
	t.Cleanup(func() { cancel(); <-done })
	return &relayEngine{srv: srv, addr: ln.Addr().String()}
}

func relayLink(t *testing.T, addr string, gen int64) *enginelink.Link {
	t.Helper()
	l, err := enginelink.New(enginelink.Config{URL: addr, Secret: relaySecret, Instance: "dddddddddddddddd", Gen: gen})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// process is one simulated stugan process: a link plus one connection.
type process struct {
	link   *enginelink.Link
	conn   *Conn
	rec    *recorder
	cancel context.CancelFunc
	done   chan error
}

func startProcess(t *testing.T, eng *relayEngine, d *autoIRCd, gen int64) *process {
	t.Helper()
	p := &process{link: relayLink(t, eng.addr, gen), rec: &recorder{}, done: make(chan error, 1)}
	c, err := New(Options{
		Network: "net", Addr: d.ln.Addr().String(), Nick: "alice", User: "u",
		Relay: p.link, RelayID: "u\x00net",
	}, p.rec)
	if err != nil {
		t.Fatal(err)
	}
	p.conn = c
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	go func() { p.done <- c.Connect(ctx) }()
	t.Cleanup(func() { cancel(); p.link.Close() })
	return p
}

// shutdown stops the process the way core does: cancel, detach, wait, close
// the link.
func (p *process) shutdown(t *testing.T) {
	t.Helper()
	p.cancel()
	_ = p.conn.Detach()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Connect did not return")
	}
	p.link.Close()
}

func isConnect(resumed bool) func(core.Event) bool {
	return func(ev core.Event) bool { return ev.Type == core.EvConnect && ev.Resumed == resumed }
}

func isMessage(text string) func(core.Event) bool {
	return func(ev core.Event) bool {
		return ev.Type == core.EvMessageIn && ev.Message != nil && ev.Message.Text == text
	}
}

func TestRelayResumeEndToEnd(t *testing.T) {
	d := newAutoIRCd(t)
	eng := startRelayEngine(t)

	// First process: a normal registration through the engine.
	p1 := startProcess(t, eng, d, 1)
	if ev, _ := p1.rec.waitEvent(t, "first connect", isConnect(false)); ev.Nick != "alice" {
		t.Fatalf("connect nick %q", ev.Nick)
	}
	if err := p1.conn.SendRaw("JOIN #chan"); err != nil {
		t.Fatal(err)
	}
	p1.rec.waitEvent(t, "self join", func(ev core.Event) bool { return ev.Type == core.EvJoin && ev.Nick == "alice" })
	up := d.conn(0)
	up.send("@msgid=m1 :bob!b@h PRIVMSG #chan :hello")
	ev, _ := p1.rec.waitEvent(t, "message", isMessage("hello"))
	for k := range ev.Message.Tags {
		if strings.HasPrefix(k, "stugan/") {
			t.Fatalf("relay tag %q leaked into the message", k)
		}
	}
	// Everything is acked once girc has caught up (ack probe + barrier).
	eventually(t, "acks", func() bool { return eng.srv.Stats().BufferedBytes == 0 })
	p1.rec.mu.Lock()
	syncs := p1.rec.syncs
	p1.rec.mu.Unlock()
	if syncs == 0 {
		t.Fatal("acks bypassed the core barrier")
	}

	// stugan restarts.
	p1.shutdown(t)
	eventually(t, "detach", func() bool { return eng.srv.Stats().Attached == 0 })
	if eng.srv.Stats().Sessions != 1 {
		t.Fatal("the engine let go of the IRC socket")
	}
	sentAt := time.Now()
	up.send(":bob!b@h PRIVMSG #chan :while you were away")
	time.Sleep(300 * time.Millisecond)

	p2 := startProcess(t, eng, d, 2)
	resumed, ri := p2.rec.waitEvent(t, "resumed connect", isConnect(true))
	if resumed.Nick != "alice" || !slices.Equal(resumed.Args, []string{"#chan"}) {
		t.Fatalf("resumed %+v", resumed)
	}
	away, ai := p2.rec.waitEvent(t, "backlog message", isMessage("while you were away"))
	if ai < ri {
		t.Fatal("backlog reached core before the resumed connect")
	}
	if dt := away.Message.Time.Sub(sentAt); dt < -time.Second || dt > 250*time.Millisecond {
		t.Fatalf("backlog stamped %v after it arrived; want the engine's receive time", dt)
	}
	// The acked message is not delivered again.
	for _, ev := range p2.rec.snapshot() {
		if isMessage("hello")(ev) {
			t.Fatal("an acked line was re-delivered")
		}
	}

	// Same socket: no new connection, no second registration.
	if d.count() != 1 {
		t.Fatalf("the server saw %d connections", d.count())
	}
	for _, prefix := range []string{"NICK ", "USER ", "CAP LS"} {
		if n := up.count(prefix); n != 1 {
			t.Fatalf("server received %q %d times", prefix, n)
		}
	}
	// Members and topic are refreshed.
	eventually(t, "NAMES/TOPIC refresh", func() bool { return up.has("NAMES #chan") && up.has("TOPIC #chan") })
	p2.rec.waitEvent(t, "names", func(ev core.Event) bool { return ev.Type == core.EvNames && len(ev.Members) == 3 })

	// Writes flow again.
	if err := p2.conn.Message("#chan", "back again"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "message out", func() bool { return up.has("PRIVMSG #chan :back again") })

	// The replayed welcome must not also produce a fresh connect (girc fires
	// CONNECTED 2s after the replayed 001).
	time.Sleep(2500 * time.Millisecond)
	for _, ev := range p2.rec.snapshot() {
		if isConnect(false)(ev) {
			t.Fatal("re-attach also produced a fresh EvConnect (perform/autojoin would rerun)")
		}
		if ev.Type == core.EvJoin {
			t.Fatalf("re-attach produced a join event: %+v", ev)
		}
		if ev.Type == core.EvNumeric && strings.Contains(ev.Text, "motd") {
			t.Fatal("the MOTD was shown again")
		}
	}

	// A real close ends the IRC connection.
	_ = p2.conn.Close()
	select {
	case <-up.eof:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not end the IRC socket")
	}
}

func TestRelayFreshConnectAfterEngineLostSocket(t *testing.T) {
	d := newAutoIRCd(t)
	eng := startRelayEngine(t)
	p1 := startProcess(t, eng, d, 1)
	p1.rec.waitEvent(t, "first connect", isConnect(false))
	p1.shutdown(t)
	eventually(t, "detach", func() bool { return eng.srv.Stats().Attached == 0 })
	// The server drops us while stugan is down.
	_ = d.conn(0).c.Close()
	eventually(t, "session gone", func() bool { return eng.srv.Stats().Sessions == 0 })

	p2 := startProcess(t, eng, d, 2)
	p2.rec.waitEvent(t, "fresh connect", isConnect(false))
	if d.count() != 2 {
		t.Fatalf("server saw %d connections, want a fresh second one", d.count())
	}
}

func TestRelayEditedNetworkReconnects(t *testing.T) {
	d := newAutoIRCd(t)
	eng := startRelayEngine(t)
	p1 := startProcess(t, eng, d, 1)
	p1.rec.waitEvent(t, "first connect", isConnect(false))
	p1.shutdown(t)

	// Same network, different registration settings: must not re-attach.
	rec := &recorder{}
	link := relayLink(t, eng.addr, 2)
	defer link.Close()
	c, err := New(Options{
		Network: "net", Addr: d.ln.Addr().String(), Nick: "alice", User: "changed",
		Relay: link, RelayID: "u\x00net",
	}, rec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Connect(ctx) }()
	rec.waitEvent(t, "fresh connect", isConnect(false))
	<-d.conn(0).eof // the old socket was ended
	if d.count() != 2 {
		t.Fatalf("server saw %d connections", d.count())
	}
}

func TestRelayNickChangeKeepsSocket(t *testing.T) {
	// The nick is not part of the fingerprint: a nick changed on a live
	// connection (or in the network settings) must not force a reconnect.
	d := newAutoIRCd(t)
	eng := startRelayEngine(t)
	p1 := startProcess(t, eng, d, 1)
	p1.rec.waitEvent(t, "first connect", isConnect(false))
	d.conn(0).send(":alice!u@host NICK :alice2")
	p1.rec.waitEvent(t, "nick", func(ev core.Event) bool { return ev.Type == core.EvNick && ev.NewNick == "alice2" })
	p1.shutdown(t)

	rec := &recorder{}
	link := relayLink(t, eng.addr, 2)
	defer link.Close()
	c, err := New(Options{
		Network: "net", Addr: d.ln.Addr().String(), Nick: "somethingelse", User: "u",
		Relay: link, RelayID: "u\x00net",
	}, rec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Connect(ctx) }()
	ev, _ := rec.waitEvent(t, "resume", isConnect(true))
	if ev.Nick != "alice2" {
		t.Fatalf("resumed as %q, want the live nick", ev.Nick)
	}
	eventually(t, "girc nick", func() bool { return c.CurrentNick() == "alice2" })
	if d.count() != 1 {
		t.Fatal("nick change forced a reconnect")
	}
}

func TestRelayFingerprint(t *testing.T) {
	base := Options{Addr: "a:1", TLS: true, User: "u", Realname: "r", SASLUser: "s", SASLPass: "p", Nick: "n"}
	addrs := []string{"a:1"}
	fp := registrationFingerprint(base, addrs)
	same := base
	same.Nick = "other"
	same.Channels = []string{"#x"}
	if registrationFingerprint(same, addrs) != fp {
		t.Fatal("nick/channels must not change the fingerprint")
	}
	for name, mut := range map[string]func(*Options) []string{
		"addr":     func(o *Options) []string { return []string{"b:1"} },
		"fallback": func(o *Options) []string { return []string{"a:1", "b:2"} },
		"tls":      func(o *Options) []string { o.TLS = false; return addrs },
		"insecure": func(o *Options) []string { o.Insecure = true; return addrs },
		"user":     func(o *Options) []string { o.User = "x"; return addrs },
		"realname": func(o *Options) []string { o.Realname = "x"; return addrs },
		"sasluser": func(o *Options) []string { o.SASLUser = "x"; return addrs },
		"saslpass": func(o *Options) []string { o.SASLPass = "x"; return addrs },
		"pass":     func(o *Options) []string { o.ServerPass = "x"; return addrs },
		"external": func(o *Options) []string { o.SASLExternal = true; return addrs },
		"cert":     func(o *Options) []string { o.CertPEM = "x"; return addrs },
	} {
		o := base
		a := mut(&o)
		if registrationFingerprint(o, a) == fp {
			t.Errorf("%s change kept the fingerprint", name)
		}
	}
	if strings.Contains(fp, "p") && strings.Contains(fp, "s") && len(fp) != 64 {
		t.Fatalf("fingerprint %q is not a digest", fp)
	}
}

func TestRelayNeedsID(t *testing.T) {
	link := relayLink(t, "127.0.0.1:1", 1)
	defer link.Close()
	if _, err := New(Options{Addr: "a:1", Relay: link}, nil); err == nil {
		t.Fatal("relay without RelayID accepted")
	}
}
