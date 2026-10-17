package engine

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klippelism/stugan/internal/engineproto"
)

func TestHelloRefusals(t *testing.T) {
	_, addr := startEngine(t, Options{})

	cases := []struct {
		name string
		f    *engineproto.Frame
		want string
	}{
		{"bad secret", &engineproto.Frame{Op: engineproto.OpHello, Protocol: engineproto.ProtocolMajor, Secret: "nope", Instance: testInstance}, "wrong engine secret"},
		{"protocol", &engineproto.Frame{Op: engineproto.OpHello, Protocol: engineproto.ProtocolMajor + 1, Secret: testSecret, Instance: testInstance}, "protocol mismatch"},
		{"no instance", &engineproto.Frame{Op: engineproto.OpHello, Protocol: engineproto.ProtocolMajor, Secret: testSecret}, "instance"},
		{"not hello", &engineproto.Frame{Op: engineproto.OpConnect, ID: "x"}, "expected hello"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := dialRaw(t, addr)
			l.send(tc.f)
			f := l.expect(engineproto.OpError)
			if !strings.Contains(f.Error, tc.want) {
				t.Fatalf("error %q, want it to mention %q", f.Error, tc.want)
			}
			select {
			case <-l.eof:
			case <-time.After(waitFor):
				t.Fatal("link not closed after refusal")
			}
		})
	}
}

func TestHealthProbe(t *testing.T) {
	_, addr := startEngine(t, Options{Version: "v9"})
	l := dialRaw(t, addr)
	l.send(&engineproto.Frame{Op: engineproto.OpHealth})
	if f := l.expect(engineproto.OpHealth); f.Version != "v9" {
		t.Fatalf("health version %q", f.Version)
	}
}

func TestFreshConnectRelaysBothWays(t *testing.T) {
	d := newIRCd(t)
	_, addr := startEngine(t, Options{})
	l := dialLink(t, addr, 1)
	l.connect("net", "h1", d.addr(), "fp")
	l.expect(engineproto.OpOpen)
	ic := d.accept()

	register(t, l, ic, "h1", "alice")
	got := l.lines(8)
	for i, f := range got {
		if f.Seq != uint64(i+1) || f.Handle != "h1" || f.At == 0 {
			t.Fatalf("line %d: %+v", i, f)
		}
	}
	if !strings.Contains(got[2].Line, " 001 alice ") {
		t.Fatalf("third line %q, want the 001", got[2].Line)
	}

	ic.send(":bob!b@h PRIVMSG alice :hi")
	if f := l.expect(engineproto.OpLine); f.Line != ":bob!b@h PRIVMSG alice :hi" || f.Seq != 9 {
		t.Fatalf("relayed %+v", f)
	}
}

func TestPingAnsweredNotRelayed(t *testing.T) {
	d := newIRCd(t)
	_, addr := startEngine(t, Options{})
	l := dialLink(t, addr, 1)
	l.connect("net", "h1", d.addr(), "fp")
	l.expect(engineproto.OpOpen)
	ic := d.accept()

	ic.send("PING :token123")
	ic.expect("PONG :token123")
	ic.send(":irc.test NOTICE * :after")
	if f := l.expect(engineproto.OpLine); f.Line != ":irc.test NOTICE * :after" || f.Seq != 1 {
		t.Fatalf("got %+v; the PING must not be relayed or numbered", f)
	}
}

// setupRegistered returns a link + ircd connection for a registered session
// "net" (nick alice) in #chan, with every line acked.
func setupRegistered(t *testing.T, d *ircd, addr string) (*tlink, *ircConn) {
	t.Helper()
	l := dialLink(t, addr, 1)
	l.connect("net", "h1", d.addr(), "fp")
	l.expect(engineproto.OpOpen)
	ic := d.accept()
	register(t, l, ic, "h1", "alice")
	ic.send(":alice!u@host JOIN #chan", ":irc.test 353 alice = #chan :alice @bob", ":irc.test 366 alice #chan :End of /NAMES list.")
	frames := l.lines(11)
	l.send(&engineproto.Frame{Op: engineproto.OpAck, ID: "net", Handle: "h1", Seq: frames[len(frames)-1].Seq})
	return l, ic
}

