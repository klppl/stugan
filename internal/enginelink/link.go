// Package enginelink is stugan's side of the stugan-engine link: one
// connection per stugan process, multiplexing every IRC connection the
// process holds through the engine.
//
// An IRC connection opened through the link is a *Session, which implements
// net.Conn so the IRC client (girc, in internal/irc) keeps a real, live
// client and none of its handlers know the socket is elsewhere. The one thing
// a Session does that a socket never would is RESTORE: when the engine
// answers a connect with `attached`, the IRC socket is already registered and
// the client is brand new, so the Session feeds it the engine's replay and
// swallows the registration commands the client sends in response, until the
// client answers a marker PING placed after the replay.
package enginelink

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/klippelism/stugan/internal/engineproto"
)

// Config configures a Link.
type Config struct {
	// URL is the engine address (host:port, tcp://host:port, unix:///path).
	URL    string
	Secret string
	// Instance identifies this stugan installation; see LoadInstanceID.
	Instance string
	Version  string
	Logger   *slog.Logger
	// Gen is this process's generation; defaults to the time New was called.
	Gen int64
	// Timeouts (defaults: 10s hello, 75s connect, 30s ping, 90s idle).
	HelloTimeout   time.Duration
	ConnectTimeout time.Duration
	PingInterval   time.Duration
	IdleTimeout    time.Duration
}

var (
	// ErrLinkLost reports that the link to the engine dropped. The IRC socket
	// is (probably) still held by the engine; reconnecting re-attaches.
	ErrLinkLost = errors.New("lost connection to stugan-engine")
	// ErrTakenOver reports that another connect claimed this connection.
	ErrTakenOver = errors.New("connection taken over")
	// ErrClosed is returned once the link has been shut down.
	ErrClosed = errors.New("engine link closed")
)

// RefusedError is the engine turning this process away at hello (wrong
// secret, protocol mismatch, …).
type RefusedError struct{ Msg string }

func (e *RefusedError) Error() string { return "stugan-engine refused the link: " + e.Msg }

// UpstreamError is the engine failing to reach the IRC server (DNS, TCP,
// TLS). The engine itself is fine.
type UpstreamError struct{ Msg string }

func (e *UpstreamError) Error() string { return e.Msg }

// Link is the process's connection to the engine. It dials lazily on the
// first Open and again after a drop.
type Link struct {
	cfg Config
	log *slog.Logger

	dialMu sync.Mutex // serializes (re)dialing

	mu       sync.Mutex
	cur      *conn // nil until dialed / after a drop
	closed   bool
	sessions map[string]*Session // by handle
}

