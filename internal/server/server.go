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
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"golang.org/x/crypto/bcrypt"

	"github.com/klippelism/stugan/internal/auth"
	"github.com/klippelism/stugan/internal/core"
	"github.com/klippelism/stugan/internal/proto"
)

// sessionCookie is the name of the session cookie set on login.
const sessionCookie = "stugan_session"

// magicCookie is the name of the cookie that grants entry past the
// magic-word gate (the outer, site-wide password configured via
// $STUGAN_WEB_PASSWORD). Independent of the per-user session cookie.
const magicCookie = "stugan_magic"

// magicWordTTL is how long a granted magic-word cookie lasts.
const magicWordTTL = 30 * 24 * time.Hour

// History provides paged message backlog for replay and full-text search,
// plus the per-buffer read markers that let unread counts survive a reload.
type History interface {
	Backlog(ctx context.Context, network, buffer string, before time.Time, limit int) ([]core.Message, bool, error)
	BacklogAround(ctx context.Context, network, buffer string, around time.Time, limit int) ([]core.Message, bool, error)
	Search(ctx context.Context, query, network, buffer string, limit int) ([]core.Message, error)
	// MarkRead advances a buffer's read marker to ts (zero = now).
	MarkRead(ctx context.Context, network, buffer string, ts time.Time) error
	// UnreadCounts reports per-buffer unread/highlight tallies since each
	// buffer's read marker, for seeding badges at connect time.
	UnreadCounts(ctx context.Context) ([]core.UnreadCount, error)
}

// Prefs persists per-user server preference blobs (highlight rules, muted
// buffers). The store implements it; it is optional (nil in tests and the
// no-history single-user case), so every use is nil-guarded.
type Prefs interface {
	Pref(key string) (string, error)
	SetPref(key, value string) error
}

// Tenant is one user's engine and history.
type Tenant struct {
	Engine  *core.Engine
	History History
	Prefs   Prefs
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
	// MagicWordHash, when non-empty, enables an outer site-wide password
	// gate (the "magic word"). It must be a bcrypt hash; the daemon
	// hashes the plaintext from $STUGAN_WEB_PASSWORD at startup and
	// never keeps the plaintext in memory. When empty, the gate is
	// disabled and all requests fall through to the normal auth flow.
	MagicWordHash string
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

	// magicHash and magicSessions implement the outer site-wide
	// password gate. magicHash is empty when the gate is disabled.
	magicHash     string
	magicSessions *auth.Sessions

	// authLimit throttles repeated failures against /api/login and
	// /api/magicword to slow credential-stuffing bots.
	authLimit *authRateLimit

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
	s := &Server{
		hub:        hub,
		log:        log,
		serverName: name,
		origins:    origins,
		staticDir:  opts.StaticDir,
		uploadDir:  opts.UploadDir,
		maxUpload:  maxUpload,
		push:       push,
		magicHash:  opts.MagicWordHash,
		authLimit:  newAuthRateLimit(60*time.Second, 8),
		clients:    map[string]map[*client]struct{}{},
	}
	if s.magicHash != "" {
		s.magicSessions = auth.NewSessions(magicWordTTL)
	}
	return s
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
	mux.HandleFunc("/api/magicword", s.handleMagicWord)
	mux.HandleFunc("/api/magicword/logout", s.handleMagicWordLogout)
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
	if s.magicHash == "" {
		return mux
	}
	return s.magicGate(mux)
}

// magicGate wraps the mux so every request that touches user data — the
// WebSocket, the /api/* endpoints, and per-user /uploads — requires a
// granted magic-word cookie when $STUGAN_WEB_PASSWORD is set. Static
// assets stay open so the SPA itself can load and render the prompt.
//
// Endpoints kept open: /api/me, /api/magicword{,/logout}, /healthz.
func (s *Server) magicGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.magicWordOpen(r.URL.Path) || s.magicGranted(r) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

