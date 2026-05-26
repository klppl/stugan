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

// History provides paged message backlog for replay. Implemented by
// store.Store; the server depends only on this narrow interface, not on the
// store package.
type History interface {
	Backlog(ctx context.Context, network, buffer string, before time.Time, limit int) ([]core.Message, bool, error)
}

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
	// History, if set, answers backlog:fetch requests with replay from the
	// message store. When nil, backlog requests return an error frame.
	History History
}

// Server bridges the core engine to WebSocket clients. It implements
// core.Sink.
type Server struct {
	engine     *core.Engine
	log        *slog.Logger
	serverName string
	origins    []string
	staticDir  string
	history    History

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
		history:    opts.History,
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
	s.broadcast(env)
}

// NetworkChanged implements core.Sink: it pushes a net:update with the
// network's current snapshot to all clients.
func (s *Server) NetworkChanged(n *core.Network) {
	env, err := proto.Frame(proto.TNetUpdate, toNetworkDTO(n))
	if err != nil {
		s.log.Error("encode net:update frame", "err", err)
		return
	}
	s.broadcast(env)
}

func (s *Server) broadcast(env proto.Envelope) {
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
		s.route(ctx, c, env)
	}
}

// route dispatches one decoded frame.
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
		s.engine.SendInput(d.Network, d.Buffer, d.Text)

	case proto.TBacklogFetch:
		s.handleBacklog(ctx, c, env)

	default:
		s.log.Debug("ignoring unknown frame", "t", env.T)
	}
}

// handleBacklog answers a backlog:fetch with a page of history from the
// store, correlated to the request id.
func (s *Server) handleBacklog(ctx context.Context, c *client, env proto.Envelope) {
	var d proto.BacklogFetch
	if err := decode(env, &d); err != nil {
		c.sendError(env.ID, "bad_request", "invalid backlog:fetch payload")
		return
	}
	if d.Network == "" || d.Buffer == "" {
		c.sendError(env.ID, "bad_request", "backlog:fetch requires network, buffer")
		return
	}
	if s.history == nil {
		c.sendError(env.ID, "unavailable", "history is not enabled")
		return
	}
	var before time.Time
	if d.Before != "" {
		if t, err := time.Parse(time.RFC3339, d.Before); err == nil {
			before = t
		}
	}
	limit := d.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	msgs, more, err := s.history.Backlog(ctx, d.Network, d.Buffer, before, limit)
	if err != nil {
		s.log.Error("backlog query", "network", d.Network, "buffer", d.Buffer, "err", err)
		c.sendError(env.ID, "internal", "backlog query failed")
		return
	}
	dtos := make([]proto.MessageDTO, len(msgs))
	for i, m := range msgs {
		dtos[i] = toMessageDTO(m)
	}
	reply, err := proto.Frame(proto.TBacklog, proto.BacklogResp{
		Network: d.Network, Buffer: d.Buffer, Messages: dtos, More: more,
	})
	if err != nil {
		return
	}
	reply.ID = env.ID
	c.trySend(reply)
}
