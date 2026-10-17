package enginelink

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klippelism/stugan/internal/engine"
)

const (
	secret  = "0123456789abcdef-link-test"
	waitFor = 5 * time.Second
)

type ircd struct {
	t     *testing.T
	ln    net.Listener
	conns chan *ircConn
}

type ircConn struct {
	t     *testing.T
	c     net.Conn
	lines chan string
	eof   chan struct{}
}

func newIRCd(t *testing.T) *ircd {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d := &ircd{t: t, ln: ln, conns: make(chan *ircConn, 8)}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			ic := &ircConn{t: t, c: c, lines: make(chan string, 256), eof: make(chan struct{})}
			go func() {
				defer close(ic.eof)
				br := bufio.NewReader(c)
				for {
					l, err := br.ReadString('\n')
					if err != nil {
						return
					}
					ic.lines <- strings.TrimRight(l, "\r\n")
				}
			}()
			d.conns <- ic
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return d
}

func (d *ircd) accept() *ircConn {
	d.t.Helper()
	select {
	case c := <-d.conns:
		d.t.Cleanup(func() { _ = c.c.Close() })
		return c
	case <-time.After(waitFor):
		d.t.Fatal("no IRC connection")
		return nil
	}
}

func (c *ircConn) send(lines ...string) {
	for _, l := range lines {
		_, _ = io.WriteString(c.c, l+"\r\n")
	}
}

func (c *ircConn) expect(want string) {
	c.t.Helper()
	select {
	case l := <-c.lines:
		if l != want {
			c.t.Fatalf("ircd got %q, want %q", l, want)
		}
	case <-time.After(waitFor):
		c.t.Fatalf("ircd: timed out waiting for %q", want)
	}
}

func (c *ircConn) quiet() {
	c.t.Helper()
	select {
	case l := <-c.lines:
		c.t.Fatalf("ircd: unexpected %q", l)
	case <-time.After(150 * time.Millisecond):
	}
}

func (c *ircConn) waitClosed() {
	c.t.Helper()
	select {
	case <-c.eof:
	case <-time.After(waitFor):
		c.t.Fatal("IRC connection not closed")
	}
}

type testEngine struct {
	srv    *engine.Server
	addr   string
	cancel context.CancelFunc
	done   chan struct{}
}

