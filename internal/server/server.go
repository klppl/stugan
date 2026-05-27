// Package server hosts the HTTP and WebSocket endpoints and bridges core
// events to and from browser sockets. It is multi-tenant: every connection
// is resolved to a user (via session cookie, or the single implicit user
// when auth is disabled) and bridged to that user's engine. It depends on
// core through the engine's public API and on a Hub for users/sessions;
// core never imports server.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/klippelism/stugan/internal/core"
	"github.com/klippelism/stugan/internal/proto"
)

// sessionCookie is the name of the session cookie set on login.
const sessionCookie = "stugan_session"

// History provides paged message backlog for replay and full-text search.
type History interface {
	Backlog(ctx context.Context, network, buffer string, before time.Time, limit int) ([]core.Message, bool, error)
	Search(ctx context.Context, query, network, buffer string, limit int) ([]core.Message, error)
}

// Tenant is one user's engine and history.
type Tenant struct {
	Engine  *core.Engine
	History History
}

// Hub resolves users, sessions, and per-user tenants. The composition root
// (cmd/stugan) implements it; SingleUser provides a no-auth one for the
// single-user case and for tests.
type Hub interface {
	AuthEnabled() bool
	// Login verifies credentials and returns the user id.
	Login(username, password string) (string, bool)
	// Session resolves a token to a user id.
	Session(token string) (string, bool)
	// StartSession issues a token for a user and its max-age in seconds.
	StartSession(userID string) (token string, maxAgeSec int)
	// EndSession invalidates a token.
	EndSession(token string)
	// Tenant returns a user's engine + history.
	Tenant(userID string) (*Tenant, bool)
	// Users lists every user id (for sink registration at startup).
	Users() []string
}

// Options configures a Server.
type Options struct {
	Logger         *slog.Logger
	ServerName     string
	OriginPatterns []string
	StaticDir      string
	UploadDir      string
	MaxUpload      int64
	PushDir        string
}

// Server bridges per-user engines to WebSocket clients.
type Server struct {
	hub        Hub
	log        *slog.Logger
	serverName string
	origins    []string
	staticDir  string
	uploadDir  string
	maxUpload  int64
	push       *pushManager

	mu      sync.Mutex
	clients map[string]map[*client]struct{} // userID → connected clients
}

// New builds a Server over the given Hub.
func New(hub Hub, opts Options) *Server {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	origins := opts.OriginPatterns
	if origins == nil {
		origins = []string{"localhost:*", "127.0.0.1:*", "[::1]:*"}
	}
	name := opts.ServerName
	if name == "" {
		name = "stugan"
	}
	maxUpload := opts.MaxUpload
	if maxUpload <= 0 {
		maxUpload = 10 << 20
	}
	push, err := newPushManager(opts.PushDir)
	if err != nil {
		log.Warn("web push disabled", "err", err)
	}
	return &Server{
		hub:        hub,
		log:        log,
		serverName: name,
		origins:    origins,
		staticDir:  opts.StaticDir,
		uploadDir:  opts.UploadDir,
		maxUpload:  maxUpload,
		push:       push,
		clients:    map[string]map[*client]struct{}{},
	}
}

// Sink returns a core.Sink that routes a user's committed lines to that
// user's connected clients. Register it on the user's engine before Run.
func (s *Server) Sink(userID string) core.Sink { return &userSink{s: s, user: userID} }

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/api/me", s.handleMe)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/logout", s.handleLogout)
	mux.HandleFunc("/api/preview", s.requireUser(s.handlePreview))
	mux.HandleFunc("/api/proxy", s.requireUser(s.handleProxy))
	if s.uploadDir != "" {
		mux.HandleFunc("/api/upload", s.requireUser(s.handleUpload))
		mux.Handle("/uploads/", s.uploadFileServer())
	}
	if s.push != nil {
		mux.HandleFunc("/api/push/vapid", s.handleVAPID)
		mux.HandleFunc("/api/push/subscribe", s.requireUser(s.handleSubscribe))
	}
	if s.staticDir != "" {
		mux.Handle("/", http.FileServer(http.Dir(s.staticDir)))
	}
	return mux
}

// ListenAndServe serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	s.log.Info("http server listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// --- auth resolution -------------------------------------------------------

// userOf resolves the request to a user id. When auth is disabled, the
// single implicit user is returned.
func (s *Server) userOf(r *http.Request) (string, bool) {
	if !s.hub.AuthEnabled() {
		users := s.hub.Users()
		if len(users) == 0 {
			return "", false
		}
		return users[0], true
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", false
	}
	return s.hub.Session(c.Value)
}

