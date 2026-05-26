// Command stugan is the self-hosted web IRC client daemon: it owns IRC
// connections, buffers history, and serves a browser frontend over
// WebSocket. See the brief in docs/ for the full architecture.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/klippelism/stugan/internal/config"
	"github.com/klippelism/stugan/internal/core"
	"github.com/klippelism/stugan/internal/irc"
	"github.com/klippelism/stugan/internal/logging"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "stugan:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configHome = flag.String("home", "", "config/data/scripts root (overrides $STUGAN_HOME)")
		showVer    = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("stugan", version())
		return nil
	}

	// Resolve config home: explicit flag wins over env/XDG defaults.
	if *configHome != "" {
		if err := os.Setenv("STUGAN_HOME", *configHome); err != nil {
			return err
		}
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.EnsureDirs(); err != nil {
		return err
	}

	log := logging.New(cfg.Log.Level, cfg.Log.Format)
	log.Info("starting stugan",
		"version", version(),
		"home", cfg.Home(),
		"listen", cfg.Server.Listen,
		"networks", len(cfg.Networks),
		"plugins_enabled", cfg.Plugins.Enabled,
	)

	// Root context cancelled on SIGINT/SIGTERM for graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Build the core engine and attach a connection per configured network.
	// Phase 2+ adds the store sink and the HTTP/WebSocket server here; for
	// now the engine's default sink prints buffer activity to the terminal.
	engine := core.New(core.Options{Logger: log})

	for _, n := range cfg.Networks {
		if !n.Connect {
			log.Info("network configured but not auto-connecting", "network", n.Name)
			continue
		}
		conn, err := irc.New(irc.Options{
			Network:  n.Name,
			Addr:     n.Addr,
			TLS:      n.TLS,
			Nick:     n.Nick,
			User:     n.User,
			Realname: n.Realname,
			SASLUser: n.SASLUser,
			SASLPass: n.SASLPass,
			Channels: n.Channels,
			Logger:   log,
		}, engine)
		if err != nil {
			return fmt.Errorf("network %q: %w", n.Name, err)
		}
		engine.AddNetwork(core.NetworkSpec{ID: n.Name, Name: n.Name, Nick: n.Nick}, conn)
	}

	if len(cfg.Networks) == 0 {
		log.Warn("no networks configured; daemon will idle until shutdown")
	}

	log.Info("daemon ready")
	if err := engine.Run(ctx); err != nil {
		return err
	}
	log.Info("shutdown complete")
	return nil
}

// version reports the build version. Replaced by -ldflags at release time.
func version() string {
	if v := buildVersion; v != "" {
		return v
	}
	return "dev"
}

// buildVersion is injected via -ldflags "-X main.buildVersion=...".
var buildVersion string
