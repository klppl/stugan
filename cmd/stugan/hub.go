package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/klippelism/stugan/internal/auth"
	"github.com/klippelism/stugan/internal/config"
	"github.com/klippelism/stugan/internal/core"
	"github.com/klippelism/stugan/internal/irc"
	"github.com/klippelism/stugan/internal/plugin"
	"github.com/klippelism/stugan/internal/safehttp"
	"github.com/klippelism/stugan/internal/scripts"
	"github.com/klippelism/stugan/internal/server"
	"github.com/klippelism/stugan/internal/store"
	"github.com/klippelism/stugan/internal/tui"
)

// installBuiltinScripts copies any bundled scripts (currently just fish.lua
// for FiSH Blowfish encryption) into the user's scripts directory on a
// fresh install. Idempotent: an existing file is left alone, so a user who
// edits or deletes a bundled script keeps their version across restarts.
func installBuiltinScripts(scriptsDir string, log *slog.Logger) {
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		log.Warn("create scripts dir", "dir", scriptsDir, "err", err)
		return
	}
	for name, body := range scripts.Builtins {
		p := filepath.Join(scriptsDir, name)
		if _, err := os.Stat(p); err == nil {
			continue // user already has it
		} else if !errors.Is(err, os.ErrNotExist) {
			log.Warn("stat builtin script", "name", name, "err", err)
			continue
		}
		if err := os.WriteFile(p, body, 0o644); err != nil {
			log.Warn("install builtin script", "name", name, "err", err)
			continue
		}
		log.Info("installed builtin script", "path", p)
	}
}

// pluginKV adapts *store.Store to plugin.KV. The interface is intentionally
// narrow (just script-scoped get/set/delete) so the plugin package never
// needs to import store.
type pluginKV struct{ s *store.Store }

func (p pluginKV) GetAll(script string) map[string]string { return p.s.PluginKVGetAll(script) }
func (p pluginKV) Set(script, key, value string) error    { return p.s.PluginKVSet(script, key, value) }
func (p pluginKV) Delete(script, key string) error        { return p.s.PluginKVDelete(script, key) }

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
	// Validate the config highlight rules up front; they seed any user who
	// hasn't customized their own (a per-user override lives in that user's
	// store, set from the settings UI — see the loop below).
	if _, err := core.NewHighlighter(cfg.Highlight.Patterns, cfg.Highlight.Exceptions); err != nil {
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

	// Plugins are sandboxed by default; multi-user is always sandboxed and
	// single-user can opt out with `sandbox = false` (see PluginSandbox).
	sandbox := cfg.PluginSandbox()

	for _, u := range cfg.EffectiveUsers() {
		dataDir, scriptsDir := userDirs(cfg, u.Name)
		for _, d := range []string{dataDir, scriptsDir} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return nil, nil, fmt.Errorf("user %q dir %s: %w", u.Name, d, err)
			}
		}
		installBuiltinScripts(scriptsDir, log.With("user", u.Name))
		db, err := store.Open(filepath.Join(dataDir, "stugan.db"), log)
		if err != nil {
			return nil, nil, fmt.Errorf("user %q store: %w", u.Name, err)
		}
		h.stores = append(h.stores, db)

		// Per-user highlight rules: a stored override (set from the settings UI)
		// wins; otherwise fall back to the config defaults. Both were validated
		// above / on save, so a compile error here means corrupt stored JSON —
		// log it and fall back rather than refusing to start.
		highlighter := userHighlighter(db, cfg, log.With("user", u.Name))
		aliases := userAliases(db, cfg, log.With("user", u.Name))

		connector := ircConnector{log: log.With("user", u.Name)}
		eng := core.New(core.Options{
			Logger:    log.With("user", u.Name),
			Highlight: highlighter,
			Aliases:   aliases,
			User:      &core.User{ID: u.Name, Name: u.Name},
			Connector: connector,
			Networks:  db, // persist GUI-added networks
			History:   db, // history backlog reader for plugins
		})
		eng.AddSink(db)

		if cfg.PluginsEnabled() {
			host, err := plugin.New(plugin.Options{
				API:      eng.API(),
				Logger:   log.With("user", u.Name),
				Dir:      scriptsDir,
				Settings: cfg.Plugins.Settings,
				Sandbox:  sandbox,
				KV:       pluginKV{db},
				HTTP:     safehttp.New(), // stugan.http, SSRF-guarded
			})
			if err != nil {
				return nil, nil, err
			}
			eng.SetHost(host)
		}

		// The store is the source of truth for networks. On first run it is
		// seeded from the config's auto-connect networks; thereafter networks
		// are managed from the GUI (add/remove are persisted).
		nets, err := db.Networks()
		if err != nil {
			return nil, nil, fmt.Errorf("user %q load networks: %w", u.Name, err)
		}
		if len(nets) == 0 {
			for _, n := range u.Networks {
				if !n.Connect {
					continue
				}
				p := paramsFromConfig(n, log.With("user", u.Name))
				if err := db.SaveNetwork(p); err != nil {
					return nil, nil, err
				}
				nets = append(nets, p)
			}
		}
		// Restore the user's manual sidebar order: networks load lowest Pos
		// first. Store.Networks() returns them id-ordered, a stable tiebreaker
		// for legacy rows that predate ordering (Pos == 0).
		slices.SortStableFunc(nets, func(a, b core.NetworkParams) int {
			return a.Pos - b.Pos
		})
		for _, p := range nets {
			conn, err := connector.Dial(p, eng)
			if err != nil {
				return nil, nil, fmt.Errorf("user %q network %q: %w", u.Name, p.Name, err)
			}
			eng.AddNetwork(p, conn)
		}

		h.engines[u.Name] = eng
		h.tenants[u.Name] = &server.Tenant{Engine: eng, History: db, Prefs: db}
	}

	cleanup := func() {
		for _, db := range h.stores {
			db.Close()
		}
	}
	return h, cleanup, nil
}