func startEngine(t *testing.T, addr string) *testEngine {
	t.Helper()
	srv, err := engine.New(engine.Options{Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	te := &testEngine{srv: srv, addr: ln.Addr().String(), cancel: cancel, done: make(chan struct{})}
	go func() { defer close(te.done); _ = srv.Serve(ctx, ln) }()
	t.Cleanup(te.stop)
	return te
}

func (te *testEngine) stop() {
	te.cancel()
	<-te.done
}

func newLink(t *testing.T, addr string, gen int64) *Link {
	t.Helper()
	l, err := New(Config{URL: addr, Secret: secret, Instance: "cccccccccccccccc", Gen: gen, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	return l
}

// reader reads lines from a session in the background.
type reader struct {
	lines  chan string
	err    chan error
	probes atomic.Int64
}

func readLines(s *Session) *reader {
	r := &reader{lines: make(chan string, 256), err: make(chan error, 1)}
	go func() {
		br := bufio.NewReader(s)
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				r.err <- err
				return
			}
			l = strings.TrimRight(l, "\r\n")
			if strings.HasSuffix(l, " "+AckProbeCommand) {
				r.probes.Add(1)
				continue
			}
			r.lines <- l
		}
	}()
	return r
}

func (r *reader) next(t *testing.T) string {
	t.Helper()
	select {
	case l := <-r.lines:
		return l
	case err := <-r.err:
		t.Fatalf("read failed: %v", err)
	case <-time.After(waitFor):
		t.Fatal("timed out reading")
	}
	return ""
}

func (r *reader) failed(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r.err:
		return err
	case l := <-r.lines:
		t.Fatalf("expected read error, got line %q", l)
	case <-time.After(waitFor):
		t.Fatal("expected read error")
	}
	return nil
}

func write(t *testing.T, s *Session, lines ...string) {
	t.Helper()
	if _, err := io.WriteString(s, strings.Join(lines, "\r\n")+"\r\n"); err != nil {
		t.Fatal(err)
	}
}

func req(d *ircd) ConnectRequest {
	return ConnectRequest{ID: "u\x00net", Addr: d.ln.Addr().String(), Fingerprint: "fp"}
}

// registerSession opens a fresh session and registers it as alice in #chan.
func registerSession(t *testing.T, l *Link, d *ircd) (*Session, *ircConn, *reader) {
	t.Helper()
	s, err := l.Open(context.Background(), req(d))
	if err != nil {
		t.Fatal(err)
	}
	if s.Info().Attached {
		t.Fatal("fresh connect reported attached")
	}
	ic := d.accept()
	r := readLines(s)
	write(t, s, "NICK alice", "USER u * * :r")
	ic.expect("NICK alice")
	ic.expect("USER u * * :r")
	ic.send(":irc.test 001 alice :Welcome", ":irc.test 005 alice CHANTYPES=# :ok", ":irc.test 376 alice :End", ":alice!u@h JOIN #chan")
	for range 4 {
		r.next(t)
	}
	return s, ic, r
}

func TestFreshSessionRelaysAndTags(t *testing.T) {
	d := newIRCd(t)
	te := startEngine(t, "")
	l := newLink(t, te.addr, 1)
	s, err := l.Open(context.Background(), req(d))
	if err != nil {
		t.Fatal(err)
	}
	ic := d.accept()
	r := readLines(s)
	restored := false
	s.OnRestored(func() { restored = true })
	if !restored || s.Restoring() {
		t.Fatal("a fresh session has nothing to restore")
	}

	// Partial writes are assembled into lines.
	_, _ = io.WriteString(s, "NICK al")
	_, _ = io.WriteString(s, "ice\r\nUSER u * * :r\r\n")
	ic.expect("NICK alice")
	ic.expect("USER u * * :r")

	ic.send("@msgid=abc;stugan/seq=999 :bob!b@h PRIVMSG alice :hi")
	got := r.next(t)
	if !strings.HasPrefix(got, "@stugan/seq=1;time=") || !strings.HasSuffix(got, ";msgid=abc :bob!b@h PRIVMSG alice :hi") {
		t.Fatalf("tagged line %q", got)
	}
	if strings.Contains(got, "999") {
		t.Fatalf("server-sent stugan/ tag survived: %q", got)
	}
}

func TestAttachReplaysAndSwallowsRegistration(t *testing.T) {
	d := newIRCd(t)
	te := startEngine(t, "")
	l1 := newLink(t, te.addr, 1)
	s1, ic, r1 := registerSession(t, l1, d)
	s1.Ack(4)
	waitUntil(t, "ack", func() bool { return te.srv.Stats().BufferedBytes == 0 })
	_ = s1.Close() // detach, as girc does on shutdown
	if err := r1.failed(t); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("read after Close: %v", err)
	}
	waitUntil(t, "detach", func() bool { return te.srv.Stats().Attached == 0 })
	ic.send(":bob!b@h PRIVMSG #chan :missed you")
	ic.quiet()

	l2 := newLink(t, te.addr, 2)
	s2, err := l2.Open(context.Background(), req(d))
	if err != nil {
		t.Fatal(err)
	}
	info := s2.Info()
	if !info.Attached || info.Nick != "alice" || len(info.Channels) != 1 || info.Channels[0] != "#chan" || info.Backlog != 1 || info.Replay != 2 {
		t.Fatalf("info %+v", info)
	}
	var restored atomic.Bool
	s2.OnRestored(func() { restored.Store(true) })
	r2 := readLines(s2)
	if l := r2.next(t); l != "@stugan/replay :irc.test 001 alice :Welcome" {
		t.Fatalf("replay[0] %q", l)
	}
	if l := r2.next(t); l != "@stugan/replay :irc.test 005 alice CHANTYPES=# :ok" {
		t.Fatalf("replay[1] %q", l)
	}
	if l := r2.next(t); l != "PING :"+s2.Marker() {
		t.Fatalf("marker %q", l)
	}
	if l := r2.next(t); !strings.Contains(l, "stugan/seq=5") || !strings.HasSuffix(l, ":bob!b@h PRIVMSG #chan :missed you") {
		t.Fatalf("backlog %q", l)
	}

	// The fresh client re-registers; none of it reaches the server.
	write(t, s2, "CAP LS 302", "NICK alice", "USER u * * :r", "CAP END")
	ic.quiet()
	if !s2.Restoring() || restored.Load() {
		t.Fatal("restore ended early")
	}
	write(t, s2, "PONG :"+s2.Marker(), "PRIVMSG #chan :back")
	ic.expect("PRIVMSG #chan :back")
	if s2.Restoring() || !restored.Load() {
		t.Fatal("restore did not end on the marker PONG")
	}
	if d := len(d.conns); d != 0 {
		t.Fatal("engine re-dialed instead of re-attaching")
	}
}

