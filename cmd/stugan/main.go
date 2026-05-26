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

	// Phase 1+ wires the daemon here: store.Open, irc connections, core
	// engine, plugin host, and the HTTP/WebSocket server, all sharing ctx
	// and tearing down cleanly when it is cancelled.
	log.Info("daemon ready; waiting for shutdown signal (no services wired yet — Phase 0)")

	<-ctx.Done()
	log.Info("shutdown signal received, stopping")
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
