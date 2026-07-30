package tui

import (
	"context"
	"log/slog"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/activeterm"
	bm "github.com/charmbracelet/wish/bubbletea"
	"github.com/klippelism/stugan/internal/core"
	"github.com/muesli/termenv"
)

// ctxUserKey stashes the authenticated user id on the SSH connection context,
// set by the public-key handler and read by the program handler.
type ctxUserKey struct{}

// Options configures the SSH TUI server.
type Options struct {
	Addr        string // listen address, e.g. "0.0.0.0:2222"
	HostKeyPath string // OpenSSH private host key; generated if absent
	Version     string // advertised server version string
	Logger      *slog.Logger
}

// Server serves the terminal UI over SSH. It owns the session registry the
// per-user sinks fan out to; construct one, register Sink(user) on each
// engine before the engines run, then call ListenAndServe.
type Server struct {
	res  Resolver
	reg  *registry
	opts Options
	log  *slog.Logger
}

// New builds a Server. It does not bind a socket; call ListenAndServe.
func New(res Resolver, opts Options) *Server {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Server{res: res, reg: newRegistry(), opts: opts, log: log}
}

// Sink returns the user's core.Sink. Register it on that user's engine
// (before Run) so committed lines reach the user's SSH sessions.
func (s *Server) Sink(userID string) core.Sink { return &sink{reg: s.reg, user: userID} }

// authorize is the SSH public-key handler: it accepts the connection when the
// offered key maps to a user and records that user id on the context.
func (s *Server) authorize(ctx ssh.Context, key ssh.PublicKey) bool {
	userID, ok := s.res.Authorize(ctx.User(), key)
	if !ok {
		s.log.Info("ssh: rejected key", "ssh_user", ctx.User(), "remote", ctx.RemoteAddr())
		return false
	}
	ctx.SetValue(ctxUserKey{}, userID)
	return true
}

// program builds the Bubble Tea program for one authenticated session and
// registers it with the fan-out registry. The registry entry is dropped when
// the SSH context is done (the connection closed), so a late sink send after
// the program stops is a harmless no-op.
func (s *Server) program(sess ssh.Session) *tea.Program {
	userID, _ := sess.Context().Value(ctxUserKey{}).(string)
	tenant, ok := s.res.Tenant(userID)
	if userID == "" || !ok {
		wish.Fatalln(sess, "no tenant for this user")
		return nil
	}
	pty, _, _ := sess.Pty()

	renderer := bm.MakeRenderer(sess)
	m := newModel(tenant, renderer, s.log.With("user", userID), pty.Window.Width, pty.Window.Height)

	opts := append(bm.MakeOptions(sess), tea.WithAltScreen())
	p := tea.NewProgram(m, opts...)

	entry := &session{user: userID, prog: p}
	s.reg.add(entry)
	go func() {
		<-sess.Context().Done()
		s.reg.remove(entry)
	}()
	return p
}

// ListenAndServe binds the SSH listener and serves until ctx is cancelled,
// then shuts down gracefully. Blocking; run it in its own goroutine.
func (s *Server) ListenAndServe(ctx context.Context) error {
	srv, err := wish.NewServer(
		wish.WithAddress(s.opts.Addr),
		wish.WithHostKeyPath(s.opts.HostKeyPath),
		wish.WithVersion(s.opts.Version),
		wish.WithPublicKeyAuth(s.authorize),
		wish.WithMiddleware(
			bm.MiddlewareWithProgramHandler(s.program, termenv.ANSI256),
			activeterm.Middleware(), // a PTY is required; reject `ssh host cmd`
		),
	)
	if err != nil {
		return err
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	s.log.Info("ssh tui listening", "addr", s.opts.Addr)

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		return srv.Shutdown(shutCtx)
	case err := <-errc:
		if err == ssh.ErrServerClosed {
			return nil
		}
		return err
	}
}