func TestReattachReplaysStateAndBacklog(t *testing.T) {
	d := newIRCd(t)
	srv, addr := startEngine(t, Options{})
	l, ic := setupRegistered(t, d, addr)

	// A nick change and a cap change after registration.
	ic.send(":alice!u@host NICK :alice2", ":irc.test CAP alice2 ACK :away-notify")
	l.lines(2)
	l.send(&engineproto.Frame{Op: engineproto.OpAck, ID: "net", Handle: "h1", Seq: 13})

	l.closeLink() // stugan goes away
	waitUntil(t, "detach", func() bool { return srv.Stats().Attached == 0 })
	ic.send(":bob!b@h PRIVMSG #chan :while you were away", "PING :still-there")
	ic.expect("PONG :still-there") // answered while detached

	l2 := dialLink(t, addr, 2)
	l2.connect("net", "h2", d.addr(), "fp")
	at := l2.expect(engineproto.OpAttached)
	if at.Nick != "alice2" || !slices.Equal(at.Channels, []string{"#chan"}) || at.Backlog != 1 || at.Unattended {
		t.Fatalf("attached %+v", at)
	}
	if at.DetachedMs < 0 || at.Lost != 0 {
		t.Fatalf("attached %+v", at)
	}
	wantReplay := []string{
		":irc.test CAP * LS :multi-prefix server-time",
		":irc.test CAP * ACK :multi-prefix server-time",
		":irc.test 001 alice2 :Welcome to the test network alice",
		":irc.test 002 alice :Your host is irc.test",
		":irc.test 005 alice PREFIX=(ov)@+ CHANTYPES=# :are supported",
		":irc.test CAP alice2 ACK :away-notify",
	}
	if !slices.Equal(at.Replay, wantReplay) {
		t.Fatalf("replay:\n%s\nwant:\n%s", strings.Join(at.Replay, "\n"), strings.Join(wantReplay, "\n"))
	}
	f := l2.expect(engineproto.OpLine)
	if f.Line != ":bob!b@h PRIVMSG #chan :while you were away" || f.Seq != 14 || f.Handle != "h2" {
		t.Fatalf("backlog %+v", f)
	}
	l2.quiet()
	d.noConn() // re-attached, not re-dialed
	ic.quiet() // and nothing re-registered

	// The re-attached handle writes through.
	l2.write("h2", "PRIVMSG #chan :back")
	ic.expect("PRIVMSG #chan :back")
	// The old handle can't.
	l2.write("h1", "PRIVMSG #chan :ghost")
	ic.quiet()
}

func TestReattachRedeliversUnackedLines(t *testing.T) {
	d := newIRCd(t)
	_, addr := startEngine(t, Options{})
	l, ic := setupRegistered(t, d, addr)
	ic.send(":bob!b@h PRIVMSG #chan :one", ":bob!b@h PRIVMSG #chan :two")
	l.lines(2) // delivered but never acked
	l.closeLink()

	l2 := dialLink(t, addr, 2)
	l2.connect("net", "h2", d.addr(), "fp")
	if at := l2.expect(engineproto.OpAttached); at.Backlog != 2 {
		t.Fatalf("backlog %d, want the 2 unacked lines", at.Backlog)
	}
	got := l2.lines(2)
	if got[0].Seq != 12 || got[1].Seq != 13 {
		t.Fatalf("redelivered seqs %d,%d", got[0].Seq, got[1].Seq)
	}
}

func TestBurstNotInBacklogWhenUnacked(t *testing.T) {
	// The app died right after registering, before acking anything: the
	// replay already covers the burst, so its lines (MOTD included) are not
	// re-sent as backlog — that would make the client register twice.
	d := newIRCd(t)
	_, addr := startEngine(t, Options{})
	l := dialLink(t, addr, 1)
	l.connect("net", "h1", d.addr(), "fp")
	l.expect(engineproto.OpOpen)
	ic := d.accept()
	register(t, l, ic, "h1", "alice")
	l.lines(8)
	ic.send(":bob!b@h PRIVMSG alice :after burst")
	l.lines(1)
	l.closeLink()

	l2 := dialLink(t, addr, 2)
	l2.connect("net", "h2", d.addr(), "fp")
	if at := l2.expect(engineproto.OpAttached); at.Backlog != 1 {
		t.Fatalf("backlog %d, want only the post-burst line", at.Backlog)
	}
	if f := l2.expect(engineproto.OpLine); f.Seq != 9 {
		t.Fatalf("backlog line %+v", f)
	}
}

