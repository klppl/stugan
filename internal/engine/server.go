// Package engine is stugan-engine: an optional sidecar process that holds IRC
// sockets so they survive stugan restarts and upgrades.
//
// stugan connects to the engine over one link per process (see
// internal/engineproto) and asks it to connect-or-attach each network. A fresh
// connect dials the IRC server; while stugan is attached, raw lines flow both
// ways. When stugan goes away the engine keeps the socket registered, answers
// PINGs and buffers what arrives. The next stugan process re-attaches: it gets
// a replay of the registration burst so its fresh IRC client believes it just
// registered, then every line it has not acknowledged.
//
// The engine deliberately understands as little IRC as possible (see
// session), so it rarely needs to change — and so rarely needs restarting,
// which is the only thing that still drops IRC.
package engine

import (
	"bufio"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/klippelism/stugan/internal/engineproto"
)

// Options configures the engine.
type Options struct {
	// Secret authenticates stugan to the engine. Required.
	Secret  string
	Version string
	Logger  *slog.Logger

	// BufferBytes caps one connection's backlog (default 4 MiB);
	// TotalBufferBytes caps the sum across connections (default 256 MiB).
	// Past either, the oldest lines go and the app is told how many.
	BufferBytes      int64
	TotalBufferBytes int64
	// OrphanTimeout ends a socket no stugan process has claimed for this
	// long (default 1h). Negative disables reaping.
	OrphanTimeout time.Duration
	// DialTimeout bounds TCP connect + TLS handshake (default 60s).
	DialTimeout time.Duration
	// IdleTimeout is how long an IRC socket may stay silent before the
	// engine probes it with a PING (default 90s); ProbeTimeout is how long the
	// probe may go unanswered before the socket is given up (default 60s).
	IdleTimeout  time.Duration
	ProbeTimeout time.Duration
	// HelloTimeout bounds the handshake on a new link (default 10s);
	// LinkTimeout ends a link that sends nothing (stugan pings every 30s)
	// for this long (default 2m).
	HelloTimeout time.Duration
	LinkTimeout  time.Duration

	// Dial overrides the upstream dialer (tests).
	Dial func(ctx context.Context, addr string) (net.Conn, error)
}

const (
	defaultBufferBytes      = 4 << 20
	defaultTotalBufferBytes = 256 << 20
	// MinBufferBytes is the smallest accepted per-connection or total cap.
	MinBufferBytes = 64 << 10
	// unregisteredTimeout ends an unclaimed socket that never registered:
	// its app died mid-handshake and a re-attach would dial fresh anyway.
	unregisteredTimeout = 2 * time.Minute
	// pullBatch is how many lines one session contributes per writer pass,
	// so one huge backlog can't starve the rest of a link.
	pullBatch = 256
)

func (o *Options) defaults() {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.BufferBytes <= 0 {
		o.BufferBytes = defaultBufferBytes
	}
	if o.TotalBufferBytes <= 0 {
		o.TotalBufferBytes = defaultTotalBufferBytes
	}
	if o.OrphanTimeout == 0 {
		o.OrphanTimeout = time.Hour
	}
	if o.DialTimeout <= 0 {
		o.DialTimeout = 60 * time.Second
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = 90 * time.Second
	}
	if o.ProbeTimeout <= 0 {
		o.ProbeTimeout = 60 * time.Second
	}
	if o.HelloTimeout <= 0 {
		o.HelloTimeout = 10 * time.Second
	}
	if o.LinkTimeout <= 0 {
		o.LinkTimeout = 2 * time.Minute
	}
}

// Server is the engine. Create with New, run with Serve.
type Server struct {
	opts   Options
	log    *slog.Logger
	budget *budget
	dialFn func(ctx context.Context, addr string) (net.Conn, error)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	closed   bool
	sessions map[string]*session // instance + "\x00" + id
	byHandle map[string]*session // current handle → session
	links    map[*link]struct{}
}

