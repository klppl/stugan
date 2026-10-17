// Package engineproto is the wire contract between stugan and stugan-engine,
// the optional sidecar that holds IRC sockets open while stugan restarts.
//
// The link is newline-delimited JSON over one TCP (or unix) stream per stugan
// process. The engine is a line-level relay: it moves raw IRC lines in both
// directions and keeps the socket — and the server's idea of who is connected —
// alive while no stugan process is attached. Everything that understands IRC
// beyond a handful of commands stays in stugan (internal/irc).
//
// Version skew is expected: the engine is the process that is NOT redeployed,
// so a newer stugan must be able to talk to an older engine. ProtocolMajor is
// the compatibility line — a mismatch is refused at hello. Adding an optional
// field or a new op is a minor change; renaming or re-meaning one is a major.
package engineproto

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// ProtocolMajor is the compatibility line. Both sides must agree on it.
	ProtocolMajor = 1
	// ProtocolMinor counts backwards-compatible additions within a major.
	ProtocolMinor = 0

	// MaxFrameBytes bounds one frame on the wire, enforced by both readers so
	// a misbehaving peer can't make either process buffer without bound. The
	// largest legitimate frame is `attached`, whose replay is capped by the
	// engine's burst limit.
	MaxFrameBytes = 1 << 20

	// MaxLineBytes bounds one relayed IRC line. IRCv3 caps a line at 8191
	// bytes including tags; this is double that so a server slightly over the
	// limit is stugan's problem to parse, not the engine's to disconnect over.
	MaxLineBytes = 16 << 10
)

// Ops. App → engine: hello, ping, pong, connect, write, ack, detach, close,
// health. Engine → app: hello, ping, pong, open, attached, line, detached,
// closed, error, health.
const (
	OpHello    = "hello"
	OpPing     = "ping"
	OpPong     = "pong"
	OpConnect  = "connect"
	OpWrite    = "write"
	OpAck      = "ack"
	OpDetach   = "detach"
	OpClose    = "close"
	OpOpen     = "open"
	OpAttached = "attached"
	OpLine     = "line"
	OpDetached = "detached"
	OpClosed   = "closed"
	OpError    = "error"
	// OpHealth may be sent instead of hello; the engine answers it and closes
	// the link. It carries no secret and reveals nothing, so a container
	// healthcheck can use it.
	OpHealth = "health"
)

// Frame is one message on the link. Which fields are set depends on Op:
//
//	hello (app)     Protocol, Minor, Secret, Instance, Gen, Version
//	hello (engine)  Protocol, Minor, Version
//	connect         ID, Handle, Addr, TLS, Insecure, CertPEM, Fingerprint
//	write           Handle, Line (or Bin)
//	ack             ID, Handle, Seq
//	detach          Handle
//	close           ID, Handle
//	open            Handle
//	attached        Handle, Replay, Nick, Channels, Unattended, DetachedMs,
//	                Backlog, Lost
//	line            Handle, Seq, At, Line (or Bin)
//	detached        Handle, Error (why: taken over / superseded)
//	closed          Handle, Error (empty for a clean close)
//	error           Handle (optional), Error
type Frame struct {
	Op string `json:"op"`

	Protocol int    `json:"protocol,omitempty"`
	Minor    int    `json:"minor,omitempty"`
	Secret   string `json:"secret,omitempty"`
	// Instance identifies the stugan installation (its data directory) the
	// link speaks for. Session ids are only unique within one instance, so
	// the engine partitions every session by it.
	Instance string `json:"instance,omitempty"`
	// Gen is the app process's generation (its start time in ns). When two
	// processes overlap, a connection goes to the newer one and an older one
	// is refused rather than allowed to steal it back.
	Gen     int64  `json:"gen,omitempty"`
	Version string `json:"version,omitempty"`

	// ID names an IRC connection within the instance (user + network).
	ID string `json:"id,omitempty"`
	// Handle names one connect attempt. Every op about a connection after
	// connect carries it, so a superseded attempt (an edited network, a
	// replaced process) can never act on its successor's socket.
	Handle string `json:"handle,omitempty"`

	Addr     string `json:"addr,omitempty"`
	TLS      bool   `json:"tls,omitempty"`
	Insecure bool   `json:"insecure,omitempty"`
	// CertPEM is the TLS client certificate (cert + key, PEM) for CertFP.
	// The private key crosses the link, the same trust boundary that already
	// carries PASS and AUTHENTICATE lines; the engine never logs it.
	CertPEM string `json:"cert,omitempty"`
	// Fingerprint digests every setting that shapes registration. A held
	// socket is only re-attached when it matches; anything else means the
	// user edited the network and wants a fresh connection.
	Fingerprint string `json:"fp,omitempty"`

	// Line is a relayed IRC line (no CRLF). One that is not valid UTF-8 rides
	// in Bin instead (base64 in JSON), so the relay stays byte-exact: JSON
	// would otherwise replace invalid bytes with U+FFFD. Use SetLine/Text.
	Line string `json:"line,omitempty"`
	Bin  []byte `json:"bin,omitempty"`
	Seq  uint64 `json:"seq,omitempty"`
	// At is when the engine received the line, in unix milliseconds.
	At int64 `json:"at,omitempty"`

	// Replay is fed to a fresh IRC client so it believes it just registered:
	// the recorded registration burst (with 001 addressed to the current
	// nick) followed by capability changes made since.
	Replay   []string `json:"replay,omitempty"`
	Nick     string   `json:"nick,omitempty"`
	Channels []string `json:"channels,omitempty"`
	// Unattended: registration completed while no app was attached, so the
	// post-registration steps (perform, autojoin) never ran.
	Unattended bool  `json:"unattended,omitempty"`
	DetachedMs int64 `json:"detached_ms,omitempty"`
	// Backlog is how many buffered lines follow the attached frame.
	Backlog int `json:"backlog,omitempty"`
	// Lost counts lines the engine had to drop, never delivered, because its
	// buffer filled while no app was attached.
	Lost uint64 `json:"lost,omitempty"`

	Error string `json:"error,omitempty"`
}