func TestFingerprintMismatchRedials(t *testing.T) {
	d := newIRCd(t)
	_, addr := startEngine(t, Options{})
	l, ic := setupRegistered(t, d, addr)

	l.connect("net", "h2", d.addr(), "edited")
	ic.waitClosed()
	if f := l.expect(engineproto.OpDetached); f.Handle != "h1" {
		t.Fatalf("old handle told %+v", f)
	}
	l.expect(engineproto.OpOpen)
	d.accept()
}

func TestStillRegisteringIsRedialed(t *testing.T) {
	d := newIRCd(t)
	_, addr := startEngine(t, Options{})
	l := dialLink(t, addr, 1)
	l.connect("net", "h1", d.addr(), "fp")
	l.expect(engineproto.OpOpen)
	ic := d.accept()
	l.write("h1", "NICK alice")
	ic.expect("NICK alice")
	l.closeLink()

	l2 := dialLink(t, addr, 2)
	l2.connect("net", "h2", d.addr(), "fp")
	l2.expect(engineproto.OpOpen)
	ic.waitClosed()
	d.accept()
}

func TestUnattendedRegistration(t *testing.T) {
	d := newIRCd(t)
	srv, addr := startEngine(t, Options{})
	l := dialLink(t, addr, 1)
	l.connect("net", "h1", d.addr(), "fp")
	l.expect(engineproto.OpOpen)
	ic := d.accept()
	l.write("h1", "NICK alice")
	ic.expect("NICK alice")
	l.closeLink()
	waitUntil(t, "detach", func() bool { return srv.Stats().Attached == 0 })
	// The welcome lands with nobody attached.
	ic.send(":irc.test 001 alice :Welcome", ":irc.test 422 alice :No MOTD")
	waitUntil(t, "registration", func() bool {
		s := srv.lookup(&link{instance: testInstance}, "net")
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.registered && !s.burstOpen
	})

	l2 := dialLink(t, addr, 2)
	l2.connect("net", "h2", d.addr(), "fp")
	if at := l2.expect(engineproto.OpAttached); !at.Unattended {
		t.Fatalf("attached %+v, want unattended", at)
	}
	l2.closeLink()
	// Once an app has attached, it is attended: the next attach must not ask
	// for perform/autojoin again.
	l3 := dialLink(t, addr, 3)
	l3.connect("net", "h3", d.addr(), "fp")
	if at := l3.expect(engineproto.OpAttached); at.Unattended {
		t.Fatalf("second attach still unattended")
	}
}

func TestTakeoverByNewerAndRefusalOfOlder(t *testing.T) {
	d := newIRCd(t)
	_, addr := startEngine(t, Options{})
	old, ic := setupRegistered(t, d, addr)

	newer := dialLink(t, addr, 5)
	newer.connect("net", "h2", d.addr(), "fp")
	newer.expect(engineproto.OpAttached)
	if f := old.expect(engineproto.OpDetached); f.Handle != "h1" {
		t.Fatalf("old link told %+v", f)
	}

	// The older process tries to take it back and is refused.
	old.connect("net", "h3", d.addr(), "fp")
	if f := old.expect(engineproto.OpError); f.Handle != "h3" || !strings.Contains(f.Error, "newer") {
		t.Fatalf("refusal %+v", f)
	}
	// The newer link still owns it.
	ic.send(":bob!b@h PRIVMSG #chan :still yours")
	if f := newer.expect(engineproto.OpLine); f.Handle != "h2" {
		t.Fatalf("line went to %+v", f)
	}
	// A stale close from the superseded handle is ignored.
	old.send(&engineproto.Frame{Op: engineproto.OpClose, ID: "net", Handle: "h1"})
	ic.quiet()
	newer.write("h2", "PRIVMSG #chan :alive")
	ic.expect("PRIVMSG #chan :alive")
}

