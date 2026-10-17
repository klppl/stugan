package engine

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/klippelism/stugan/internal/engineproto"
)

type sessionState int

const (
	stateDialing sessionState = iota
	stateOpen
	stateClosed
)

const (
	// maxBurstBytes stops the recorded registration burst from growing. The
	// part a replay needs (CAP, SASL, 001–005) is a few KiB; a server that
	// never ends its burst must not grow it forever.
	maxBurstBytes = 64 << 10
	// maxCapLogBytes bounds the post-registration CAP ACK/DEL record.
	maxCapLogBytes = 16 << 10
	// recentHandles is how many past handles may still ack. A just-replaced
	// handle's in-flight acks remain valid: they describe lines it committed.
	recentHandles = 8
	// writeQueueSize bounds lines waiting for the IRC socket. Lines from the
	// app are already flood-limited by girc; a full queue means the server
	// stopped reading, and the socket is given up on.
	writeQueueSize = 1024
	// upstreamWriteTimeout is how long one write to the IRC server may block.
	upstreamWriteTimeout = 30 * time.Second
	// probeToken marks the engine's own keepalive PING; its PONG is not relayed.
	probeToken = "stugan-engine-probe"
)

// session is one held IRC socket. Its whole reason to exist is that it
// outlives the stugan process that asked for it.
//
// It understands exactly six IRC things, and nothing else:
//  1. PING         — answered here, always, never forwarded: a detached app
//     can't answer it.
//  2. 001          — registration done; our nick as the server confirmed it.
//  3. 376 / 422    — the end of the registration burst recorded for replay.
//  4. own NICK     — so a replay lands on the live nick.
//  5. own JOIN / PART / KICK — the channels a re-attaching app is told about.
//  6. CAP ACK / DEL after registration — the caps that are on now.
//
// Every other line is bytes: numbered, buffered until acked, relayed.
type session struct {
	srv      *Server
	key      string // instance + "\x00" + id
	instance string
	id       string
	fp       string
	addr     string
	created  time.Time

	writeQ   chan string
	stop     chan struct{}
	stopOnce sync.Once

	mu         sync.Mutex
	state      sessionState
	closeErr   string
	conn       net.Conn
	link       *link  // attached link; nil while detached
	handle     string // the current (or, while detached, the last) claim
	handles    []string
	detachedAt time.Time

	buf      lineBuffer
	seq      uint64 // last assigned
	acked    uint64
	sent     uint64 // highest seq written to the current link
	everSent uint64 // highest seq ever written to any link
	lost     uint64 // undelivered lines dropped since the last attach
	ctrl     []*engineproto.Frame
	finished bool // the closed frame went out (or nobody needs one)

	registered   bool
	unattended   bool
	burst        []string
	burstBytes   int
	burstOpen    bool
	burstCovered uint64 // seq of the last line that arrived during the burst
	welcomeIdx   int    // index of 001 in burst; -1 until seen
	nick         string
	channels     []string
	capLog       []string
	capLogBytes  int
}

func newSession(srv *Server, l *link, f *engineproto.Frame, key string) *session {
	s := &session{
		srv:        srv,
		key:        key,
		instance:   l.instance,
		id:         f.ID,
		fp:         f.Fingerprint,
		addr:       f.Addr,
		created:    time.Now(),
		writeQ:     make(chan string, writeQueueSize),
		stop:       make(chan struct{}),
		link:       l,
		handle:     f.Handle,
		handles:    []string{f.Handle},
		welcomeIdx: -1,
	}
	return s
}

// dial opens the IRC socket (TCP, then TLS if asked) and starts relaying.
func (s *session) dial(ctx context.Context, f *engineproto.Frame) {
	conn, err := s.open(ctx, f)

	s.mu.Lock()
	if s.state == stateClosed {
		// Ended (closed by the app, replaced, server shutdown) while dialing.
		s.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
		return
	}
	if err != nil {
		s.state = stateClosed
		s.closeErr = err.Error()
		l := s.link
		s.mu.Unlock()
		s.stopOnce.Do(func() { close(s.stop) })
		s.srv.log.Info("dial failed", "id", s.logID(), "addr", s.addr, "err", err)
		s.release(l)
		return
	}
	s.conn = conn
	s.state = stateOpen
	s.burstOpen = true
	s.ctrl = append(s.ctrl, &engineproto.Frame{Op: engineproto.OpOpen, Handle: s.handle})
	l := s.link
	s.mu.Unlock()

	s.srv.log.Info("connected", "id", s.logID(), "addr", s.addr, "tls", f.TLS)
	s.srv.wg.Add(2)
	go func() { defer s.srv.wg.Done(); s.readLoop(conn) }()
	go func() { defer s.srv.wg.Done(); s.writeLoop(conn) }()
	if l != nil {
		l.notify()
	}
}

