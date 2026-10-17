package enginelink

import (
	"bytes"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klippelism/stugan/internal/engineproto"
)

// maxPending bounds bytes received from the engine but not yet read by the
// IRC client. The engine's own buffer is the real bound; this only stops a
// wedged client from growing memory without limit.
const maxPending = 32 << 20

// Info is what the engine reported when the connection opened.
type Info struct {
	// Attached: the engine already held a registered socket; the replay was
	// fed to the client and its registration commands are being swallowed.
	Attached bool
	// Unattended: registration finished while no stugan was attached, so the
	// post-registration steps (perform, autojoin) never ran.
	Unattended bool
	Nick       string
	Channels   []string
	// DetachedFor is how long no stugan process was attached.
	DetachedFor time.Duration
	// Backlog counts the buffered lines that follow the replay.
	Backlog int
	// Lost counts lines the engine dropped, never delivered, because its
	// buffer filled while detached.
	Lost uint64
	// Replay is how many replay lines were fed to the client.
	Replay int
}

// Session is one IRC connection relayed through the engine. It implements
// net.Conn: Read yields the server's lines (tagged, see TagLine), Write
// sends the client's lines.
type Session struct {
	link   *Link
	c      *conn
	id     string
	handle string
	marker string

	ready     chan struct{}
	readyOnce sync.Once
	openErr   error
	info      Info

	wake chan struct{}

	mu         sync.Mutex
	rbuf       bytes.Buffer
	rerr       error // set once the far side is gone; Read returns it after draining
	closed     bool  // Close or Terminate was called
	deadline   time.Time
	restoring  bool
	onRestored func()
	wpartial   []byte

	// delivered is the seq of the last line queued for the client; probed
	// the last one an ack probe was queued for. Both guarded by mu.
	delivered uint64
	probed    uint64

	ackPending atomic.Uint64
	ackSent    uint64 // guarded by the conn's ack loop
}

func newSession(l *Link, c *conn, id, handle string) *Session {
	return &Session{
		link: l, c: c, id: id, handle: handle,
		marker: "stugan-restore-" + handle,
		ready:  make(chan struct{}),
		wake:   make(chan struct{}, 1),
	}
}

// Info reports what the engine said at open.
func (s *Session) Info() Info { return s.info }

// Handle is this connect attempt's handle (for logs).
func (s *Session) Handle() string { return s.handle }

// Marker is the PING token that ends the restore of an attached session.
func (s *Session) Marker() string { return s.marker }

// OnRestored registers fn to run once the client has answered the restore
// marker — i.e. it has processed the whole replay and nothing it writes from
// now on is swallowed. It runs immediately on a fresh (non-attached) session.
func (s *Session) OnRestored(fn func()) {
	s.mu.Lock()
	if !s.restoring {
		s.mu.Unlock()
		fn()
		return
	}
	s.onRestored = fn
	s.mu.Unlock()
}

// Restoring reports whether the client's writes are still being swallowed.
func (s *Session) Restoring() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restoring
}

func (s *Session) opened(f *engineproto.Frame) {
	s.readyOnce.Do(func() {
		if f != nil {
			s.mu.Lock()
			s.info = Info{
				Attached:    true,
				Unattended:  f.Unattended,
				Nick:        f.Nick,
				Channels:    f.Channels,
				DetachedFor: time.Duration(f.DetachedMs) * time.Millisecond,
				Backlog:     f.Backlog,
				Lost:        f.Lost,
				Replay:      len(f.Replay),
			}
			for _, line := range f.Replay {
				s.rbuf.WriteString(TagLine(line, 0, 0, true))
				s.rbuf.WriteString("\r\n")
			}
			s.rbuf.WriteString("PING :" + s.marker + "\r\n")
			s.restoring = true
			s.mu.Unlock()
		}
		close(s.ready)
	})
}

// fail ends the session before or after it opened.
func (s *Session) fail(err error) {
	s.remoteClosed(err)
}

func (s *Session) remoteClosed(err error) {
	s.mu.Lock()
	if s.rerr == nil {
		s.rerr = err
	}
	s.mu.Unlock()
	s.signal()
	s.readyOnce.Do(func() {
		s.openErr = err
		close(s.ready)
	})
	s.link.forget(s)
}

func (s *Session) deliver(seq uint64, at int64, line string) {
	s.mu.Lock()
	if s.closed || s.rerr != nil {
		s.mu.Unlock()
		return
	}
	s.rbuf.WriteString(TagLine(line, seq, at, false))
	s.rbuf.WriteString("\r\n")
	s.delivered = max(s.delivered, seq)
	overflow := s.rbuf.Len() > maxPending
	s.mu.Unlock()
	if overflow {
		// The client stopped reading. Fail it; the engine keeps the socket and
		// everything unacked, so the reconnect re-attaches without loss.
		s.remoteClosed(errors.New("stugan-engine: client stopped reading"))
		s.detach()
		return
	}
	s.signal()
}