// magicWordOpen reports whether the path is reachable without a
// granted magic-word cookie. Static SPA assets (anything that isn't
// /api/*, /ws, or /uploads/*) stay open so the browser can fetch the
// HTML/CSS/JS that renders the prompt.
func (s *Server) magicWordOpen(path string) bool {
	switch path {
	case "/api/me", "/api/magicword", "/api/magicword/logout", "/healthz":
		return true
	}
	if path == "/ws" || strings.HasPrefix(path, "/uploads/") {
		return false
	}
	return !strings.HasPrefix(path, "/api/")
}

// magicGranted reports whether the request carries a valid magic-word
// cookie. Returns true unconditionally when the gate is disabled.
func (s *Server) magicGranted(r *http.Request) bool {
	if s.magicHash == "" {
		return true
	}
	c, err := r.Cookie(magicCookie)
	if err != nil {
		return false
	}
	_, ok := s.magicSessions.Lookup(c.Value)
	return ok
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
	resp := map[string]any{
		"authEnabled":   s.hub.AuthEnabled(),
		"user":          user,
		"authenticated": ok,
		"magicWord": map[string]bool{
			"required": s.magicHash != "",
			"granted":  s.magicGranted(r),
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleMagicWord verifies the site-wide password and grants an
// HttpOnly cookie. Failed attempts are throttled per source IP and
// answered after a short delay to slow credential-stuffing.
//
// The decoded body also includes honeypot fields (email, website)
// that don't exist on the real form — they're rendered hidden in the
// HTML purely to trap form-filling bots. Any non-empty value there is
// treated as a failed attempt and counted toward the rate limit.
func (s *Server) handleMagicWord(w http.ResponseWriter, r *http.Request) {
	if s.magicHash == "" {
		http.Error(w, "magic word not configured", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	noStore(w)
	ip := clientIP(r)
	if !s.authLimit.allow(ip) {
		http.Error(w, "too many attempts; try again later", http.StatusTooManyRequests)
		return
	}
	var body struct{ Word, Email, Website string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Email != "" || body.Website != "" {
		s.authLimit.fail(ip)
		slowFail(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(s.magicHash), []byte(body.Word)) != nil {
		s.authLimit.fail(ip)
		slowFail(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	tok := s.magicSessions.Create("magic")
	http.SetCookie(w, &http.Cookie{
		Name: magicCookie, Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(s.magicSessions.TTL().Seconds()),
		Secure:   secureCookie(r),
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleMagicWordLogout clears the magic-word cookie. Separate from
// the per-user /api/logout so a multi-user instance can revoke the
// outer gate without invalidating an individual session.
func (s *Server) handleMagicWordLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(magicCookie); err == nil && s.magicSessions != nil {
		s.magicSessions.Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: magicCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	noStore(w)
	ip := clientIP(r)
	if !s.authLimit.allow(ip) {
		http.Error(w, "too many attempts; try again later", http.StatusTooManyRequests)
		return
	}
	// The honeypot fields (Email, Website) don't exist on the real form;
	// they're hidden inputs the SPA renders only to trap auto-fillers.
	var body struct{ Username, Password, Email, Website string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Email != "" || body.Website != "" {
		s.authLimit.fail(ip)
		slowFail(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	user, ok := s.hub.Login(body.Username, body.Password)
	if !ok {
		s.authLimit.fail(ip)
		slowFail(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	tok, maxAge := s.hub.StartSession(user)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: maxAge, Secure: secureCookie(r),
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"user": user})
}

// noStore tells caches and clients not to keep auth endpoint responses.
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

// secureCookie reports whether session cookies should carry the Secure
// attribute. Direct TLS sets r.TLS; behind a TLS-terminating reverse proxy
// (the documented deployment) the proxy speaks plain HTTP to the daemon, so
// r.TLS is nil and we fall back to forwarded-scheme hints. This is fail-safe:
// a spoofed header can only *add* Secure (at worst a plaintext client can't
// send the cookie back — a self-inflicted no-op), never strip it.
//
// X-Forwarded-Proto is the standard signal; the left-most entry is the
// original client scheme when the request crossed more than one proxy.
// CF-Visitor ({"scheme":"https"}) is Cloudflare's native equivalent, honored
// as a fallback for Cloudflare Tunnel deployments.
func secureCookie(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	if strings.EqualFold(strings.TrimSpace(proto), "https") {
		return true
	}
	return strings.Contains(r.Header.Get("CF-Visitor"), `"scheme":"https"`)
}

// slowFail responds with a small delay so an attacker can't pump the
// endpoint at line rate. Real users never notice ~250ms on a typo.
func slowFail(w http.ResponseWriter, msg string, code int) {
	time.Sleep(250 * time.Millisecond)
	http.Error(w, msg, code)
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
	state := toInitState(tenant.Engine.Snapshot())
	// The live unread counter in core.Channel is browser-only (always 0 here);
	// seed real counts from the persisted read markers so badges survive a
	// reload. Best-effort: on error the client just starts everything at 0.
	if tenant.History != nil {
		if counts, err := tenant.History.UnreadCounts(ctx); err != nil {
			s.log.Error("unread counts", "user", userID, "err", err)
		} else {
			applyUnread(&state, counts)
		}
	}
	patterns, exceptions := tenant.Engine.HighlightRules()
	state.Highlight = proto.HighlightRules{Patterns: patterns, Exceptions: exceptions}
	state.Muted = loadMuted(tenant)
	init, _ := proto.Frame(proto.TInit, state)
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

	case proto.TRead:
		var d proto.ReadMark
		if err := decode(env, &d); err != nil || d.Network == "" || d.Buffer == "" {
			return // read marks are best-effort; ignore malformed
		}
		if c.tenant.History == nil {
			return
		}
		if err := c.tenant.History.MarkRead(ctx, d.Network, d.Buffer, time.Time{}); err != nil {
			s.log.Error("mark read", "network", d.Network, "buffer", d.Buffer, "err", err)
		}

	case proto.TCompleteReq:
		var d proto.CompleteReq
		if err := decode(env, &d); err != nil {
			return // completion is best-effort; ignore malformed
		}
		items := c.tenant.Engine.Complete(d.Network, d.Buffer, d.Word)
		s.reply(c, env.ID, proto.TCompleteRes, proto.CompleteRes{Seq: d.Seq, Items: items})

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
			ServerPass: d.ServerPass, Perform: d.Perform,
			SASLExternal: d.SASLExternal, CertPEM: d.CertPEM,
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

	case proto.TReact:
		var d proto.React
		if err := decode(env, &d); err != nil || d.Network == "" || d.Buffer == "" {
			return // reactions are best-effort
		}
		c.tenant.Engine.SendReaction(d.Network, d.Buffer, d.Target, d.Reaction)

	case proto.TRedact:
		var d proto.Redact
		if err := decode(env, &d); err != nil || d.Network == "" || d.Buffer == "" || d.Target == "" {
			c.sendError(env.ID, "bad_request", "redact requires network, buffer, target")
			return
		}
		c.tenant.Engine.SendRedact(d.Network, d.Buffer, d.Target, d.Reason)

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
			ServerPass: p.ServerPass, Perform: p.Perform,
			SASLExternal: p.SASLExternal, CertPEM: p.CertPEM,
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
			ServerPass: d.ServerPass, Perform: d.Perform,
			SASLExternal: d.SASLExternal, CertPEM: d.CertPEM,
		}); err != nil {
			c.sendError(env.ID, "bad_request", err.Error())
		}

	case proto.TPluginList:
		s.reply(c, env.ID, proto.TPluginList, proto.PluginListResp{
			Plugins: toPluginInfos(c.tenant.Engine.Plugins()),
		})

	case proto.TPluginAction:
		var d proto.PluginAction
		if err := decode(env, &d); err != nil || d.Name == "" {
			c.sendError(env.ID, "bad_request", "plugin:action requires name and action")
			return
		}
		var err error
		switch d.Action {
		case "load":
			err = c.tenant.Engine.LoadPlugin(d.Name)
		case "unload":
			err = c.tenant.Engine.UnloadPlugin(d.Name)
		case "reload":
			err = c.tenant.Engine.ReloadPlugin(d.Name)
		default:
			c.sendError(env.ID, "bad_request", "plugin:action requires action load|unload|reload")
			return
		}
		if err != nil {
			c.sendError(env.ID, "bad_request", err.Error())
			return
		}
		s.reply(c, env.ID, proto.TPluginList, proto.PluginListResp{
			Plugins: toPluginInfos(c.tenant.Engine.Plugins()),
		})

	case proto.THighlightSet:
		var d proto.HighlightRules
		if err := decode(env, &d); err != nil {
			c.sendError(env.ID, "bad_request", "invalid highlight:set payload")
			return
		}
		hl, err := core.NewHighlighter(d.Patterns, d.Exceptions)
		if err != nil {
			c.sendError(env.ID, "bad_request", err.Error())
			return
		}
		c.tenant.Engine.SetHighlighter(hl)
		if c.tenant.Prefs != nil {
			if b, err := json.Marshal(d); err == nil {
				if err := c.tenant.Prefs.SetPref(prefHighlight, string(b)); err != nil {
					s.log.Error("save highlight", "user", c.user, "err", err)
				}
			}
		}
		// Broadcast the normalized rules (blank lines dropped) to every one of
		// the user's clients — the requester to confirm the save, other tabs to
		// stay in sync without a reload. The frame is uncorrelated; only the
		// requesting tab flashes "saved" (it tracks that locally).
		if frame, err := proto.Frame(proto.THighlight, proto.HighlightRules{
			Patterns: hl.Patterns(), Exceptions: hl.Exceptions(),
		}); err == nil {
			s.routeToUser(c.user, frame)
		}

	case proto.TMute:
		var d proto.MuteSet
		if err := decode(env, &d); err != nil || d.Network == "" || d.Buffer == "" {
			return // mute is best-effort; ignore malformed
		}
		if c.tenant.Prefs == nil {
			return
		}
		refs := setMuted(loadMuted(c.tenant), d.Network, d.Buffer, d.Muted)
		if b, err := json.Marshal(refs); err == nil {
			if err := c.tenant.Prefs.SetPref(prefMuted, string(b)); err != nil {
				s.log.Error("save mute", "user", c.user, "err", err)
			}
		}
		// Broadcast the absolute new state to every tab so they converge. The
		// client handler sets (not toggles), so the originating tab's optimistic
		// update is a no-op and other tabs catch up.
		if frame, err := proto.Frame(proto.TMute, d); err == nil {
			s.routeToUser(c.user, frame)
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
	limit := clampLimit(d.Limit, 100)

	// Around takes precedence over Before: when the client asks for a
	// window of context around a specific time, return one centered there
	// regardless of any (likely stale) before cursor that came with it.
	if d.Around != "" {
		var around time.Time
		if t, err := time.Parse(time.RFC3339, d.Around); err == nil {
			around = t
		}
		msgs, more, err := c.tenant.History.BacklogAround(ctx, d.Network, d.Buffer, around, limit)
		if err != nil {
			s.log.Error("backlog around query", "err", err)
			c.sendError(env.ID, "internal", "backlog query failed")
			return
		}
		s.reply(c, env.ID, proto.TBacklog, proto.BacklogResp{
			Network: d.Network, Buffer: d.Buffer,
			Messages: toMessageDTOs(msgs), More: more, Around: d.Around,
		})
		return
	}

	var before time.Time
	if d.Before != "" {
		if t, err := time.Parse(time.RFC3339, d.Before); err == nil {
			before = t
		}
	}
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
	caps = append(caps, "search")  // every tenant has a history store
	caps = append(caps, "plugins") // plugin manager (list/load/unload/reload)
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

func (u *userSink) React(network, buffer, target, nick, reaction string) {
	if env, err := proto.Frame(proto.TReact, proto.React{
		Network: network, Buffer: buffer, Target: target, Nick: nick, Reaction: reaction,
	}); err == nil {
		u.s.routeToUser(u.user, env)
	}
}

func (u *userSink) Redact(network, buffer, target, nick, reason string) {
	if env, err := proto.Frame(proto.TRedact, proto.Redact{
		Network: network, Buffer: buffer, Target: target, By: nick, Reason: reason,
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