// New builds an engine.
func New(opts Options) (*Server, error) {
	if opts.Secret == "" {
		return nil, errors.New("engine: a secret is required")
	}
	opts.defaults()
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		opts:     opts,
		log:      opts.Logger,
		budget:   newBudget(opts.TotalBufferBytes),
		ctx:      ctx,
		cancel:   cancel,
		sessions: map[string]*session{},
		byHandle: map[string]*session{},
		links:    map[*link]struct{}{},
	}
	s.dialFn = opts.Dial
	if s.dialFn == nil {
		d := &net.Dialer{KeepAlive: 30 * time.Second}
		s.dialFn = func(ctx context.Context, addr string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp", addr)
		}
	}
	return s, nil
}

// Serve accepts links on ln until ctx is cancelled, then closes every link
// and IRC socket and returns once all goroutines have exited.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.reapLoop() }()

	var err error
	for {
		var c net.Conn
		c, err = ln.Accept()
		if err != nil {
			break
		}
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.serveLink(c) }()
	}
	s.shutdown()
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// shutdown ends everything. The engine going away drops IRC; that is the one
// upgrade the engine cannot hide.
func (s *Server) shutdown() {
	s.mu.Lock()
	s.closed = true
	links := make([]*link, 0, len(s.links))
	for l := range s.links {
		links = append(links, l)
	}
	sessions := make([]*session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()

	s.cancel()
	for _, l := range links {
		l.close()
	}
	for _, sess := range sessions {
		s.endSession(sess, "engine shutting down")
	}
	s.wg.Wait()
}

// Stats is a point-in-time summary for logs and tests.
type Stats struct {
	Sessions      int
	Attached      int
	BufferedBytes int64
}

// Stats reports the current session count and buffer use.
func (s *Server) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Stats{Sessions: len(s.sessions), BufferedBytes: s.budget.used.Load()}
	for _, sess := range s.sessions {
		sess.mu.Lock()
		if sess.link != nil {
			st.Attached++
		}
		sess.mu.Unlock()
	}
	return st
}

// endSession closes a session's socket and finishes it off.
func (s *Server) endSession(sess *session, reason string) {
	l, did := sess.shutdown(reason)
	if !did {
		return
	}
	s.log.Info("connection ended", "id", sess.logID(), "reason", reason)
	sess.release(l)
}

// forget drops a closed session from every index and returns its buffer to
// the budget.
func (s *Server) forget(sess *session) {
	s.mu.Lock()
	s.forgetLocked(sess)
	s.mu.Unlock()
}

func (s *Server) forgetLocked(sess *session) {
	if s.sessions[sess.key] == sess {
		delete(s.sessions, sess.key)
	}
	sess.mu.Lock()
	for _, h := range sess.handles {
		if s.byHandle[h] == sess {
			delete(s.byHandle, h)
		}
	}
	freed := sess.buf.clear()
	sess.mu.Unlock()
	s.budget.unregister(sess)
	s.budget.refund(freed)
}

// reapLoop ends sockets nobody has claimed for OrphanTimeout, and unclaimed
// ones that never finished registering.
func (s *Server) reapLoop() {
	every := time.Minute
	if t := s.opts.OrphanTimeout; t > 0 && t/4 < every {
		every = max(t/4, 10*time.Millisecond)
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-tick.C:
		}
		s.reap(time.Now())
	}
}

func (s *Server) reap(now time.Time) {
	s.mu.Lock()
	type victim struct {
		sess   *session
		reason string
	}
	var victims []victim
	for _, sess := range s.sessions {
		sess.mu.Lock()
		if sess.link == nil && sess.state != stateClosed {
			switch {
			case !sess.registered && now.Sub(sess.created) > unregisteredTimeout:
				victims = append(victims, victim{sess, "never finished registering"})
			case s.opts.OrphanTimeout > 0 && !sess.detachedAt.IsZero() && now.Sub(sess.detachedAt) > s.opts.OrphanTimeout:
				victims = append(victims, victim{sess, fmt.Sprintf("no stugan attached for %s", s.opts.OrphanTimeout)})
			}
		}
		sess.mu.Unlock()
	}
	s.mu.Unlock()
	for _, v := range victims {
		s.endSession(v.sess, v.reason)
	}
}

