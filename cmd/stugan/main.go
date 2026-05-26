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
	"path/filepath"
	"sync"
	"syscall"

	"github.com/klippelism/stugan/internal/config"
	"github.com/klippelism/stugan/internal/core"
	"github.com/klippelism/stugan/internal/irc"
	"github.com/klippelism/stugan/internal/logging"
	"github.com/klippelism/stugan/internal/server"
	"github.com/klippelism/stugan/internal/store"
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

	// Root context cancelled on SIGINT/SIGTERM, or when either long-running
	// service exits, so the whole daemon tears down together.
	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(sigCtx)
	defer cancel()

	// Open the SQLite history store; it persists every committed line.
	db, err := store.Open(filepath.Join(cfg.DataDir(), "stugan.db"), log)
	if err != nil {
		return err
	}
	defer db.Close()

	// Build the core engine, the WebSocket server bridge, and a connection
	// per configured network. The server and store are registered as engine
	// sinks so committed lines fan out to browsers and to disk; the default
	// terminal sink stays on for headless visibility.
	engine := core.New(core.Options{Logger: log})
	engine.AddSink(db)

	srv := server.New(engine, server.Options{
		Logger:         log,
		ServerName:     "stugan/" + version(),
		StaticDir:      cfg.Server.StaticDir,
		OriginPatterns: cfg.Server.OriginPatterns,
	})
	engine.AddSink(srv)

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

	// Run the engine and HTTP server concurrently; if either returns, cancel
	// the shared context so the other unwinds, then surface the first error.
	var wg sync.WaitGroup
	var engErr, srvErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer cancel()
		engErr = engine.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		defer cancel()
		srvErr = srv.ListenAndServe(ctx, cfg.Server.Listen)
	}()
	wg.Wait()

	log.Info("shutdown complete")
	if srvErr != nil {
		return srvErr
	}
	return engErr
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
