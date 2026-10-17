package engine

import (
	"bufio"
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/klippelism/stugan/internal/engineproto"
)

const (
	testSecret   = "0123456789abcdef-test-secret"
	testInstance = "aaaaaaaaaaaaaaaa"
	waitFor      = 5 * time.Second
)

// ircd is a scriptable fake IRC server.
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
	d := &ircd{t: t, ln: ln, conns: make(chan *ircConn, 16)}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			ic := &ircConn{t: t, c: c, lines: make(chan string, 1024), eof: make(chan struct{})}
			go func() {
				defer close(ic.eof)
				br := bufio.NewReader(c)
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					ic.lines <- strings.TrimRight(line, "\r\n")
				}
			}()
			d.conns <- ic
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return d
}

func (d *ircd) addr() string { return d.ln.Addr().String() }

func (d *ircd) accept() *ircConn {
	d.t.Helper()
	select {
	case c := <-d.conns:
		d.t.Cleanup(func() { _ = c.c.Close() })
		return c
	case <-time.After(waitFor):
		d.t.Fatal("ircd: no connection")
		return nil
	}
}

// noConn asserts that no new connection arrives for a short while.
func (d *ircd) noConn() {
	d.t.Helper()
	select {
	case <-d.conns:
		d.t.Fatal("ircd: unexpected new connection")
	case <-time.After(150 * time.Millisecond):
	}
}

func (c *ircConn) send(lines ...string) {
	c.t.Helper()
	for _, l := range lines {
		if _, err := io.WriteString(c.c, l+"\r\n"); err != nil {
			c.t.Fatalf("ircd send: %v", err)
		}
	}
}

// expect reads lines until one starts with prefix, failing after waitFor.
func (c *ircConn) expect(prefix string) string {
	c.t.Helper()
	deadline := time.After(waitFor)
	for {
		select {
		case l := <-c.lines:
			if strings.HasPrefix(l, prefix) {
				return l
			}
		case <-c.eof:
			c.t.Fatalf("ircd: connection closed while waiting for %q", prefix)
		case <-deadline:
			c.t.Fatalf("ircd: timed out waiting for %q", prefix)
		}
	}
}

// quiet asserts nothing is received for a short while.
func (c *ircConn) quiet() {
	c.t.Helper()
	select {
	case l := <-c.lines:
		c.t.Fatalf("ircd: unexpected line %q", l)
	case <-time.After(150 * time.Millisecond):
	}
}

func (c *ircConn) waitClosed() {
	c.t.Helper()
	select {
	case <-c.eof:
	case <-time.After(waitFor):
		c.t.Fatal("ircd: connection was not closed")
	}
}

// startEngine runs a Server on a loopback listener.
func startEngine(t *testing.T, opts Options) (*Server, string) {
	t.Helper()
	if opts.Secret == "" {
		opts.Secret = testSecret
	}
	srv, err := New(opts)
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
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(waitFor):
			t.Error("engine did not shut down")
		}
	})
	return srv, ln.Addr().String()
}

// tlink is a raw protocol client standing in for stugan.
type tlink struct {
	t      *testing.T
	c      net.Conn
	frames chan *engineproto.Frame
	eof    chan struct{}
}

func dialRaw(t *testing.T, addr string) *tlink {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	l := &tlink{t: t, c: c, frames: make(chan *engineproto.Frame, 4096), eof: make(chan struct{})}
	go func() {
		defer close(l.eof)
		r := engineproto.NewReader(c)
		for {
			f, err := r.Next()
			if err != nil {
				return
			}
			l.frames <- f
		}
	}()
	t.Cleanup(func() { _ = c.Close() })
	return l
}

// dialLink connects and completes hello.
func dialLink(t *testing.T, addr string, gen int64) *tlink {
	t.Helper()
	l := dialRaw(t, addr)
	l.send(&engineproto.Frame{
		Op: engineproto.OpHello, Protocol: engineproto.ProtocolMajor,
		Secret: testSecret, Instance: testInstance, Gen: gen, Version: "test",
	})
	if f := l.next(); f.Op != engineproto.OpHello {
		t.Fatalf("hello answered with %+v", f)
	}
	return l
}

func (l *tlink) send(f *engineproto.Frame) {
	l.t.Helper()
	b, err := engineproto.Encode(f)
	if err != nil {
		l.t.Fatal(err)
	}
	if _, err := l.c.Write(b); err != nil {
		l.t.Fatalf("link send: %v", err)
	}
}

// next returns the next frame, skipping pongs.
func (l *tlink) next() *engineproto.Frame {
	l.t.Helper()
	for {
		select {
		case f := <-l.frames:
			if f.Op == engineproto.OpPong {
				continue
			}
			return f
		case <-l.eof:
			l.t.Fatal("link closed")
			return nil
		case <-time.After(waitFor):
			l.t.Fatal("link: timed out waiting for a frame")
			return nil
		}
	}
}

// expect returns the next frame, which must have op.
func (l *tlink) expect(op string) *engineproto.Frame {
	l.t.Helper()
	f := l.next()
	if f.Op != op {
		l.t.Fatalf("expected %s frame, got %+v", op, f)
	}
	return f
}

// quiet asserts no frame (other than pong) arrives for a short while.
func (l *tlink) quiet() {
	l.t.Helper()
	for {
		select {
		case f := <-l.frames:
			if f.Op == engineproto.OpPong {
				continue
			}
			l.t.Fatalf("unexpected frame %+v", f)
		case <-time.After(150 * time.Millisecond):
			return
		}
	}
}

func (l *tlink) closeLink() {
	_ = l.c.Close()
	<-l.eof
}

// connect sends a connect frame for id.
func (l *tlink) connect(id, handle, addr, fp string) {
	l.send(&engineproto.Frame{Op: engineproto.OpConnect, ID: id, Handle: handle, Addr: addr, Fingerprint: fp})
}

func (l *tlink) write(handle, line string) {
	l.send(&engineproto.Frame{Op: engineproto.OpWrite, Handle: handle, Line: line})
}

// register drives a full registration over a fresh connection: the app writes
// NICK/USER, the server answers with a CAP exchange, welcome, ISUPPORT, MOTD.
func register(t *testing.T, l *tlink, ic *ircConn, handle, nick string) {
	t.Helper()
	l.write(handle, "CAP LS 302")
	l.write(handle, "NICK "+nick)
	l.write(handle, "USER u * * :real")
	ic.expect("CAP LS")
	ic.expect("NICK " + nick)
	ic.expect("USER ")
	ic.send(
		":irc.test CAP * LS :multi-prefix server-time",
		":irc.test CAP * ACK :multi-prefix server-time",
		":irc.test 001 "+nick+" :Welcome to the test network "+nick,
		":irc.test 002 "+nick+" :Your host is irc.test",
		":irc.test 005 "+nick+" PREFIX=(ov)@+ CHANTYPES=# :are supported",
		":irc.test 375 "+nick+" :- irc.test Message of the day -",
		":irc.test 372 "+nick+" :- hello",
		":irc.test 376 "+nick+" :End of /MOTD command.",
	)
}

// lines collects n line frames.
func (l *tlink) lines(n int) []*engineproto.Frame {
	l.t.Helper()
	out := make([]*engineproto.Frame, 0, n)
	for len(out) < n {
		f := l.next()
		if f.Op != engineproto.OpLine {
			l.t.Fatalf("expected line frame, got %+v", f)
		}
		out = append(out, f)
	}
	return out
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

func contextWithCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}