// link is one stugan process's connection to the engine.
type link struct {
	srv      *Server
	conn     net.Conn
	instance string
	gen      int64
	version  string

	wake chan struct{}
	done chan struct{}
	once sync.Once

	mu       sync.Mutex
	ctrl     []*engineproto.Frame
	sessions map[*session]struct{}
}

func (l *link) notify() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func (l *link) send(f *engineproto.Frame) {
	l.mu.Lock()
	l.ctrl = append(l.ctrl, f)
	l.mu.Unlock()
	l.notify()
}

func (l *link) addSession(s *session) {
	l.mu.Lock()
	l.sessions[s] = struct{}{}
	l.mu.Unlock()
}

func (l *link) removeSession(s *session) {
	l.mu.Lock()
	delete(l.sessions, s)
	l.mu.Unlock()
}

// close ends the link. Every session it held is detached, not closed: the
// IRC sockets stay up for the next process.
func (l *link) close() {
	l.once.Do(func() {
		close(l.done)
		_ = l.conn.Close()
		l.mu.Lock()
		held := make([]*session, 0, len(l.sessions))
		for sess := range l.sessions {
			held = append(held, sess)
		}
		l.sessions = map[*session]struct{}{}
		l.mu.Unlock()

		srv := l.srv
		srv.mu.Lock()
		delete(srv.links, l)
		for _, sess := range held {
			sess.mu.Lock()
			detached := sess.detachLocked(l)
			gone := sess.state == stateClosed
			sess.mu.Unlock()
			if detached && gone {
				srv.forgetLocked(sess)
			}
		}
		srv.mu.Unlock()
		if len(held) > 0 {
			srv.log.Info("stugan detached", "instance", l.instance, "connections", len(held))
		}
	})
}

func (s *Server) serveLink(c net.Conn) {
	r := engineproto.NewReader(c)
	_ = c.SetReadDeadline(time.Now().Add(s.opts.HelloTimeout))
	f, err := r.Next()
	if err != nil {
		_ = c.Close()
		return
	}
	reject := func(msg string) {
		if b, err := engineproto.Encode(&engineproto.Frame{Op: engineproto.OpError, Error: msg}); err == nil {
			_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
			_, _ = c.Write(b)
		}
		_ = c.Close()
	}
	switch {
	case f.Op == engineproto.OpHealth:
		if b, err := engineproto.Encode(&engineproto.Frame{Op: engineproto.OpHealth, Version: s.opts.Version}); err == nil {
			_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
			_, _ = c.Write(b)
		}
		_ = c.Close()
		return
	case f.Op != engineproto.OpHello:
		reject("expected hello")
		return
	case subtle.ConstantTimeCompare([]byte(f.Secret), []byte(s.opts.Secret)) != 1:
		s.log.Warn("link refused: bad secret", "remote", c.RemoteAddr())
		reject("authentication failed: wrong engine secret")
		return
	case f.Protocol != engineproto.ProtocolMajor:
		s.log.Warn("link refused: protocol mismatch", "remote", c.RemoteAddr(), "theirs", f.Protocol, "ours", engineproto.ProtocolMajor)
		reject(fmt.Sprintf("protocol mismatch: engine speaks %d, stugan speaks %d", engineproto.ProtocolMajor, f.Protocol))
		return
	case f.Instance == "" || strings.ContainsRune(f.Instance, 0):
		reject("hello without a valid instance")
		return
	}

	l := &link{
		srv: s, conn: c, instance: f.Instance, gen: f.Gen, version: f.Version,
		wake: make(chan struct{}, 1), done: make(chan struct{}),
		sessions: map[*session]struct{}{},
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		reject("engine shutting down")
		return
	}
	s.links[l] = struct{}{}
	s.mu.Unlock()
	s.log.Info("stugan attached", "instance", l.instance, "version", l.version, "remote", c.RemoteAddr())

	l.send(&engineproto.Frame{
		Op: engineproto.OpHello, Protocol: engineproto.ProtocolMajor,
		Minor: engineproto.ProtocolMinor, Version: s.opts.Version,
	})
	s.wg.Add(1)
	go func() { defer s.wg.Done(); l.writeLoop() }()
	defer l.close()

	for {
		_ = c.SetReadDeadline(time.Now().Add(s.opts.LinkTimeout))
		f, err := r.Next()
		if err != nil {
			return
		}
		s.handleFrame(l, f)
	}
}