func TestTerminateEndsSocket(t *testing.T) {
	d := newIRCd(t)
	te := startEngine(t, "")
	l := newLink(t, te.addr, 1)
	s, ic, _ := registerSession(t, l, d)
	s.Terminate()
	ic.waitClosed()
	waitUntil(t, "forgotten", func() bool { return te.srv.Stats().Sessions == 0 })
}

func TestTerminateAfterCloseStillEnds(t *testing.T) {
	d := newIRCd(t)
	te := startEngine(t, "")
	l := newLink(t, te.addr, 1)
	s, ic, _ := registerSession(t, l, d)
	_ = s.Close()
	s.Terminate()
	ic.waitClosed()
}

func TestUpstreamCloseSurfacesAfterLines(t *testing.T) {
	d := newIRCd(t)
	te := startEngine(t, "")
	l := newLink(t, te.addr, 1)
	_, ic, r := registerSession(t, l, d)
	ic.send("ERROR :bye")
	_ = ic.c.Close()
	if got := r.next(t); !strings.HasSuffix(got, "ERROR :bye") {
		t.Fatalf("got %q", got)
	}
	var ue *UpstreamError
	if err := r.failed(t); !errors.As(err, &ue) {
		t.Fatalf("read error %v, want UpstreamError", err)
	}
	_ = te
}

func TestTakenOver(t *testing.T) {
	d := newIRCd(t)
	te := startEngine(t, "")
	l1 := newLink(t, te.addr, 1)
	_, _, r1 := registerSession(t, l1, d)
	l2 := newLink(t, te.addr, 2)
	if _, err := l2.Open(context.Background(), req(d)); err != nil {
		t.Fatal(err)
	}
	if err := r1.failed(t); !errors.Is(err, ErrTakenOver) {
		t.Fatalf("old session error %v", err)
	}
	// The older process can't take it back.
	if _, err := l1.Open(context.Background(), req(d)); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("older reclaim: %v", err)
	}
}

func TestLinkLossFailsSessionsAndRedials(t *testing.T) {
	d := newIRCd(t)
	te := startEngine(t, "")
	l := newLink(t, te.addr, 1)
	_, _, r := registerSession(t, l, d)
	te.stop()
	if err := r.failed(t); !errors.Is(err, ErrLinkLost) {
		t.Fatalf("read error %v", err)
	}
	if _, err := l.Open(context.Background(), req(d)); err == nil {
		t.Fatal("open succeeded with the engine down")
	}
	// The engine comes back on the same address: the link redials.
	te2 := startEngine(t, te.addr)
	_ = te2
	s, err := l.Open(context.Background(), req(d))
	if err != nil {
		t.Fatal(err)
	}
	if s.Info().Attached {
		t.Fatal("a restarted engine holds nothing to attach to")
	}
}

func TestRefusedOnBadSecret(t *testing.T) {
	te := startEngine(t, "")
	l, err := New(Config{URL: te.addr, Secret: "wrong-secret-xxxxxxxx", Instance: "cccccccccccccccc"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, err = l.Open(context.Background(), ConnectRequest{ID: "x", Addr: "127.0.0.1:1"})
	var re *RefusedError
	if !errors.As(err, &re) || !strings.Contains(re.Msg, "secret") {
		t.Fatalf("err %v", err)
	}
}

func TestUpstreamDialFailure(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	_ = ln.Close()
	te := startEngine(t, "")
	l := newLink(t, te.addr, 1)
	_, err := l.Open(context.Background(), ConnectRequest{ID: "x", Addr: dead})
	var ue *UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("err %v, want UpstreamError", err)
	}
}

func TestOpenCancelledDetaches(t *testing.T) {
	// The engine never answers (a listener that accepts and stalls), so Open
	// waits until its context ends.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 16)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		br := bufio.NewReader(c)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if strings.Contains(line, `"op":"hello"`) {
				_, _ = io.WriteString(c, `{"op":"hello","protocol":1}`+"\n")
			}
			got <- line
		}
	}()
	l := newLink(t, ln.Addr().String(), 1)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := l.Open(ctx, ConnectRequest{ID: "x", Addr: "h:1"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	deadline := time.After(waitFor)
	for {
		select {
		case line := <-got:
			if strings.Contains(line, `"op":"close"`) {
				t.Fatal("a cancelled open must detach, not end the socket")
			}
			if strings.Contains(line, `"op":"detach"`) {
				return
			}
		case <-deadline:
			t.Fatal("no detach after cancelled open")
		}
	}
}

func TestReadDeadline(t *testing.T) {
	d := newIRCd(t)
	te := startEngine(t, "")
	l := newLink(t, te.addr, 1)
	s, err := l.Open(context.Background(), req(d))
	if err != nil {
		t.Fatal(err)
	}
	_ = s.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	buf := make([]byte, 10)
	if _, err := s.Read(buf); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	var ne net.Error
	if _, err := s.Read(buf); !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("expired deadline: %v", err)
	}
	_ = s.SetReadDeadline(time.Time{})
	ic := d.accept()
	ic.send("NOTICE * :x")
	n, err := s.Read(buf)
	if err != nil || n == 0 {
		t.Fatalf("read after clearing deadline: %d %v", n, err)
	}
}

