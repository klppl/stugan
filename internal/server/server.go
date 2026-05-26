// Package server hosts the HTTP and WebSocket endpoints and bridges core
// events to and from browser sockets. It owns the typed event router
// (encoding/decoding proto structs) and implements core.Sink to fan
// committed buffer lines out to connected clients. It depends on core only
// through the Engine's public API; core never imports server.
package server

import (
	"context"
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

// Options configures a Server.
type Options struct {
	Logger *slog.Logger
	// ServerName is advertised in the Hello frame.
	ServerName string
	// OriginPatterns authorizes WebSocket origins (path.Match patterns).
	// Defaults to localhost variants, which suits the single-user
	// localhost deployment and the Vite dev proxy.
	OriginPatterns []string
	// StaticDir, if set, is served at / (the built Vue client). When empty,
	// only the API is served.
	StaticDir string
}

// Server bridges the core engine to WebSocket clients. It implements
// core.Sink.
type Server struct {
	engine     *core.Engine
	log        *slog.Logger
	serverName string
	origins    []string
	staticDir  string

	mu      sync.Mutex
	clients map[*client]struct{}
}

var _ core.Sink = (*Server)(nil)

// New builds a Server bridging engine to clients. Register it with the
// engine via engine.AddSink(srv) before starting the engine.
func New(engine *core.Engine, opts Options) *Server {
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
	return &Server{
		engine:     engine,
		log:        log,
		serverName: name,
		origins:    origins,
		staticDir:  opts.StaticDir,
		clients:    map[*client]struct{}{},
	}
}

// Handler returns the HTTP handler: /ws for the WebSocket, and (if a static
// dir is configured) the built client at /.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if s.staticDir != "" {
		mux.Handle("/", http.FileServer(http.Dir(s.staticDir)))
	}
	return mux
}

// ListenAndServe serves until ctx is cancelled, then gracefully shuts down.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
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

// Print implements core.Sink: it fans a committed line out to all clients
// as a "msg" frame. Called from the engine loop goroutine; must not block.
func (s *Server) Print(m core.Message) {
	env, err := proto.Frame(proto.TMsg, toMessageDTO(m))
	if err != nil {
		s.log.Error("encode msg frame", "err", err)
		return
	}
	s.mu.Lock()
	for c := range s.clients {
		c.trySend(env)
	}
	s.mu.Unlock()
}

func (s *Server) addClient(c *client) {
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) removeClient(c *client) {
	s.mu.Lock()
	delete(s.clients, c)
	s.mu.Unlock()
}

// handleWS upgrades the connection and runs its read/write pumps.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.origins})
	if err != nil {
		s.log.Warn("ws accept failed", "err", err)
		return
	}
	defer ws.CloseNow()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	c := newClient(ws, s.log)
	go c.writePump(ctx)

	// Greet with hello + an authoritative state snapshot, then go live.
	// Enqueueing before registering keeps wire order hello→init→live; the
	// small window between snapshot and registration is reconciled by
	// backlog replay in Phase 4.
	hello, _ := proto.Frame(proto.THello, proto.Hello{
		Protocol: proto.Protocol, Server: s.serverName, Caps: []string{},
	})
	init, _ := proto.Frame(proto.TInit, toInitState(s.engine.Snapshot()))
	c.trySend(hello)
	c.trySend(init)

	s.addClient(c)
	defer s.removeClient(c)

	s.readPump(ctx, c)
}

// readPump decodes inbound frames and routes them until the client
// disconnects or ctx is cancelled.
func (s *Server) readPump(ctx context.Context, c *client) {
	for {
		var env proto.Envelope
		if err := wsjson.Read(ctx, c.ws, &env); err != nil {
			return // disconnect or ctx cancelled
		}
		s.route(c, env)
	}
}

// route dispatches one decoded frame.
func (s *Server) route(c *client, env proto.Envelope) {
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
		s.engine.SendInput(d.Network, d.Buffer, d.Text)
	default:
		s.log.Debug("ignoring unknown frame", "t", env.T)
	}
}