func (s *Server) handleFrame(l *link, f *engineproto.Frame) {
	switch f.Op {
	case engineproto.OpPing:
		l.send(&engineproto.Frame{Op: engineproto.OpPong})
	case engineproto.OpPong:
	case engineproto.OpConnect:
		s.handleConnect(l, f)
	case engineproto.OpWrite:
		s.handleWrite(l, f)
	case engineproto.OpAck:
		if sess := s.lookup(l, f.ID); sess != nil {
			sess.ack(f.Handle, f.Seq)
		}
	case engineproto.OpDetach:
		s.handleDetach(l, f)
	case engineproto.OpClose:
		s.handleClose(l, f)
	default:
		l.send(&engineproto.Frame{Op: engineproto.OpError, Handle: f.Handle, Error: "unknown op " + f.Op})
	}
}

func (s *Server) lookup(l *link, id string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[l.instance+"\x00"+id]
}

// handleConnect is connect-or-attach. The app never has to know which it
// gets: the engine answers `open` for a fresh socket and `attached` for one
// it holds.
func (s *Server) handleConnect(l *link, f *engineproto.Frame) {
	fail := func(msg string) {
		l.send(&engineproto.Frame{Op: engineproto.OpError, Handle: f.Handle, Error: msg})
	}
	if f.ID == "" || f.Handle == "" || f.Addr == "" {
		fail("connect: missing id, handle or addr")
		return
	}
	if _, _, err := net.SplitHostPort(f.Addr); err != nil {
		fail("connect: bad addr: " + err.Error())
		return
	}
	key := l.instance + "\x00" + f.ID

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		fail("engine shutting down")
		return
	}
	if s.byHandle[f.Handle] != nil {
		fail("connect: handle already in use")
		return
	}
	if sess := s.sessions[key]; sess != nil {
		sess.mu.Lock()
		holder := sess.link
		if holder != nil && holder != l && holder.gen > l.gen {
			// A newer process holds it; an older one must not steal it back.
			sess.mu.Unlock()
			fail("connection is held by a newer stugan process")
			return
		}
		oldHandle := sess.handle
		if sess.reusable(f.Fingerprint) {
			if holder != nil {
				holder.removeSession(sess)
				holder.send(&engineproto.Frame{Op: engineproto.OpDetached, Handle: oldHandle, Error: "taken over by a newer connect"})
			}
			if s.byHandle[oldHandle] == sess {
				delete(s.byHandle, oldHandle)
			}
			sess.attachLocked(l, f.Handle)
			s.byHandle[f.Handle] = sess
			attached := sess.ctrl[0]
			sess.mu.Unlock()
			l.addSession(sess)
			l.notify()
			s.log.Info("re-attached", "id", sess.logID(), "nick", attached.Nick,
				"channels", len(attached.Channels), "backlog", attached.Backlog,
				"lost", attached.Lost, "away", time.Duration(attached.DetachedMs)*time.Millisecond)
			return
		}
		// Not re-attachable (edited network, still registering, or dead):
		// end the old socket and dial fresh.
		if holder != nil {
			holder.removeSession(sess)
			holder.send(&engineproto.Frame{Op: engineproto.OpDetached, Handle: oldHandle, Error: "replaced by a new connection"})
			sess.detachLocked(holder)
		}
		sess.mu.Unlock()
		if _, did := sess.shutdown("replaced by a new connection"); did {
			s.log.Info("connection replaced", "id", sess.logID())
		}
		s.forgetLocked(sess)
	}

	sess := newSession(s, l, f, key)
	s.sessions[key] = sess
	s.byHandle[f.Handle] = sess
	s.budget.register(sess)
	l.addSession(sess)
	s.wg.Add(1)
	go func() { defer s.wg.Done(); sess.dial(s.ctx, f) }()
}