// New builds a Link. It does not dial.
func New(cfg Config) (*Link, error) {
	if _, _, err := engineproto.ParseAddr(cfg.URL); err != nil {
		return nil, err
	}
	if cfg.Secret == "" {
		return nil, errors.New("enginelink: a secret is required")
	}
	if cfg.Instance == "" {
		return nil, errors.New("enginelink: an instance id is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Gen == 0 {
		cfg.Gen = time.Now().UnixNano()
	}
	if cfg.HelloTimeout <= 0 {
		cfg.HelloTimeout = 10 * time.Second
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 75 * time.Second
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = 30 * time.Second
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 90 * time.Second
	}
	return &Link{cfg: cfg, log: cfg.Logger, sessions: map[string]*Session{}}, nil
}

// ConnectRequest describes one IRC connection.
type ConnectRequest struct {
	ID          string // stable per (user, network) within the instance
	Addr        string // host:port to dial when the engine holds no socket
	TLS         bool
	Insecure    bool
	CertPEM     string
	Fingerprint string
}

// Open asks the engine to connect-or-attach req.ID and returns the session
// once the socket is open (fresh) or attached (held). On attach, Info reports
// what the engine kept.
func (l *Link) Open(ctx context.Context, req ConnectRequest) (*Session, error) {
	c, err := l.ensure(ctx)
	if err != nil {
		return nil, err
	}
	s := newSession(l, c, req.ID, newHandle())
	l.mu.Lock()
	if l.closed || l.cur != c {
		l.mu.Unlock()
		return nil, ErrLinkLost
	}
	l.sessions[s.handle] = s
	l.mu.Unlock()

	err = c.send(&engineproto.Frame{
		Op: engineproto.OpConnect, ID: req.ID, Handle: s.handle,
		Addr: req.Addr, TLS: req.TLS, Insecure: req.Insecure,
		CertPEM: req.CertPEM, Fingerprint: req.Fingerprint,
	})
	if err != nil {
		s.fail(ErrLinkLost)
		return nil, ErrLinkLost
	}

	t := time.NewTimer(l.cfg.ConnectTimeout)
	defer t.Stop()
	select {
	case <-s.ready:
		if s.openErr != nil {
			return nil, s.openErr
		}
		return s, nil
	case <-ctx.Done():
		// Usually shutdown: detach, never end, the socket — this may be the
		// re-attach that keeps IRC up. A fresh socket nobody claims is reaped
		// by the engine once it fails to register.
		s.detach()
		return nil, ctx.Err()
	case <-t.C:
		s.Terminate()
		return nil, fmt.Errorf("stugan-engine did not answer connect within %s", l.cfg.ConnectTimeout)
	}
}

// Close shuts the link down. The engine treats a closed link as a detach of
// every connection on it: the IRC sockets stay up for the next process.
func (l *Link) Close() {
	l.mu.Lock()
	l.closed = true
	c := l.cur
	l.cur = nil
	l.mu.Unlock()
	if c != nil {
		// Acks queued by the final commits of a shutdown go out first, so
		// the next process isn't handed lines this one already stored.
		c.flushAcks()
		c.shutdown(ErrClosed)
	}
}

// ensure returns the live link connection, dialing if needed.
func (l *Link) ensure(ctx context.Context) (*conn, error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, ErrClosed
	}
	if c := l.cur; c != nil {
		l.mu.Unlock()
		return c, nil
	}
	l.mu.Unlock()

	l.dialMu.Lock()
	defer l.dialMu.Unlock()
	l.mu.Lock()
	if c := l.cur; c != nil || l.closed {
		l.mu.Unlock()
		if c == nil {
			return nil, ErrClosed
		}
		return c, nil
	}
	l.mu.Unlock()

	c, err := l.dial(ctx)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		c.shutdown(ErrClosed)
		return nil, ErrClosed
	}
	l.cur = c
	l.mu.Unlock()
	go c.readLoop()
	go c.pingLoop()
	go c.ackLoop()
	return c, nil
}

func (l *Link) dial(ctx context.Context) (*conn, error) {
	network, addr, _ := engineproto.ParseAddr(l.cfg.URL)
	dctx, cancel := context.WithTimeout(ctx, l.cfg.HelloTimeout)
	defer cancel()
	var d net.Dialer
	nc, err := d.DialContext(dctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("stugan-engine unreachable at %s: %w", l.cfg.URL, err)
	}
	c := &conn{
		link: l, nc: nc, bw: bufio.NewWriter(nc), r: engineproto.NewReader(nc), done: make(chan struct{}),
		ackSet: map[*Session]struct{}{}, ackWake: make(chan struct{}, 1),
	}
	if err := c.send(&engineproto.Frame{
		Op: engineproto.OpHello, Protocol: engineproto.ProtocolMajor, Minor: engineproto.ProtocolMinor,
		Secret: l.cfg.Secret, Instance: l.cfg.Instance, Gen: l.cfg.Gen, Version: l.cfg.Version,
	}); err != nil {
		_ = nc.Close()
		return nil, fmt.Errorf("stugan-engine hello: %w", err)
	}
	deadline, _ := dctx.Deadline()
	_ = nc.SetReadDeadline(deadline)
	f, err := c.r.Next()
	if err != nil {
		_ = nc.Close()
		return nil, fmt.Errorf("stugan-engine hello: %w", err)
	}
	switch f.Op {
	case engineproto.OpHello:
		if f.Protocol != engineproto.ProtocolMajor {
			_ = nc.Close()
			return nil, &RefusedError{Msg: fmt.Sprintf("engine speaks protocol %d, stugan speaks %d", f.Protocol, engineproto.ProtocolMajor)}
		}
	case engineproto.OpError:
		_ = nc.Close()
		return nil, &RefusedError{Msg: f.Error}
	default:
		_ = nc.Close()
		return nil, fmt.Errorf("stugan-engine hello: unexpected %q", f.Op)
	}
	l.log.Info("connected to stugan-engine", "url", l.cfg.URL, "engine_version", f.Version, "minor", f.Minor)
	return c, nil
}

// drop forgets c as the current connection and fails its sessions.
func (l *Link) drop(c *conn, err error) {
	l.mu.Lock()
	if l.cur == c {
		l.cur = nil
	}
	var victims []*Session
	for h, s := range l.sessions {
		if s.c == c {
			victims = append(victims, s)
			delete(l.sessions, h)
		}
	}
	l.mu.Unlock()
	for _, s := range victims {
		s.fail(err)
	}
}