func (s *session) open(ctx context.Context, f *engineproto.Frame) (net.Conn, error) {
	host, _, err := net.SplitHostPort(f.Addr)
	if err != nil {
		return nil, fmt.Errorf("bad address %q: %w", f.Addr, err)
	}
	var tcfg *tls.Config
	if f.TLS {
		tcfg = &tls.Config{
			ServerName:         host,
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: f.Insecure, //nolint:gosec // user opt-in for self-signed/LAN servers
		}
		if f.CertPEM != "" {
			// Parse before dialing: a malformed pair must fail this one
			// connection, never anything else in the process.
			cert, err := tls.X509KeyPair([]byte(f.CertPEM), []byte(f.CertPEM))
			if err != nil {
				return nil, fmt.Errorf("parse client certificate: %w", err)
			}
			tcfg.Certificates = []tls.Certificate{cert}
		}
	}
	dctx, cancel := context.WithTimeout(ctx, s.srv.opts.DialTimeout)
	defer cancel()
	conn, err := s.srv.dialFn(dctx, f.Addr)
	if err != nil {
		return nil, err
	}
	if tcfg == nil {
		return conn, nil
	}
	tc := tls.Client(conn, tcfg)
	if err := tc.HandshakeContext(dctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return tc, nil
}

// readLoop reads lines off the IRC socket until it fails. An idle socket is
// probed with a PING so a silently dead connection is noticed even while no
// app is attached (and so can't be re-attached to forever).
func (s *session) readLoop(conn net.Conn) {
	lr := &lineReader{br: bufio.NewReaderSize(conn, 4096)}
	probed := false
	for {
		idle := s.srv.opts.IdleTimeout
		if probed {
			idle = s.srv.opts.ProbeTimeout
		}
		_ = conn.SetReadDeadline(time.Now().Add(idle))
		line, err := lr.next(engineproto.MaxLineBytes)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() && !probed {
				probed = true
				s.enqueueWrite("PING :" + probeToken)
				continue
			}
			reason := "connection closed by server"
			switch {
			case errors.As(err, &ne) && ne.Timeout():
				reason = "ping timeout"
			case !errors.Is(err, io.EOF):
				reason = err.Error()
			}
			s.srv.endSession(s, reason)
			return
		}
		probed = false
		s.ingest(line)
	}
}

// lineReader reads CRLF/LF-terminated lines. A read that times out mid-line
// keeps what it has, so the idle probe's deadline can't split a line in two.
type lineReader struct {
	br      *bufio.Reader
	partial []byte
}

// next returns the next line without its terminator. A line longer than
// limit is an error: whatever sent it is not an IRC server.
func (r *lineReader) next(limit int) (string, error) {
	for {
		chunk, err := r.br.ReadSlice('\n')
		if len(r.partial)+len(chunk) > limit+2 {
			r.partial = nil
			return "", fmt.Errorf("line longer than %d bytes", limit)
		}
		r.partial = append(r.partial, chunk...)
		if err == nil {
			line := strings.TrimRight(string(r.partial), "\r\n")
			r.partial = r.partial[:0]
			return line, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return "", err
		}
	}
}

func (s *session) writeLoop(conn net.Conn) {
	for {
		select {
		case <-s.stop:
			return
		case line := <-s.writeQ:
			_ = conn.SetWriteDeadline(time.Now().Add(upstreamWriteTimeout))
			if _, err := io.WriteString(conn, line+"\r\n"); err != nil {
				s.srv.endSession(s, "write: "+err.Error())
				return
			}
		}
	}
}

// enqueueWrite queues a line for the IRC socket without blocking the caller.
func (s *session) enqueueWrite(line string) {
	select {
	case s.writeQ <- line:
	case <-s.stop:
	default:
		// Callers run on goroutines the WaitGroup already counts, so the
		// count can't be zero here.
		s.srv.wg.Add(1)
		go func() {
			defer s.srv.wg.Done()
			s.srv.endSession(s, "write queue overflow (server not reading)")
		}()
	}
}