func (s *Server) handleWrite(l *link, f *engineproto.Frame) {
	line := f.Text()
	if line == "" || len(line) > engineproto.MaxLineBytes || strings.ContainsAny(line, "\r\n\x00") {
		return
	}
	s.mu.Lock()
	sess := s.byHandle[f.Handle]
	s.mu.Unlock()
	if sess == nil {
		return
	}
	sess.mu.Lock()
	ok := sess.handle == f.Handle && sess.link == l && sess.state == stateOpen
	sess.mu.Unlock()
	if ok {
		sess.enqueueWrite(line)
	}
}

func (s *Server) handleDetach(l *link, f *engineproto.Frame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.byHandle[f.Handle]
	if sess == nil {
		return
	}
	sess.mu.Lock()
	detached := sess.handle == f.Handle && sess.detachLocked(l)
	gone := sess.state == stateClosed
	sess.mu.Unlock()
	if !detached {
		return
	}
	l.removeSession(sess)
	if gone {
		s.forgetLocked(sess)
		return
	}
	s.log.Info("detached; holding the connection", "id", sess.logID())
}

// handleClose ends the IRC socket. Allowed for the handle that holds the
// claim, or held it last while nobody else claimed it since — so a
// superseded connect can never end its successor's socket.
func (s *Server) handleClose(l *link, f *engineproto.Frame) {
	s.mu.Lock()
	sess := s.sessions[l.instance+"\x00"+f.ID]
	if sess == nil {
		s.mu.Unlock()
		return
	}
	sess.mu.Lock()
	allowed := sess.handle == f.Handle && (sess.link == nil || sess.link == l)
	if allowed {
		sess.detachLocked(l)
		// The app asked; it is not waiting for a closed frame.
		sess.finished = true
	}
	sess.mu.Unlock()
	if !allowed {
		s.mu.Unlock()
		return
	}
	l.removeSession(sess)
	s.mu.Unlock()
	if _, did := sess.shutdown("closed by stugan"); did {
		s.log.Info("connection closed by stugan", "id", sess.logID())
	}
	s.forget(sess)
}

// writeLoop is the link's only writer. Control frames go first, then each
// session's owed frames in bounded batches, until nothing is owed.
func (l *link) writeLoop() {
	bw := bufio.NewWriterSize(l.conn, 64<<10)
	defer l.close()
	for {
		select {
		case <-l.done:
			return
		case <-l.wake:
		}
		for {
			_ = l.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
			wrote := 0
			l.mu.Lock()
			ctrl := l.ctrl
			l.ctrl = nil
			held := make([]*session, 0, len(l.sessions))
			for sess := range l.sessions {
				held = append(held, sess)
			}
			l.mu.Unlock()
			for _, f := range ctrl {
				if !l.write(bw, f) {
					return
				}
				wrote++
			}
			for _, sess := range held {
				frames, done, stale := sess.pull(l, pullBatch)
				for _, f := range frames {
					if !l.write(bw, f) {
						return
					}
					wrote++
				}
				if stale || done {
					l.removeSession(sess)
				}
				if done {
					l.srv.forget(sess)
				}
			}
			if err := bw.Flush(); err != nil {
				return
			}
			if wrote == 0 {
				break
			}
		}
	}
}

func (l *link) write(bw *bufio.Writer, f *engineproto.Frame) bool {
	b, err := engineproto.Encode(f)
	if err != nil {
		l.srv.log.Error("encode frame", "op", f.Op, "err", err)
		return true
	}
	_, err = bw.Write(b)
	return err == nil
}