func (l *Link) session(handle string) *Session {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sessions[handle]
}

func (l *Link) forget(s *Session) {
	l.mu.Lock()
	if l.sessions[s.handle] == s {
		delete(l.sessions, s.handle)
	}
	l.mu.Unlock()
}

// conn is one dialed link connection.
type conn struct {
	link *Link
	nc   net.Conn
	r    *engineproto.Reader

	wmu sync.Mutex
	bw  *bufio.Writer

	once sync.Once
	done chan struct{}

	ackMu   sync.Mutex
	ackSet  map[*Session]struct{}
	ackWake chan struct{}
}

// queueAck marks s as having an ack to send and wakes the ack loop.
func (c *conn) queueAck(s *Session) {
	c.ackMu.Lock()
	c.ackSet[s] = struct{}{}
	c.ackMu.Unlock()
	select {
	case c.ackWake <- struct{}{}:
	default:
	}
}

// ackLoop sends queued acks off the caller's goroutine: Ack is called from
// the engine loop, which must never wait on the network.
func (c *conn) ackLoop() {
	for {
		select {
		case <-c.done:
			return
		case <-c.ackWake:
		}
		if !c.flushAcks() {
			return
		}
	}
}

// flushAcks sends every pending ack; false once the link is gone. ackMu is
// held throughout so the ack loop and Close never send the same session's
// acks concurrently.
func (c *conn) flushAcks() bool {
	c.ackMu.Lock()
	defer c.ackMu.Unlock()
	set := c.ackSet
	c.ackSet = map[*Session]struct{}{}
	for s := range set {
		seq := s.ackPending.Load()
		if seq <= s.ackSent {
			continue
		}
		if err := c.send(&engineproto.Frame{Op: engineproto.OpAck, ID: s.id, Handle: s.handle, Seq: seq}); err != nil {
			return false
		}
		s.ackSent = seq
	}
	return true
}

func (c *conn) send(f *engineproto.Frame) error {
	b, err := engineproto.Encode(f)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	select {
	case <-c.done:
		return ErrLinkLost
	default:
	}
	_ = c.nc.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if _, err := c.bw.Write(b); err != nil {
		go c.shutdown(ErrLinkLost)
		return err
	}
	if err := c.bw.Flush(); err != nil {
		go c.shutdown(ErrLinkLost)
		return err
	}
	return nil
}

func (c *conn) shutdown(err error) {
	c.once.Do(func() {
		close(c.done)
		_ = c.nc.Close()
		c.link.drop(c, err)
	})
}

func (c *conn) pingLoop() {
	t := time.NewTicker(c.link.cfg.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			_ = c.send(&engineproto.Frame{Op: engineproto.OpPing})
		}
	}
}

func (c *conn) readLoop() {
	defer c.shutdown(ErrLinkLost)
	for {
		_ = c.nc.SetReadDeadline(time.Now().Add(c.link.cfg.IdleTimeout))
		f, err := c.r.Next()
		if err != nil {
			select {
			case <-c.done:
			default:
				if !errors.Is(err, io.EOF) {
					c.link.log.Warn("stugan-engine link failed", "err", err)
				} else {
					c.link.log.Warn("stugan-engine closed the link")
				}
			}
			return
		}
		c.dispatch(f)
	}
}

func (c *conn) dispatch(f *engineproto.Frame) {
	switch f.Op {
	case engineproto.OpPing:
		_ = c.send(&engineproto.Frame{Op: engineproto.OpPong})
		return
	case engineproto.OpPong, engineproto.OpHello:
		return
	}
	if f.Handle == "" {
		if f.Op == engineproto.OpError {
			c.link.log.Warn("stugan-engine error", "err", f.Error)
		}
		return
	}
	s := c.link.session(f.Handle)
	if s == nil || s.c != c {
		return
	}
	switch f.Op {
	case engineproto.OpOpen:
		s.opened(nil)
	case engineproto.OpAttached:
		s.opened(f)
	case engineproto.OpLine:
		s.deliver(f.Seq, f.At, f.Text())
	case engineproto.OpClosed:
		msg := "connection closed by server"
		if f.Error != "" {
			msg = f.Error
		}
		s.remoteClosed(&UpstreamError{Msg: msg})
	case engineproto.OpDetached:
		s.remoteClosed(fmt.Errorf("%w: %s", ErrTakenOver, f.Error))
	case engineproto.OpError:
		s.remoteClosed(errors.New("stugan-engine: " + f.Error))
	}
}

func newHandle() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