func TestCloseEndsSocket(t *testing.T) {
	d := newIRCd(t)
	srv, addr := startEngine(t, Options{})
	l, ic := setupRegistered(t, d, addr)
	l.send(&engineproto.Frame{Op: engineproto.OpClose, ID: "net", Handle: "h1"})
	ic.waitClosed()
	waitUntil(t, "session forgotten", func() bool { return srv.Stats().Sessions == 0 })
	l.quiet() // the app asked; no closed frame
}

func TestCloseAfterDetach(t *testing.T) {
	// Close right after a detach (core cancels the connection, then closes
	// it): the last handle may still end the unclaimed socket.
	d := newIRCd(t)
	srv, addr := startEngine(t, Options{})
	l, ic := setupRegistered(t, d, addr)
	l.send(&engineproto.Frame{Op: engineproto.OpDetach, Handle: "h1"})
	l.send(&engineproto.Frame{Op: engineproto.OpClose, ID: "net", Handle: "h1"})
	ic.waitClosed()
	waitUntil(t, "session forgotten", func() bool { return srv.Stats().Sessions == 0 })
}

func TestUpstreamCloseDeliversLinesThenClosed(t *testing.T) {
	d := newIRCd(t)
	srv, addr := startEngine(t, Options{})
	l, ic := setupRegistered(t, d, addr)
	ic.send("ERROR :Closing Link: bye")
	_ = ic.c.Close()
	if f := l.expect(engineproto.OpLine); !strings.HasPrefix(f.Line, "ERROR") {
		t.Fatalf("got %+v", f)
	}
	if f := l.expect(engineproto.OpClosed); f.Handle != "h1" || f.Error == "" {
		t.Fatalf("closed %+v", f)
	}
	waitUntil(t, "session forgotten", func() bool { return srv.Stats().Sessions == 0 })
}

func TestUpstreamCloseWhileDetachedForgets(t *testing.T) {
	d := newIRCd(t)
	srv, addr := startEngine(t, Options{})
	l, ic := setupRegistered(t, d, addr)
	l.closeLink()
	waitUntil(t, "detach", func() bool { return srv.Stats().Attached == 0 })
	_ = ic.c.Close()
	waitUntil(t, "session forgotten", func() bool { return srv.Stats().Sessions == 0 })
	if used := srv.Stats().BufferedBytes; used != 0 {
		t.Fatalf("budget leaked %d bytes", used)
	}
	// A new connect dials fresh.
	l2 := dialLink(t, addr, 2)
	l2.connect("net", "h2", d.addr(), "fp")
	l2.expect(engineproto.OpOpen)
}

func TestDialFailureReportsClosed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	_ = ln.Close()

	srv, addr := startEngine(t, Options{})
	l := dialLink(t, addr, 1)
	l.connect("net", "h1", dead, "fp")
	if f := l.expect(engineproto.OpClosed); f.Handle != "h1" || f.Error == "" {
		t.Fatalf("closed %+v", f)
	}
	waitUntil(t, "session forgotten", func() bool { return srv.Stats().Sessions == 0 })
}

func TestConnectValidation(t *testing.T) {
	_, addr := startEngine(t, Options{})
	l := dialLink(t, addr, 1)
	l.connect("", "h1", "x:1", "fp")
	l.expect(engineproto.OpError)
	l.connect("net", "h2", "no-port", "fp")
	l.expect(engineproto.OpError)
}