// SetLine stores an IRC line in f, in Line or (not valid UTF-8) Bin.
func (f *Frame) SetLine(line string) {
	if utf8.ValidString(line) {
		f.Line, f.Bin = line, nil
		return
	}
	f.Line, f.Bin = "", []byte(line)
}

// Text returns the IRC line carried by f.
func (f *Frame) Text() string {
	if f.Bin != nil {
		return string(f.Bin)
	}
	return f.Line
}

// Encode serializes f as one line, newline included.
func Encode(f *Frame) ([]byte, error) {
	b, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	if len(b)+1 > MaxFrameBytes {
		return nil, fmt.Errorf("engineproto: %s frame too large (%d bytes)", f.Op, len(b))
	}
	return append(b, '\n'), nil
}

// ErrFrameTooLong is returned by Reader.Next for an over-long frame; the
// caller should drop the link — nothing legitimate is that big.
var ErrFrameTooLong = errors.New("engineproto: frame too long")

// Reader decodes frames from a stream.
type Reader struct {
	sc *bufio.Scanner
}

// NewReader wraps r.
func NewReader(r io.Reader) *Reader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), MaxFrameBytes)
	return &Reader{sc: sc}
}

// Next returns the next frame. Blank lines are skipped. At end of stream it
// returns io.EOF.
func (r *Reader) Next() (*Frame, error) {
	for r.sc.Scan() {
		b := r.sc.Bytes()
		if len(strings.TrimSpace(string(b))) == 0 {
			continue
		}
		var f Frame
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("engineproto: malformed frame: %w", err)
		}
		if f.Op == "" {
			return nil, errors.New("engineproto: frame without op")
		}
		return &f, nil
	}
	if err := r.sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, ErrFrameTooLong
		}
		return nil, err
	}
	return nil, io.EOF
}

// ParseAddr turns an engine address into a (network, address) pair for
// net.Dial / net.Listen. Accepted forms:
//
//	host:port            tcp
//	tcp://host:port      tcp
//	unix:///path/sock    unix socket
//	unix:/path/sock      unix socket
func ParseAddr(s string) (network, address string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", errors.New("empty engine address")
	}
	if !strings.Contains(s, "://") && !strings.HasPrefix(s, "unix:") {
		return "tcp", s, checkHostPort(s)
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", "", fmt.Errorf("invalid engine address %q: %w", s, err)
	}
	switch u.Scheme {
	case "tcp":
		if u.Path != "" && u.Path != "/" {
			return "", "", fmt.Errorf("invalid engine address %q: tcp address takes no path", s)
		}
		return "tcp", u.Host, checkHostPort(u.Host)
	case "unix":
		p := u.Path
		if p == "" {
			p = u.Opaque
		}
		if p == "" {
			return "", "", fmt.Errorf("invalid engine address %q: missing socket path", s)
		}
		return "unix", p, nil
	default:
		return "", "", fmt.Errorf("invalid engine address %q: scheme must be tcp or unix", s)
	}
}

func checkHostPort(hp string) error {
	i := strings.LastIndexByte(hp, ':')
	if i < 0 || i == len(hp)-1 {
		return fmt.Errorf("invalid engine address %q: missing port", hp)
	}
	port, err := strconv.Atoi(hp[i+1:])
	if err != nil || port <= 0 || port > 65535 {
		return fmt.Errorf("invalid engine address %q: bad port", hp)
	}
	return nil
}