// userHighlighter builds a user's highlighter from their stored override (the
// "highlight" pref, set via the settings UI) when present, falling back to the
// config defaults. A stored value that fails to parse or compile is logged and
// ignored so a corrupt blob never blocks startup.
func userHighlighter(db *store.Store, cfg *config.Config, log *slog.Logger) *core.Highlighter {
	patterns, exceptions := cfg.Highlight.Patterns, cfg.Highlight.Exceptions
	if v, err := db.Pref("highlight"); err != nil {
		log.Warn("read highlight pref", "err", err)
	} else if v != "" {
		var r struct {
			Patterns   []string `json:"patterns"`
			Exceptions []string `json:"exceptions"`
		}
		if err := json.Unmarshal([]byte(v), &r); err != nil {
			log.Warn("parse highlight pref", "err", err)
		} else {
			patterns, exceptions = r.Patterns, r.Exceptions
		}
	}
	hl, err := core.NewHighlighter(patterns, exceptions)
	if err != nil {
		log.Warn("compile highlight pref; using defaults", "err", err)
		hl, _ = core.NewHighlighter(cfg.Highlight.Patterns, cfg.Highlight.Exceptions)
	}
	return hl
}

// userAliases builds a user's command-alias table from their stored override
// (the "aliases" pref, set via the settings UI) when present, falling back to
// the config aliases. A stored value that fails to parse is logged and ignored
// so a corrupt blob never blocks startup.
func userAliases(db *store.Store, cfg *config.Config, log *slog.Logger) map[string]string {
	if v, err := db.Pref("aliases"); err != nil {
		log.Warn("read aliases pref", "err", err)
	} else if v != "" {
		var m map[string]string
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			log.Warn("parse aliases pref; using config", "err", err)
		} else {
			return m
		}
	}
	return cfg.Aliases
}

// registerSinks wires the server's per-user sink onto each engine. Call
// before running the engines.
func (h *hub) registerSinks(srv *server.Server) {
	for id, eng := range h.engines {
		eng.AddSink(srv.Sink(id))
	}
}

// registerTUISinks wires the SSH TUI server's per-user sink onto each engine,
// so committed lines reach SSH sessions too. Like registerSinks, call before
// running the engines: AddSink mutates the engine's sink slice, which must be
// stable once the loop goroutine starts.
func (h *hub) registerTUISinks(srv *tui.Server) {
	for id, eng := range h.engines {
		eng.AddSink(srv.Sink(id))
	}
}

// ircConnector builds core connections from params, wrapping internal/irc so
// core can dial at runtime without importing the IRC library.
type ircConnector struct{ log *slog.Logger }

func (c ircConnector) Dial(p core.NetworkParams, h core.ConnHandler) (core.IRCConn, error) {
	return irc.New(irc.Options{
		Network: p.ID, Addr: p.Addr, Fallbacks: p.Fallbacks,
		TLS: p.TLS, Insecure: p.Insecure,
		Nick: p.Nick, User: p.User, Realname: p.Realname,
		SASLUser: p.SASLUser, SASLPass: p.SASLPass,
		ServerPass: p.ServerPass, SASLExternal: p.SASLExternal, CertPEM: p.CertPEM,
		Channels: p.Channels, ChannelKeys: p.ChannelKeys, Monitor: p.Monitor, Logger: c.log,
	}, h)
}

// paramsFromConfig converts a config network into runtime params. A
// configured cert_file is read into CertPEM (and persisted thereafter); a
// missing/unreadable file is logged and left empty so startup still proceeds.
func paramsFromConfig(n config.NetworkConfig, log *slog.Logger) core.NetworkParams {
	certPEM := ""
	if n.CertFile != "" {
		b, err := os.ReadFile(n.CertFile)
		if err != nil {
			log.Warn("read network cert_file", "network", n.Name, "path", n.CertFile, "err", err)
		} else {
			certPEM = string(b)
		}
	}
	return core.NetworkParams{
		ID: n.Name, Name: n.Name, Addr: n.Addr, Fallbacks: n.Fallbacks,
		TLS: n.TLS, Insecure: n.Insecure,
		Nick: n.Nick, User: n.User, Realname: n.Realname,
		SASLUser: n.SASLUser, SASLPass: n.SASLPass, Channels: n.Channels,
		Monitor:    n.Monitor,
		ServerPass: n.ServerPass, Perform: n.Perform,
		SASLExternal: n.SASLExternal, CertPEM: certPEM,
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

// pruneHistoryLoop deletes messages older than the retention window from
// every user's store: once at startup, then hourly until ctx ends. Runs
// only when [history] retention_days > 0.
func (h *hub) pruneHistoryLoop(ctx context.Context, days int, log *slog.Logger) {
	prune := func() {
		before := time.Now().AddDate(0, 0, -days)
		for _, db := range h.stores {
			n, err := db.Prune(ctx, before)
			if err != nil {
				log.Warn("history prune failed", "err", err)
			} else if n > 0 {
				log.Info("history pruned", "rows", n, "older_than_days", days)
			}
		}
	}
	prune()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			prune()
		}
	}
}