// ingest handles one inbound line.
func (s *session) ingest(raw string) {
	l, ok := parseLine(raw)
	if !ok || len(strings.TrimSpace(raw)) < 2 {
		// Not a line the IRC client could parse either. girc drops the
		// connection over one; relayed, the reconnect would re-attach and be
		// handed the same unacked line again, forever. Nothing in it can be
		// shown anyway, so it never enters the stream.
		return
	}
	if l.cmd == "PING" {
		s.enqueueWrite("PONG :" + l.last())
		return
	}
	if l.cmd == "PONG" && l.last() == probeToken {
		return
	}

	s.mu.Lock()
	if s.state != stateOpen {
		s.mu.Unlock()
		return
	}
	s.seq++
	s.buf.push(bufferedLine{seq: s.seq, at: time.Now().UnixMilli(), line: raw})
	s.srv.budget.charge(lineCost(raw))
	s.track(l, raw)
	for s.buf.bytes.Load() > s.srv.opts.BufferBytes && s.buf.len() > 1 {
		s.dropOldestLocked()
	}
	attached := s.link
	s.mu.Unlock()

	s.srv.budget.reclaim()
	if attached != nil {
		attached.notify()
	}
}

// track updates the little IRC state a replay is built from. Caller holds mu.
func (s *session) track(l ircLine, raw string) {
	if s.burstOpen {
		s.burstCovered = s.seq
		if burstCommands[l.cmd] {
			if s.burstBytes+len(raw) > maxBurstBytes {
				// Latched: the burst must stay a contiguous prefix.
				s.burstOpen = false
			} else {
				s.burst = append(s.burst, raw)
				s.burstBytes += len(raw)
				if l.cmd == "001" && s.welcomeIdx < 0 {
					s.welcomeIdx = len(s.burst) - 1
				}
			}
		}
		if endOfBurst(l.cmd) {
			s.burstOpen = false
		}
	} else if l.cmd == "CAP" && (strings.EqualFold(l.param(1), "ACK") || strings.EqualFold(l.param(1), "DEL")) {
		// A cap-notify change after registration: the burst alone stops
		// describing this socket's caps the moment one lands.
		if s.capLogBytes+len(raw) <= maxCapLogBytes {
			s.capLog = append(s.capLog, raw)
			s.capLogBytes += len(raw)
		}
	}

	self := s.nick != "" && l.nick != "" && fold(l.nick) == fold(s.nick)
	switch l.cmd {
	case "001":
		if !s.registered {
			s.registered = true
			s.unattended = s.link == nil
		}
		if n := l.param(0); n != "" {
			s.nick = n
		}
	case "NICK":
		if self && l.param(0) != "" {
			s.nick = l.param(0)
		}
	case "JOIN":
		if self {
			for ch := range strings.SplitSeq(l.param(0), ",") {
				s.addChannel(ch)
			}
		}
	case "PART":
		if self {
			for ch := range strings.SplitSeq(l.param(0), ",") {
				s.removeChannel(ch)
			}
		}
	case "KICK":
		if s.nick != "" && fold(l.param(1)) == fold(s.nick) {
			s.removeChannel(l.param(0))
		}
	}
}

func (s *session) addChannel(ch string) {
	if ch == "" || ch == "0" {
		return
	}
	f := fold(ch)
	for _, c := range s.channels {
		if fold(c) == f {
			return
		}
	}
	s.channels = append(s.channels, ch)
}

func (s *session) removeChannel(ch string) {
	f := fold(ch)
	s.channels = slices.DeleteFunc(s.channels, func(c string) bool { return fold(c) == f })
}

// dropOldestLocked sheds the oldest buffered line. Caller holds mu.
func (s *session) dropOldestLocked() {
	l, ok := s.buf.dropOldest()
	if !ok {
		return
	}
	s.srv.budget.refund(lineCost(l.line))
	if l.seq > s.everSent {
		s.lost++
	}
}

// shedOldest is the global budget's hook; false when nothing can go.
func (s *session) shedOldest() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buf.len() <= 1 {
		return false
	}
	s.dropOldestLocked()
	return true
}

// reusable reports whether a connect with fingerprint fp may attach to this
// socket rather than dialing a new one. Caller holds mu. A socket still
// registering is never re-attached: girc would have to pick up someone
// else's half-finished handshake, and a fresh dial costs seconds.
func (s *session) reusable(fp string) bool {
	return s.state == stateOpen && s.registered && s.welcomeIdx >= 0 && s.fp == fp
}