func TestBufferOverflowReportsLost(t *testing.T) {
	d := newIRCd(t)
	srv, addr := startEngine(t, Options{BufferBytes: MinBufferBytes})
	l, ic := setupRegistered(t, d, addr)
	l.closeLink()
	waitUntil(t, "detach", func() bool { return srv.Stats().Attached == 0 })

	line := ":bob!b@h PRIVMSG #chan :" + strings.Repeat("x", 400)
	const n = 400 // ~170 KiB against a 64 KiB cap
	for range n {
		ic.send(line)
	}
	ic.send(":bob!b@h PRIVMSG #chan :last")
	waitUntil(t, "all lines ingested", func() bool {
		s := srv.lookup(&link{instance: testInstance}, "net")
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.seq == 11+n+1
	})
	if used := srv.Stats().BufferedBytes; used > MinBufferBytes {
		t.Fatalf("buffer holds %d bytes, cap %d", used, MinBufferBytes)
	}

	l2 := dialLink(t, addr, 2)
	l2.connect("net", "h2", d.addr(), "fp")
	at := l2.expect(engineproto.OpAttached)
	if at.Lost == 0 || uint64(at.Backlog)+at.Lost != n+1 {
		t.Fatalf("backlog %d + lost %d, want %d", at.Backlog, at.Lost, n+1)
	}
	got := l2.lines(at.Backlog)
	if last := got[len(got)-1]; last.Line != ":bob!b@h PRIVMSG #chan :last" {
		t.Fatalf("newest line must survive, got %q", last.Line)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Seq != got[i-1].Seq+1 {
			t.Fatalf("backlog not contiguous at %d", i)
		}
	}
}

func TestGlobalBudgetShedsLargest(t *testing.T) {
	d := newIRCd(t)
	srv, addr := startEngine(t, Options{BufferBytes: 1 << 20, TotalBufferBytes: MinBufferBytes})
	l := dialLink(t, addr, 1)
	l.connect("a", "ha", d.addr(), "fp")
	l.expect(engineproto.OpOpen)
	icA := d.accept()
	l.connect("b", "hb", d.addr(), "fp")
	l.expect(engineproto.OpOpen)
	icB := d.accept()
	l.closeLink()
	waitUntil(t, "detach", func() bool { return srv.Stats().Attached == 0 })

	icB.send(":x PRIVMSG y :quiet one")
	big := ":x PRIVMSG y :" + strings.Repeat("z", 1000)
	for range 200 {
		icA.send(big)
	}
	waitUntil(t, "ingest", func() bool {
		s := srv.lookup(&link{instance: testInstance}, "a")
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.seq == 200
	})
	waitUntil(t, "budget", func() bool { return srv.Stats().BufferedBytes <= MinBufferBytes })
	sb := srv.lookup(&link{instance: testInstance}, "b")
	sb.mu.Lock()
	n := sb.buf.len()
	sb.mu.Unlock()
	if n != 1 {
		t.Fatalf("quiet session lost its line to the hoarder (has %d)", n)
	}
}

func TestOrphanReaped(t *testing.T) {
	d := newIRCd(t)
	srv, addr := startEngine(t, Options{OrphanTimeout: time.Hour})
	l, ic := setupRegistered(t, d, addr)
	l.closeLink()
	waitUntil(t, "detach", func() bool { return srv.Stats().Attached == 0 })

	srv.reap(time.Now().Add(30 * time.Minute))
	ic.quiet()
	srv.reap(time.Now().Add(2 * time.Hour))
	ic.waitClosed()
	waitUntil(t, "forgotten", func() bool { return srv.Stats().Sessions == 0 })
}

func TestOrphanReapingDisabled(t *testing.T) {
	d := newIRCd(t)
	srv, addr := startEngine(t, Options{OrphanTimeout: -1})
	l, ic := setupRegistered(t, d, addr)
	l.closeLink()
	waitUntil(t, "detach", func() bool { return srv.Stats().Attached == 0 })
	srv.reap(time.Now().Add(1000 * time.Hour))
	ic.quiet()
	if srv.Stats().Sessions != 1 {
		t.Fatal("session reaped with reaping disabled")
	}
}

func TestUnregisteredOrphanReaped(t *testing.T) {
	d := newIRCd(t)
	srv, addr := startEngine(t, Options{OrphanTimeout: -1})
	l := dialLink(t, addr, 1)
	l.connect("net", "h1", d.addr(), "fp")
	l.expect(engineproto.OpOpen)
	ic := d.accept()
	l.closeLink()
	waitUntil(t, "detach", func() bool { return srv.Stats().Attached == 0 })
	srv.reap(time.Now().Add(3 * time.Minute))
	ic.waitClosed()
}

