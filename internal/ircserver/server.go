package ircserver

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/klippelism/stugan/internal/core"
	"github.com/klippelism/stugan/internal/server"
)

// History is the backlog provider for IRC bouncer sessions.
type History interface {
	Backlog(ctx context.Context, network, buffer string, beforeSeq int64, limit int) ([]core.Message, bool, error)
}

// Hub resolves users, credentials, and tenant engines.
type Hub interface {
	AuthEnabled() bool
	Login(username, password string) (string, bool)
	Tenant(userID string) (*server.Tenant, bool)
	Users() []string
}

// Options configures the IRC bouncer server.
type Options struct {
	Logger      *slog.Logger
	TLS         bool
	CertFile    string
	KeyFile     string
	DataDir     string
	MaxPlayback int
}

// Server serves the downstream IRC bouncer protocol over TCP or TLS.
type Server struct {
	hub       Hub
	opts      Options
	log       *slog.Logger
	reg       *registry
	tlsResult *TLSResult

	mu       sync.Mutex
	listener net.Listener
}

// New creates a new IRC bouncer server.
func New(hub Hub, opts Options) *Server {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	if opts.MaxPlayback <= 0 {
		opts.MaxPlayback = defaultMaxPlayback
	}
	return &Server{
		hub:  hub,
		opts: opts,
		log:  log,
		reg:  newRegistry(),
	}
}

// Sink returns the core.Sink for a specific user to be registered on the engine.
func (s *Server) Sink(userID string) core.Sink {
	return &ircSink{srv: s, user: userID}
}

type registry struct {
	mu        sync.RWMutex
	all       map[*Session]struct{}
	byUserNet map[string]map[*Session]struct{}
	byUser    map[string]map[*Session]struct{}
}

func newRegistry() *registry {
	return &registry{
		all:       make(map[*Session]struct{}),
		byUserNet: make(map[string]map[*Session]struct{}),
		byUser:    make(map[string]map[*Session]struct{}),
	}
}

func (r *registry) add(sess *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.all[sess] = struct{}{}

	if sess.userID != "" {
		if r.byUser[sess.userID] == nil {
			r.byUser[sess.userID] = make(map[*Session]struct{})
		}
		r.byUser[sess.userID][sess] = struct{}{}

		if sess.networkID != "" {
			key := sess.userID + ":" + sess.networkID
			if r.byUserNet[key] == nil {
				r.byUserNet[key] = make(map[*Session]struct{})
			}
			r.byUserNet[key][sess] = struct{}{}
		}
	}
}

func (r *registry) remove(sess *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.all, sess)

	if sess.userID != "" {
		if m := r.byUser[sess.userID]; m != nil {
			delete(m, sess)
			if len(m) == 0 {
				delete(r.byUser, sess.userID)
			}
		}
		if sess.networkID != "" {
			key := sess.userID + ":" + sess.networkID
			if m := r.byUserNet[key]; m != nil {
				delete(m, sess)
				if len(m) == 0 {
					delete(r.byUserNet, key)
				}
			}
		}
	}
}

func (r *registry) sessionsFor(userID, networkID string) []*Session {
	r.mu.RLock()
	defer r.mu.RUnlock()

	key := userID + ":" + networkID
	set := r.byUserNet[key]
	out := make([]*Session, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	return out
}

func (r *registry) allForUser(userID string) []*Session {
	r.mu.RLock()
	defer r.mu.RUnlock()

	set := r.byUser[userID]
	out := make([]*Session, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	return out
}

func (r *registry) allSessions() []*Session {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*Session, 0, len(r.all))
	for s := range r.all {
		out = append(out, s)
	}
	return out
}

// ListenAndServe binds the IRC bouncer listener and serves clients until context is canceled.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	if addr == "" {
		return nil
	}

	var ln net.Listener
	var err error

	if s.opts.TLS {
		tlsRes, err := SetupTLS(s.opts.CertFile, s.opts.KeyFile, s.opts.DataDir)
		if err != nil {
			return fmt.Errorf("ircserver: tls setup failed: %w", err)
		}
		s.tlsResult = tlsRes
		ln, err = tls.Listen("tcp", addr, tlsRes.Config)
		if err != nil {
			return fmt.Errorf("ircserver: tls listen on %s failed: %w", addr, err)
		}
		pinMsg := ""
		if tlsRes.Source == "self-signed" {
			pinMsg = " (self-signed — pin this fingerprint in your IRC client)"
		}
		s.log.Info("irc bouncer listening with TLS",
			"addr", addr,
			"tls_source", tlsRes.Source,
			"fingerprint_sha256", tlsRes.Fingerprint+pinMsg,
		)
	} else {
		ln, err = net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("ircserver: listen on %s failed: %w", addr, err)
		}
		s.log.Info("irc bouncer listening in plaintext", "addr", addr)
	}

	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		s.mu.Lock()
		if s.listener != nil {
			_ = s.listener.Close()
		}
		s.mu.Unlock()
		for _, sess := range s.reg.allSessions() {
			sess.destroy("Server shutting down")
		}
	}()

	// Heartbeat ticker
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-ticker.C:
				for _, sess := range s.reg.allSessions() {
					sess.heartbeat(t)
				}
			}
		}
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return nil
			}
			s.log.Warn("ircserver: accept error", "err", err)
			continue
		}

		sess := newSession(conn, s, s.hub)
		go sess.serve()
	}
}