// attachLocked claims the session for (l, handle) and queues the attached
// frame: the replay, then (via pull) every buffered line the app has not
// acked. Caller holds mu.
func (s *session) attachLocked(l *link, handle string) {
	var detachedMs int64
	if !s.detachedAt.IsZero() {
		detachedMs = time.Since(s.detachedAt).Milliseconds()
	}
	s.link = l
	s.handle = handle
	s.handles = append(s.handles, handle)
	if len(s.handles) > recentHandles {
		s.handles = s.handles[len(s.handles)-recentHandles:]
	}
	s.detachedAt = time.Time{}
	// The replay covers the burst; the backlog starts after whatever the app
	// already committed or the replay already describes.
	s.sent = max(s.acked, s.burstCovered)

	replay := make([]string, 0, len(s.burst)+len(s.capLog))
	for i, line := range s.burst {
		if i == s.welcomeIdx && s.nick != "" {
			line = rewriteTarget(line, s.nick)
		}
		replay = append(replay, line)
	}
	replay = append(replay, s.capLog...)

	s.ctrl = []*engineproto.Frame{{
		Op:         engineproto.OpAttached,
		Handle:     handle,
		Replay:     replay,
		Nick:       s.nick,
		Channels:   slices.Clone(s.channels),
		Unattended: s.unattended,
		DetachedMs: detachedMs,
		Backlog:    s.buf.countAfter(s.sent),
		Lost:       s.lost,
	}}
	s.lost = 0
	// Registration is now attended: the app this goes to runs the
	// post-registration steps if needed, so the next re-attach must not ask
	// for them again.
	s.unattended = false
}

// detachLocked releases the claim held by l, keeping the socket. Caller
// holds mu. Reports whether the session was claimed by l.
func (s *session) detachLocked(l *link) bool {
	if s.link != l {
		return false
	}
	s.link = nil
	s.detachedAt = time.Now()
	s.ctrl = nil
	return true
}

// pull returns the frames owed to link l: control frames, then buffered lines
// past what l has been sent, then — once the socket is gone and
// every line went out — the closed frame. At most limit lines are
// returned per call. done reports that the closed frame
// is among them; stale that l no longer holds the session.
func (s *session) pull(l *link, limit int) (out []*engineproto.Frame, done, stale bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.link != l {
		return nil, false, true
	}
	out = s.ctrl
	s.ctrl = nil
	if s.state != stateDialing {
		for _, bl := range s.buf.after(s.sent, limit) {
			f := &engineproto.Frame{Op: engineproto.OpLine, Handle: s.handle, Seq: bl.seq, At: bl.at}
			f.SetLine(bl.line)
			out = append(out, f)
			s.sent = bl.seq
		}
		s.everSent = max(s.everSent, s.sent)
	}
	if s.state == stateClosed && !s.finished && s.buf.countAfter(s.sent) == 0 {
		out = append(out, &engineproto.Frame{Op: engineproto.OpClosed, Handle: s.handle, Error: s.closeErr})
		s.finished = true
		done = true
	}
	return out, done, false
}

// ack forgets every line through seq, if handle is (or recently was) a claim
// on this session.
func (s *session) ack(handle string, seq uint64) {
	s.mu.Lock()
	if !slices.Contains(s.handles, handle) || seq <= s.acked || seq > s.seq {
		s.mu.Unlock()
		return
	}
	freed := s.buf.ackThrough(seq)
	s.acked = seq
	s.mu.Unlock()
	s.srv.budget.refund(freed)
}

// shutdown closes the socket and marks the session closed. It returns the
// link that should be told (nil when detached) and whether this call did it.
func (s *session) shutdown(reason string) (*link, bool) {
	s.mu.Lock()
	if s.state == stateClosed {
		s.mu.Unlock()
		return nil, false
	}
	s.state = stateClosed
	s.closeErr = reason
	conn := s.conn
	l := s.link
	s.mu.Unlock()
	s.stopOnce.Do(func() { close(s.stop) })
	if conn != nil {
		_ = conn.Close()
	}
	return l, true
}

// release finishes off a closed session: an attached link is woken to deliver
// the remaining lines and the closed frame (its writer forgets the session
// afterwards); a detached one is forgotten now.
func (s *session) release(l *link) {
	if l != nil {
		l.notify()
		return
	}
	s.srv.forget(s)
}

// logID is the session id for logs, without the instance.
func (s *session) logID() string { return strings.ReplaceAll(s.id, "\x00", "/") }