func TestAckFromRecentHandle(t *testing.T) {
	d := newIRCd(t)
	srv, addr := startEngine(t, Options{})
	l, ic := setupRegistered(t, d, addr)
	ic.send(":b PRIVMSG #chan :x")
	l.lines(1)
	l.closeLink()

	l2 := dialLink(t, addr, 2)
	l2.connect("net", "h2", d.addr(), "fp")
	l2.expect(engineproto.OpAttached)
	l2.lines(1)
	// h1 committed seq 12 before it went away; its late ack still counts.
	l2.send(&engineproto.Frame{Op: engineproto.OpAck, ID: "net", Handle: "h1", Seq: 12})
	// A made-up handle's ack does not.
	l2.send(&engineproto.Frame{Op: engineproto.OpAck, ID: "net", Handle: "nope", Seq: 12})
	waitUntil(t, "ack applied", func() bool {
		s := srv.lookup(&link{instance: testInstance}, "net")
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.acked == 12 && s.buf.len() == 0
	})
}

func TestInstancesArePartitioned(t *testing.T) {
	d := newIRCd(t)
	_, addr := startEngine(t, Options{})
	l, ic := setupRegistered(t, d, addr)

	other := dialRaw(t, addr)
	other.send(&engineproto.Frame{Op: engineproto.OpHello, Protocol: engineproto.ProtocolMajor, Secret: testSecret, Instance: "bbbbbbbbbbbbbbbb", Gen: 9})
	other.expect(engineproto.OpHello)
	// Same id, other instance: a separate connection, not an attach.
	other.connect("net", "x1", d.addr(), "fp")
	other.expect(engineproto.OpOpen)
	d.accept()
	// And it can't touch the first instance's socket.
	other.send(&engineproto.Frame{Op: engineproto.OpClose, ID: "net", Handle: "h1"})
	other.write("h1", "QUIT :pwned")
	ic.quiet()
	l.write("h1", "PRIVMSG #chan :fine")
	ic.expect("PRIVMSG #chan :fine")
}

func TestDuplicateHandleRefused(t *testing.T) {
	d := newIRCd(t)
	_, addr := startEngine(t, Options{})
	l := dialLink(t, addr, 1)
	l.connect("a", "same", d.addr(), "fp")
	l.expect(engineproto.OpOpen)
	l.connect("b", "same", d.addr(), "fp")
	if f := l.next(); f.Op != engineproto.OpError {
		t.Fatalf("got %+v", f)
	}
}

func TestIdleProbe(t *testing.T) {
	d := newIRCd(t)
	srv, addr := startEngine(t, Options{IdleTimeout: 100 * time.Millisecond, ProbeTimeout: 200 * time.Millisecond})
	l := dialLink(t, addr, 1)
	l.connect("net", "h1", d.addr(), "fp")
	l.expect(engineproto.OpOpen)
	ic := d.accept()

	ic.expect("PING :" + probeToken)
	ic.send(":irc.test PONG irc.test :" + probeToken)
	l.quiet() // the probe's PONG is not relayed
	// Next idle spell: no answer this time.
	ic.expect("PING :" + probeToken)
	if f := l.expect(engineproto.OpClosed); f.Error != "ping timeout" {
		t.Fatalf("closed %+v", f)
	}
	ic.waitClosed()
	waitUntil(t, "forgotten", func() bool { return srv.Stats().Sessions == 0 })
}

func TestOverlongLineEndsSession(t *testing.T) {
	d := newIRCd(t)
	_, addr := startEngine(t, Options{})
	l := dialLink(t, addr, 1)
	l.connect("net", "h1", d.addr(), "fp")
	l.expect(engineproto.OpOpen)
	ic := d.accept()
	ic.send(strings.Repeat("a", engineproto.MaxLineBytes+10))
	if f := l.expect(engineproto.OpClosed); !strings.Contains(f.Error, "longer than") {
		t.Fatalf("closed %+v", f)
	}
}