// requireUser wraps a handler, rejecting unauthenticated requests.
func (s *Server) requireUser(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.userOf(r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userOf(r)
	resp := map[string]any{"authEnabled": s.hub.AuthEnabled(), "user": user, "authenticated": ok}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct{ Username, Password string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	user, ok := s.hub.Login(body.Username, body.Password)
	if !ok {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	tok, maxAge := s.hub.StartSession(user)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: maxAge, Secure: r.TLS != nil,
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"user": user})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.hub.EndSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	w.WriteHeader(http.StatusNoContent)
}

// --- WebSocket -------------------------------------------------------------

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.userOf(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	tenant, ok := s.hub.Tenant(userID)
	if !ok {
		http.Error(w, "unknown user", http.StatusUnauthorized)
		return
	}

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.origins})
	if err != nil {
		s.log.Warn("ws accept failed", "err", err)
		return
	}
	defer ws.CloseNow()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	c := newClient(ws, s.log)
	c.user = userID
	c.tenant = tenant
	go c.writePump(ctx)

	hello, _ := proto.Frame(proto.THello, proto.Hello{Protocol: proto.Protocol, Server: s.serverName, Caps: s.caps()})
	init, _ := proto.Frame(proto.TInit, toInitState(tenant.Engine.Snapshot()))
	c.trySend(hello)
	c.trySend(init)

	s.addClient(userID, c)
	defer s.removeClient(userID, c)

	s.readPump(ctx, c)
}

func (s *Server) readPump(ctx context.Context, c *client) {
	for {
		var env proto.Envelope
		if err := wsjson.Read(ctx, c.ws, &env); err != nil {
			return
		}
		s.route(ctx, c, env)
	}
}

func (s *Server) route(ctx context.Context, c *client, env proto.Envelope) {
	switch env.T {
	case proto.TMsgSend:
		var d proto.MsgSend
		if err := decode(env, &d); err != nil {
			c.sendError(env.ID, "bad_request", "invalid msg:send payload")
			return
		}
		if d.Network == "" || d.Buffer == "" || d.Text == "" {
			c.sendError(env.ID, "bad_request", "msg:send requires network, buffer, text")
			return
		}
		c.tenant.Engine.SendInput(d.Network, d.Buffer, d.Text)

	case proto.TBacklogFetch:
		s.handleBacklog(ctx, c, env)

	case proto.TSearch:
		s.handleSearch(ctx, c, env)

	case proto.TNetAdd:
		var d proto.NetAdd
		if err := decode(env, &d); err != nil || d.Name == "" || d.Addr == "" {
			c.sendError(env.ID, "bad_request", "net:add requires name and addr")
			return
		}
		if err := c.tenant.Engine.AddNetworkLive(core.NetworkParams{
			ID: d.Name, Name: d.Name, Addr: d.Addr, TLS: d.TLS,
			Nick: d.Nick, User: d.User, Realname: d.Realname,
			SASLUser: d.SASLUser, SASLPass: d.SASLPass, Channels: d.Channels,
		}); err != nil {
			c.sendError(env.ID, "bad_request", err.Error())
		}

	case proto.TNetRemove:
		var d proto.NetRemove
		if err := decode(env, &d); err != nil || d.Network == "" {
			c.sendError(env.ID, "bad_request", "net:remove requires network")
			return
		}
		if err := c.tenant.Engine.RemoveNetwork(d.Network); err != nil {
			c.sendError(env.ID, "bad_request", err.Error())
		}

	case proto.TTyping:
		var d proto.Typing
		if err := decode(env, &d); err != nil || d.Network == "" || d.Buffer == "" {
			return // typing is best-effort; ignore malformed
		}
		c.tenant.Engine.SendTyping(d.Network, d.Buffer, d.State)

	case proto.TList:
		var d proto.ListReq
		if err := decode(env, &d); err != nil || d.Network == "" {
			c.sendError(env.ID, "bad_request", "list requires network")
			return
		}
		if err := c.tenant.Engine.ListChannels(d.Network, d.Query); err != nil {
			c.sendError(env.ID, "bad_request", err.Error())
		}

	case proto.TNetConnect:
		var d proto.NetConnect
		if err := decode(env, &d); err != nil || d.Network == "" {
			c.sendError(env.ID, "bad_request", "net:connect requires network")
			return
		}
		if err := c.tenant.Engine.SetConnected(d.Network, d.Connect); err != nil {
			c.sendError(env.ID, "bad_request", err.Error())
		}

	case proto.TNetInfo:
		var d proto.NetInfoReq
		if err := decode(env, &d); err != nil || d.Network == "" {
			c.sendError(env.ID, "bad_request", "net:info requires network")
			return
		}
		p, ok := c.tenant.Engine.NetworkConfig(d.Network)
		if !ok {
			c.sendError(env.ID, "not_found", "unknown network")
			return
		}
		s.reply(c, env.ID, proto.TNetInfo, proto.NetConfig{
			Network: p.ID, Name: p.Name, Addr: p.Addr, TLS: p.TLS,
			Nick: p.Nick, User: p.User, Realname: p.Realname,
			SASLUser: p.SASLUser, SASLPass: p.SASLPass, Channels: p.Channels,
		})

	case proto.TNetEdit:
		var d proto.NetConfig
		if err := decode(env, &d); err != nil || d.Network == "" || d.Addr == "" {
			c.sendError(env.ID, "bad_request", "net:edit requires network and addr")
			return
		}
		if err := c.tenant.Engine.UpdateNetwork(core.NetworkParams{
			ID: d.Network, Name: d.Network, Addr: d.Addr, TLS: d.TLS,
			Nick: d.Nick, User: d.User, Realname: d.Realname,
			SASLUser: d.SASLUser, SASLPass: d.SASLPass, Channels: d.Channels,
		}); err != nil {
			c.sendError(env.ID, "bad_request", err.Error())
		}

	default:
		s.log.Debug("ignoring unknown frame", "t", env.T)
	}
}