func (s *Server) broadcastPrint(userID string, m core.Message) {
	sessions := s.reg.sessionsFor(userID, m.Network)
	for _, sess := range sessions {
		if !sess.registered || sess.closed.Load() {
			continue
		}

		// Self-echo suppression: if this message was sent by this session,
		// only send it back if the client explicitly negotiated echo-message.
		if m.Self && sess.isOwnEcho(m.Kind, m.Buffer, m.Text) {
			if !sess.caps[CapEchoMessage] {
				continue
			}
		}

		lines := sess.formatSinkMessage(m)
		for _, line := range lines {
			_ = sess.write(line)
		}
	}
}

func (s *Server) broadcastNetworkChanged(userID string, n *core.Network) {
	sessions := s.reg.allForUser(userID)
	for _, sess := range sessions {
		if !sess.registered || sess.closed.Load() {
			continue
		}

		if sess.caps[CapBouncerNetworksNotify] {
			state := "disconnected"
			if n.State == core.StateRegistered {
				state = "connected"
			} else if n.State == core.StateConnecting {
				state = "connecting"
			}
			attrs := fmt.Sprintf("state=%s", state)
			if state == "connected" {
				attrs = "state=connected;error="
			}
			_ = sess.write(FormatLine("", serverName, "BOUNCER", "NETWORK", n.ID, attrs))
		}

		if sess.networkID == n.ID {
			if n.State == core.StateRegistered {
				sess.notice(fmt.Sprintf("Network '%s' is now connected.", n.Name))
			} else if n.State == core.StateDisconnected {
				sess.notice(fmt.Sprintf("Network '%s' is disconnected — will keep retrying.", n.Name))
			}
		}
	}
}

func (s *Server) broadcastNetworkRemoved(userID string, networkID string) {
	sessions := s.reg.sessionsFor(userID, networkID)
	for _, sess := range sessions {
		sess.closeWithError("Network removed")
	}
}

func (s *Server) broadcastChannelList(userID, network string, items []core.ChannelListItem) {
	sessions := s.reg.sessionsFor(userID, network)
	for _, sess := range sessions {
		if !sess.registered || sess.closed.Load() {
			continue
		}
		nick := sess.currentNickOr("*")
		_ = sess.write(FormatLine("", serverName, "321", nick, "Channel", ":Users  Name"))
		for _, it := range items {
			userCount := strconv.Itoa(it.Users)
			_ = sess.write(FormatLine("", serverName, "322", nick, it.Name, userCount, ":"+it.Topic))
		}
		_ = sess.write(FormatLine("", serverName, "323", nick, ":End of /LIST"))
	}
}

func (s *Server) broadcastTyping(userID, network, buffer, nick, state string) {
	sessions := s.reg.sessionsFor(userID, network)
	for _, sess := range sessions {
		if !sess.registered || sess.closed.Load() || !sess.caps[CapMessageTags] {
			continue
		}
		fromPrefix := fmt.Sprintf("%s!stugan@%s", nick, serverName)
		tags := fmt.Sprintf("+typing=%s", state)
		_ = sess.write(FormatLine(tags, fromPrefix, "TAGMSG", buffer))
	}
}

func (s *Server) broadcastReact(userID, network, buffer, target, nick, reaction string) {
	sessions := s.reg.sessionsFor(userID, network)
	for _, sess := range sessions {
		if !sess.registered || sess.closed.Load() || !sess.caps[CapMessageTags] {
			continue
		}
		fromPrefix := fmt.Sprintf("%s!stugan@%s", nick, serverName)
		tags := fmt.Sprintf("+draft/react=%s;+draft/reply=%s", EscapeTagValue(reaction), target)
		_ = sess.write(FormatLine(tags, fromPrefix, "TAGMSG", buffer))
	}
}

func (s *Server) broadcastRedact(userID, network, buffer, target, nick, reason string) {
	sessions := s.reg.sessionsFor(userID, network)
	for _, sess := range sessions {
		if !sess.registered || sess.closed.Load() || !sess.caps[CapMessageTags] {
			continue
		}
		fromPrefix := fmt.Sprintf("%s!stugan@%s", nick, serverName)
		tags := fmt.Sprintf("+draft/delete=%s", target)
		_ = sess.write(FormatLine(tags, fromPrefix, "TAGMSG", buffer))
	}
}

func (s *Server) broadcastReadMarker(userID, network, buffer string, ts time.Time) {
	sessions := s.reg.sessionsFor(userID, network)
	for _, sess := range sessions {
		if !sess.registered || sess.closed.Load() || (!sess.caps[CapReadMarker] && !sess.caps[CapReadMarkerAlt]) {
			continue
		}
		param := "timestamp=" + FormatIrcTime(ts)
		_ = sess.write(FormatLine("", serverName, "MARKREAD", buffer, param))
	}
}