func TestAckIsAsyncAndMonotonic(t *testing.T) {
	d := newIRCd(t)
	te := startEngine(t, "")
	l := newLink(t, te.addr, 1)
	s, _, _ := registerSession(t, l, d)
	s.Ack(4)
	s.Ack(2) // never goes backwards
	waitUntil(t, "acked", func() bool { return te.srv.Stats().BufferedBytes == 0 })
}

func TestAckProbeFollowsDrainedReads(t *testing.T) {
	d := newIRCd(t)
	te := startEngine(t, "")
	l := newLink(t, te.addr, 1)
	s, err := l.Open(context.Background(), req(d))
	if err != nil {
		t.Fatal(err)
	}
	ic := d.accept()
	ic.send(":a NOTICE b :one")
	br := bufio.NewReader(s)
	line, _ := br.ReadString('\n')
	if !strings.Contains(line, "stugan/seq=1") {
		t.Fatalf("first read %q", line)
	}
	// Caught up: the next read is a probe vouching for seq 1, without
	// waiting for more traffic.
	probe, _ := br.ReadString('\n')
	if probe != AckProbe(1) {
		t.Fatalf("probe %q, want %q", probe, AckProbe(1))
	}
	// Nothing new delivered: no second probe; the read blocks.
	_ = s.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := br.ReadString('\n'); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("expected the read to block, got %v", err)
	}
}

func TestTagLine(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 6_000_000, time.UTC).UnixMilli()
	cases := []struct {
		in     string
		seq    uint64
		at     int64
		replay bool
		want   string
	}{
		{":a PRIVMSG b :c", 7, at, false, "@stugan/seq=7;time=2026-01-02T03:04:05.006Z :a PRIVMSG b :c"},
		{"@time=x;msgid=1 :a PRIVMSG b :c", 7, at, false, "@stugan/seq=7;time=x;msgid=1 :a PRIVMSG b :c"},
		{"@stugan/replay;stugan/seq=1;a=b :x 001 me :hi", 0, 0, true, "@stugan/replay;a=b :x 001 me :hi"},
		{"@stugan/x=1 :a NOTICE b :c", 0, 0, false, ":a NOTICE b :c"},
		{"PING :x", 0, 0, false, "PING :x"},
		{"@notags", 3, 0, false, "@notags"},
	}
	for _, tc := range cases {
		if got := TagLine(tc.in, tc.seq, tc.at, tc.replay); got != tc.want {
			t.Errorf("TagLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsPong(t *testing.T) {
	for line, want := range map[string]bool{
		"PONG :tok":             true,
		"PONG tok":              true,
		"pong server :tok":      true,
		"PONG server tok":       true,
		"PONG :tok2":            false,
		"PING :tok":             false,
		"PONG :tok extra words": false,
	} {
		if isPong(line, "tok") != want {
			t.Errorf("isPong(%q) != %v", line, want)
		}
	}
}

func TestLoadInstanceID(t *testing.T) {
	dir := t.TempDir()
	id, err := LoadInstanceID(dir)
	if err != nil || len(id) != 32 {
		t.Fatalf("id %q %v", id, err)
	}
	again, err := LoadInstanceID(dir)
	if err != nil || again != id {
		t.Fatalf("not stable: %q vs %q (%v)", again, id, err)
	}
	if err := os.WriteFile(filepath.Join(dir, instanceFile), []byte("not hex!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadInstanceID(dir); err == nil {
		t.Fatal("malformed id accepted")
	}
}

func TestNewValidates(t *testing.T) {
	for _, cfg := range []Config{
		{URL: "", Secret: "s", Instance: "i"},
		{URL: "h:1", Secret: "", Instance: "i"},
		{URL: "h:1", Secret: "s", Instance: ""},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("New(%+v) accepted", cfg)
		}
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