func TestInvalidWritesDropped(t *testing.T) {
	d := newIRCd(t)
	_, addr := startEngine(t, Options{})
	l := dialLink(t, addr, 1)
	l.connect("net", "h1", d.addr(), "fp")
	l.expect(engineproto.OpOpen)
	ic := d.accept()
	l.write("h1", "PRIVMSG #a :x\r\nQUIT :smuggled")
	l.write("h1", "")
	ic.quiet()
	l.write("h1", "PRIVMSG #a :ok")
	ic.expect("PRIVMSG #a :ok")
}

func TestShutdownClosesSockets(t *testing.T) {
	d := newIRCd(t)
	srv, err := New(Options{Secret: testSecret})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := contextWithCancel()
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ctx, ln) }()
	l := dialLink(t, ln.Addr().String(), 1)
	l.connect("net", "h1", d.addr(), "fp")
	l.expect(engineproto.OpOpen)
	ic := d.accept()
	cancel()
	ic.waitClosed()
	select {
	case <-done:
	case <-time.After(waitFor):
		t.Fatal("Serve did not return")
	}
}

func TestTLSUpstream(t *testing.T) {
	cert := selfSigned(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _ = c.Write([]byte(":tls.test NOTICE * :secure hello\r\n"))
				buf := make([]byte, 1)
				_, _ = c.Read(buf)
			}()
		}
	}()
	_, addr := startEngine(t, Options{})
	l := dialLink(t, addr, 1)

	// Self-signed without opting in: verification fails.
	l.send(&engineproto.Frame{Op: engineproto.OpConnect, ID: "v", Handle: "hv", Addr: ln.Addr().String(), TLS: true})
	if f := l.expect(engineproto.OpClosed); !strings.Contains(f.Error, "certificate") {
		t.Fatalf("closed %+v", f)
	}
	// Opted in: works.
	l.send(&engineproto.Frame{Op: engineproto.OpConnect, ID: "i", Handle: "hi", Addr: ln.Addr().String(), TLS: true, Insecure: true})
	l.expect(engineproto.OpOpen)
	if f := l.expect(engineproto.OpLine); f.Line != ":tls.test NOTICE * :secure hello" {
		t.Fatalf("line %+v", f)
	}
	// A malformed client certificate fails this connection only.
	l.send(&engineproto.Frame{Op: engineproto.OpConnect, ID: "c", Handle: "hc", Addr: ln.Addr().String(), TLS: true, Insecure: true, CertPEM: "garbage"})
	if f := l.expect(engineproto.OpClosed); !strings.Contains(f.Error, "client certificate") {
		t.Fatalf("closed %+v", f)
	}
}

func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "irc.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestParseLine(t *testing.T) {
	cases := []struct {
		in     string
		nick   string
		cmd    string
		params []string
	}{
		{":nick!u@h PRIVMSG #c :hello there", "nick", "PRIVMSG", []string{"#c", "hello there"}},
		{"@time=x;msgid=y :srv 001 me :Welcome", "srv", "001", []string{"me", "Welcome"}},
		{"PING :abc", "", "PING", []string{"abc"}},
		{"ping abc", "", "PING", []string{"abc"}},
		{":n JOIN #a", "n", "JOIN", []string{"#a"}},
		{":n  KICK  #a  victim :why", "n", "KICK", []string{"#a", "victim", "why"}},
		{":n PART #a :", "n", "PART", []string{"#a", ""}},
	}
	for _, tc := range cases {
		l, ok := parseLine(tc.in)
		if !ok || l.nick != tc.nick || l.cmd != tc.cmd || !slices.Equal(l.params, tc.params) {
			t.Errorf("parseLine(%q) = %+v, %v", tc.in, l, ok)
		}
	}
	for _, bad := range []string{"", ":onlyprefix", "@tagsonly", "@t :p", "@ PRIVMSG x :y", ": PRIVMSG x :y"} {
		if _, ok := parseLine(bad); ok {
			t.Errorf("parseLine(%q) ok", bad)
		}
	}
}