func (s *Session) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Read implements net.Conn.
func (s *Session) Read(p []byte) (int, error) {
	for {
		s.mu.Lock()
		if s.rbuf.Len() == 0 && s.delivered > s.probed && !s.closed {
			// The client has caught up: hand it an ack probe. When the probe
			// reaches its handlers, every line before it has been fully
			// handled — without waiting for the next real line to say so.
			s.probed = s.delivered
			s.rbuf.WriteString(AckProbe(s.probed))
		}
		if s.rbuf.Len() > 0 {
			n, _ := s.rbuf.Read(p)
			s.mu.Unlock()
			return n, nil
		}
		if s.closed {
			s.mu.Unlock()
			return 0, net.ErrClosed
		}
		if s.rerr != nil {
			err := s.rerr
			s.mu.Unlock()
			return 0, err
		}
		dl := s.deadline
		s.mu.Unlock()

		var timeout <-chan time.Time
		if !dl.IsZero() {
			d := time.Until(dl)
			if d <= 0 {
				return 0, os.ErrDeadlineExceeded
			}
			t := time.NewTimer(d)
			timeout = t.C
			select {
			case <-s.wake:
			case <-timeout:
			}
			t.Stop()
			continue
		}
		<-s.wake
	}
}

// Write implements net.Conn. Complete lines are relayed; while restoring they
// are swallowed (the server already has them), until the client's answer to
// the restore marker.
func (s *Session) Write(p []byte) (int, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, net.ErrClosed
	}
	if s.rerr != nil {
		err := s.rerr
		s.mu.Unlock()
		return 0, err
	}
	s.wpartial = append(s.wpartial, p...)
	var out []string
	var restored func()
	for {
		i := bytes.IndexByte(s.wpartial, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(s.wpartial[:i]), "\r")
		s.wpartial = s.wpartial[i+1:]
		if line == "" {
			continue
		}
		if s.restoring {
			if isPong(line, s.marker) {
				s.restoring = false
				restored, s.onRestored = s.onRestored, nil
			}
			continue
		}
		out = append(out, line)
	}
	if len(s.wpartial) == 0 {
		s.wpartial = nil
	}
	s.mu.Unlock()

	for _, line := range out {
		f := &engineproto.Frame{Op: engineproto.OpWrite, Handle: s.handle}
		f.SetLine(line)
		if err := s.c.send(f); err != nil {
			return 0, ErrLinkLost
		}
	}
	if restored != nil {
		restored()
	}
	return len(p), nil
}

// isPong reports whether line is a PONG whose final param is token.
func isPong(line, token string) bool {
	cmd, rest, _ := strings.Cut(line, " ")
	if !strings.EqualFold(cmd, "PONG") {
		return false
	}
	if i := strings.LastIndex(rest, " :"); i >= 0 {
		rest = rest[i+2:]
	} else {
		rest = strings.TrimPrefix(rest, ":")
		if j := strings.LastIndexByte(rest, ' '); j >= 0 {
			rest = rest[j+1:]
		}
	}
	return rest == token
}

// Close implements net.Conn as a DETACH: the engine keeps the IRC socket for
// the next connect. Use Terminate to end the IRC connection itself.
func (s *Session) Close() error {
	s.detach()
	return nil
}

func (s *Session) detach() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	gone := s.rerr != nil
	s.mu.Unlock()
	s.signal()
	s.link.forget(s)
	if !gone {
		_ = s.c.send(&engineproto.Frame{Op: engineproto.OpDetach, Handle: s.handle})
	}
}

// Terminate ends the IRC connection: the engine closes the socket. Safe to
// call after Close, as long as nothing re-claimed the connection since.
func (s *Session) Terminate() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.signal()
	s.link.forget(s)
	_ = s.c.send(&engineproto.Frame{Op: engineproto.OpClose, ID: s.id, Handle: s.handle})
}

// Ack tells the engine that every line through seq has been committed, so it
// may forget them. Never blocks: the conn's ack loop sends it.
func (s *Session) Ack(seq uint64) {
	for {
		cur := s.ackPending.Load()
		if seq <= cur {
			return
		}
		if s.ackPending.CompareAndSwap(cur, seq) {
			break
		}
	}
	s.c.queueAck(s)
}

// SetDeadline implements net.Conn (read side only; writes never block on
// the IRC server).
func (s *Session) SetDeadline(t time.Time) error { return s.SetReadDeadline(t) }

// SetReadDeadline implements net.Conn.
func (s *Session) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	s.deadline = t
	s.mu.Unlock()
	s.signal()
	return nil
}

// SetWriteDeadline implements net.Conn; writes are handed to the link and
// never wait on the IRC server, so there is nothing to bound.
func (s *Session) SetWriteDeadline(time.Time) error { return nil }

// LocalAddr implements net.Conn.
func (s *Session) LocalAddr() net.Addr { return engineAddr("stugan") }

// RemoteAddr implements net.Conn.
func (s *Session) RemoteAddr() net.Addr { return engineAddr("stugan-engine") }

type engineAddr string

func (a engineAddr) Network() string { return "stugan-engine" }
func (a engineAddr) String() string  { return string(a) }

var _ net.Conn = (*Session)(nil)