func (s *Server) handleBacklog(ctx context.Context, c *client, env proto.Envelope) {
	var d proto.BacklogFetch
	if err := decode(env, &d); err != nil || d.Network == "" || d.Buffer == "" {
		c.sendError(env.ID, "bad_request", "backlog:fetch requires network, buffer")
		return
	}
	if c.tenant.History == nil {
		c.sendError(env.ID, "unavailable", "history is not enabled")
		return
	}
	var before time.Time
	if d.Before != "" {
		if t, err := time.Parse(time.RFC3339, d.Before); err == nil {
			before = t
		}
	}
	limit := clampLimit(d.Limit, 100)
	msgs, more, err := c.tenant.History.Backlog(ctx, d.Network, d.Buffer, before, limit)
	if err != nil {
		s.log.Error("backlog query", "err", err)
		c.sendError(env.ID, "internal", "backlog query failed")
		return
	}
	s.reply(c, env.ID, proto.TBacklog, proto.BacklogResp{
		Network: d.Network, Buffer: d.Buffer, Messages: toMessageDTOs(msgs), More: more,
	})
}

func (s *Server) handleSearch(ctx context.Context, c *client, env proto.Envelope) {
	var d proto.SearchReq
	if err := decode(env, &d); err != nil || d.Query == "" {
		c.sendError(env.ID, "bad_request", "search requires a query")
		return
	}
	if c.tenant.History == nil {
		c.sendError(env.ID, "unavailable", "search is not enabled")
		return
	}
	msgs, err := c.tenant.History.Search(ctx, d.Query, d.Network, d.Buffer, clampLimit(d.Limit, 50))
	if err != nil {
		s.log.Error("search query", "err", err)
		c.sendError(env.ID, "internal", "search failed")
		return
	}
	s.reply(c, env.ID, proto.TSearchResult, proto.SearchResp{Query: d.Query, Results: toMessageDTOs(msgs)})
}

// reply sends a correlated frame to one client.
func (s *Server) reply(c *client, id, t string, d any) {
	env, err := proto.Frame(t, d)
	if err != nil {
		return
	}
	env.ID = id
	c.trySend(env)
}

// --- client registry + fan-out --------------------------------------------

func (s *Server) addClient(user string, c *client) {
	s.mu.Lock()
	if s.clients[user] == nil {
		s.clients[user] = map[*client]struct{}{}
	}
	s.clients[user][c] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) removeClient(user string, c *client) {
	s.mu.Lock()
	delete(s.clients[user], c)
	s.mu.Unlock()
}

// routeToUser fans a frame out to one user's clients.
func (s *Server) routeToUser(user string, env proto.Envelope) {
	s.mu.Lock()
	for c := range s.clients[user] {
		c.trySend(env)
	}
	s.mu.Unlock()
}

func (s *Server) connectedCount(user string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients[user])
}

// caps lists the optional features this server supports.
func (s *Server) caps() []string {
	caps := []string{"uploads", "previews"}
	caps = append(caps, "search") // every tenant has a history store
	if s.push != nil {
		caps = append(caps, "push")
	}
	return caps
}

func clampLimit(n, def int) int {
	if n <= 0 || n > 500 {
		return def
	}
	return n
}

// userSink routes one user's committed lines to their clients.
type userSink struct {
	s    *Server
	user string
}

var _ core.Sink = (*userSink)(nil)

func (u *userSink) Print(m core.Message) {
	if env, err := proto.Frame(proto.TMsg, toMessageDTO(m)); err == nil {
		u.s.routeToUser(u.user, env)
	}
	u.s.maybePush(u.user, m)
}

func (u *userSink) NetworkChanged(n *core.Network) {
	if env, err := proto.Frame(proto.TNetUpdate, toNetworkDTO(n)); err == nil {
		u.s.routeToUser(u.user, env)
	}
}

func (u *userSink) NetworkRemoved(networkID string) {
	if env, err := proto.Frame(proto.TNetRemove, proto.NetRemove{Network: networkID}); err == nil {
		u.s.routeToUser(u.user, env)
	}
}

func (u *userSink) Typing(network, buffer, nick, state string) {
	if env, err := proto.Frame(proto.TTyping, proto.Typing{
		Network: network, Buffer: buffer, Nick: nick, State: state,
	}); err == nil {
		u.s.routeToUser(u.user, env)
	}
}

func (u *userSink) ChannelList(network string, items []core.ChannelListItem) {
	chans := make([]proto.ListChannel, len(items))
	for i, it := range items {
		chans[i] = proto.ListChannel{Name: it.Name, Users: it.Users, Topic: it.Topic}
	}
	if env, err := proto.Frame(proto.TListResult, proto.ListResp{Network: network, Channels: chans}); err == nil {
		u.s.routeToUser(u.user, env)
	}
}