func TestRewriteTarget(t *testing.T) {
	cases := map[string]string{
		":srv 001 old :Welcome old":         ":srv 001 new :Welcome old",
		"@a=b :srv 001 old :Welcome":        "@a=b :srv 001 new :Welcome",
		":srv 001 old":                      ":srv 001 new",
		":srv 001 :trailing-only":           ":srv 001 :trailing-only",
		"001 old extra params :and trailer": "001 new extra params :and trailer",
	}
	for in, want := range cases {
		if got := rewriteTarget(in, "new"); got != want {
			t.Errorf("rewriteTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFold(t *testing.T) {
	if fold("Nick[]\\~") != "nick{}|^" {
		t.Fatalf("fold = %q", fold("Nick[]\\~"))
	}
}

func TestChannelTracking(t *testing.T) {
	d := newIRCd(t)
	srv, addr := startEngine(t, Options{})
	l, ic := setupRegistered(t, d, addr)
	ic.send(
		":alice!u@host JOIN #two",
		":alice!u@host JOIN :#Three",
		":bob!b@h JOIN #chan",            // someone else: no change
		":alice!u@host PART #chan :bye",  // left
		":op!o@h KICK #two alice :out",   // kicked
		":op!o@h KICK #Three bob :other", // someone else kicked
	)
	l.lines(6)
	s := srv.lookup(&link{instance: testInstance}, "net")
	waitUntil(t, "tracking", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.seq == 17
	})
	s.mu.Lock()
	got := slices.Clone(s.channels)
	s.mu.Unlock()
	if !slices.Equal(got, []string{"#Three"}) {
		t.Fatalf("channels %v", got)
	}
}

func TestUnparseableLinesNeverEnterTheStream(t *testing.T) {
	// girc drops a connection over a line it can't parse; relayed, the
	// re-attach would be handed the same unacked line forever.
	d := newIRCd(t)
	_, addr := startEngine(t, Options{})
	l := dialLink(t, addr, 1)
	l.connect("net", "h1", d.addr(), "fp")
	l.expect(engineproto.OpOpen)
	ic := d.accept()
	ic.send(":bare-prefix", "@ PRIVMSG x :y", ": PRIVMSG x :y", "@onlytags", "A", "")
	ic.send(":irc.test NOTICE * :fine")
	if f := l.expect(engineproto.OpLine); f.Line != ":irc.test NOTICE * :fine" || f.Seq != 1 {
		t.Fatalf("got %+v", f)
	}
}

func TestNonUTF8LinesRelayedExactly(t *testing.T) {
	d := newIRCd(t)
	_, addr := startEngine(t, Options{})
	l := dialLink(t, addr, 1)
	l.connect("net", "h1", d.addr(), "fp")
	l.expect(engineproto.OpOpen)
	ic := d.accept()
	latin1 := ":bob!b@h PRIVMSG #c :caf\xe9 r\xe4ksm\xf6rg\xe5s"
	ic.send(latin1)
	f := l.expect(engineproto.OpLine)
	if f.Text() != latin1 || f.Line != "" || f.Bin == nil {
		t.Fatalf("line %q (bin %v), want the exact bytes", f.Text(), f.Bin != nil)
	}
	out := &engineproto.Frame{Op: engineproto.OpWrite, Handle: "h1"}
	out.SetLine("PRIVMSG #c :h\xe4j")
	l.send(out)
	ic.expect("PRIVMSG #c :h\xe4j")
}

func TestLineReaderSurvivesTimeoutMidLine(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	lr := &lineReader{br: bufio.NewReader(a)}
	go func() { _, _ = b.Write([]byte(":srv NOTICE * :first ha")) }()
	_ = a.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := lr.next(engineproto.MaxLineBytes); err == nil {
		t.Fatal("expected a timeout")
	}
	go func() { _, _ = b.Write([]byte("lf\r\n:srv NOTICE * :second\n")) }()
	_ = a.SetReadDeadline(time.Now().Add(waitFor))
	for _, want := range []string{":srv NOTICE * :first half", ":srv NOTICE * :second"} {
		got, err := lr.next(engineproto.MaxLineBytes)
		if err != nil || got != want {
			t.Fatalf("got %q %v, want %q", got, err, want)
		}
	}
}
