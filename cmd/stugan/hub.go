package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/klippelism/stugan/internal/auth"
	"github.com/klippelism/stugan/internal/config"
	"github.com/klippelism/stugan/internal/core"
	"github.com/klippelism/stugan/internal/irc"
	"github.com/klippelism/stugan/internal/plugin"
	"github.com/klippelism/stugan/internal/server"
	"github.com/klippelism/stugan/internal/store"
)

// hub is the composition root's implementation of server.Hub: it owns one
// engine + store (+ plugin host) per user and the auth/session machinery.
type hub struct {
	authEnabled bool
	users       *auth.Users
	sessions    *auth.Sessions
	sessMaxAge  int

	engines map[string]*core.Engine
	stores  []*store.Store
	tenants map[string]*server.Tenant
}

func (h *hub) AuthEnabled() bool { return h.authEnabled }

func (h *hub) Login(username, password string) (string, bool) {
	if h.users != nil && h.users.Verify(username, password) {
		return username, true
	}
	return "", false
}

func (h *hub) Session(token string) (string, bool) {
	if h.sessions == nil {
		return "", false
	}
	return h.sessions.Lookup(token)
}

func (h *hub) StartSession(userID string) (string, int) {
	return h.sessions.Create(userID), h.sessMaxAge
}

func (h *hub) EndSession(token string) {
	if h.sessions != nil {
		h.sessions.Delete(token)
	}
}

func (h *hub) Tenant(userID string) (*server.Tenant, bool) {
	t, ok := h.tenants[userID]
	return t, ok
}

func (h *hub) Users() []string {
	out := make([]string, 0, len(h.tenants))
	for id := range h.tenants {
		out = append(out, id)
	}
	return out
}

// buildHub constructs every user's engine/store/connections/plugin host. It
// returns the hub and a cleanup that closes the stores. Engines are not yet
// running and have no server sink — the caller registers sinks then runs
// hub.Engines().
func buildHub(cfg *config.Config, log *slog.Logger) (*hub, func(), error) {
	highlighter, err := core.NewHighlighter(cfg.Highlight.Patterns, cfg.Highlight.Exceptions)
	if err != nil {
		return nil, nil, err
	}

	h := &hub{
		authEnabled: cfg.AuthEnabled(),
		engines:     map[string]*core.Engine{},
		tenants:     map[string]*server.Tenant{},
		sessMaxAge:  sessionMaxAge(cfg),
	}
	if h.authEnabled {
		hashes := map[string]string{}
		for _, u := range cfg.Users {
			hashes[u.Name] = u.PasswordHash
		}
		h.users = auth.NewUsers(hashes)
		h.sessions = auth.NewSessions(time.Duration(h.sessMaxAge) * time.Second)
	}

	// Plugins are sandboxed by default once auth (multi-user) is on.
	sandbox := cfg.Plugins.Sandbox || h.authEnabled

	for _, u := range cfg.EffectiveUsers() {
		dataDir, scriptsDir := userDirs(cfg, u.Name)
		for _, d := range []string{dataDir, scriptsDir} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return nil, nil, fmt.Errorf("user %q dir %s: %w", u.Name, d, err)
			}
		}
		db, err := store.Open(filepath.Join(dataDir, "stugan.db"), log)
		if err != nil {
			return nil, nil, fmt.Errorf("user %q store: %w", u.Name, err)
		}
		h.stores = append(h.stores, db)

		eng := core.New(core.Options{
			Logger:    log.With("user", u.Name),
			Highlight: highlighter,
			Aliases:   cfg.Aliases,
			User:      &core.User{ID: u.Name, Name: u.Name},
		})
		eng.AddSink(db)

		if cfg.Plugins.Enabled {
			host, err := plugin.New(plugin.Options{
				API:      eng.API(),
				Logger:   log.With("user", u.Name),
				Dir:      scriptsDir,
				Settings: cfg.Plugins.Settings,
				Sandbox:  sandbox,
			})
			if err != nil {
				return nil, nil, err
			}
			eng.SetHost(host)
		}

		for _, n := range u.Networks {
			if !n.Connect {
				log.Info("network configured but not auto-connecting", "user", u.Name, "network", n.Name)
				continue
			}
			conn, err := irc.New(irc.Options{
				Network: n.Name, Addr: n.Addr, TLS: n.TLS,
				Nick: n.Nick, User: n.User, Realname: n.Realname,
				SASLUser: n.SASLUser, SASLPass: n.SASLPass,
				Channels: n.Channels, Logger: log,
			}, eng)
			if err != nil {
				return nil, nil, fmt.Errorf("user %q network %q: %w", u.Name, n.Name, err)
			}
			eng.AddNetwork(core.NetworkSpec{ID: n.Name, Name: n.Name, Nick: n.Nick}, conn)
		}

		h.engines[u.Name] = eng
		h.tenants[u.Name] = &server.Tenant{Engine: eng, History: db}
	}

	cleanup := func() {
		for _, db := range h.stores {
			db.Close()
		}
	}
	return h, cleanup, nil
}

// registerSinks wires the server's per-user sink onto each engine. Call
// before running the engines.
func (h *hub) registerSinks(srv *server.Server) {
	for id, eng := range h.engines {
		eng.AddSink(srv.Sink(id))
	}
}

func sessionMaxAge(cfg *config.Config) int {
	hours := cfg.Auth.SessionHours
	if hours <= 0 {
		hours = 30 * 24
	}
	return hours * 3600
}

// userDirs returns the data and scripts directories for a user. The single
// implicit user keeps the legacy top-level paths; named users are isolated
// under users/<name>/.
func userDirs(cfg *config.Config, name string) (dataDir, scriptsDir string) {
	if !cfg.AuthEnabled() {
		return cfg.DataDir(), cfg.ScriptsDir()
	}
	base := filepath.Join(cfg.Home(), "users", name)
	return filepath.Join(base, "data"), filepath.Join(base, "scripts")
}
