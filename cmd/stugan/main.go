// Command stugan is the self-hosted web IRC client daemon: it owns IRC
// connections, buffers history, and serves a browser frontend over
// WebSocket. See the brief in docs/ for the full architecture.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/klippelism/stugan/internal/auth"
	"github.com/klippelism/stugan/internal/config"
	"github.com/klippelism/stugan/internal/logging"
	"github.com/klippelism/stugan/internal/server"
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
		hashPw     = flag.Bool("hashpw", false, "read a password from stdin and print its bcrypt hash")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("stugan", version())
		return nil
	}

	if *hashPw {
		fmt.Fprint(os.Stderr, "password: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		hash, err := auth.HashPassword(strings.TrimRight(line, "\r\n"))
		if err != nil {
			return err
		}
		fmt.Println(hash)
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

	// Build one engine + store (+ plugin host) per user, plus auth/sessions.
	hub, cleanup, err := buildHub(cfg, log)
	if err != nil {
		return err
	}
	defer cleanup()

	srv := server.New(hub, server.Options{
		Logger:         log,
		ServerName:     "stugan/" + version(),
		StaticDir:      cfg.Server.StaticDir,
		OriginPatterns: cfg.Server.OriginPatterns,
		UploadDir:      filepath.Join(cfg.DataDir(), "uploads"),
		PushDir:        filepath.Join(cfg.DataDir(), "push"),
	})
	hub.registerSinks(srv)

	log.Info("daemon ready", "users", len(hub.Users()), "auth", cfg.AuthEnabled())

	// Run every user's engine and the HTTP server concurrently; if any
	// returns, cancel the shared context so the rest unwind, then surface the
	// first error.
	var wg sync.WaitGroup
	errc := make(chan error, 1)
	fail := func(err error) {
		if err != nil {
			select {
			case errc <- err:
			default:
			}
		}
	}
	for _, eng := range hub.engines {
		wg.Go(func() { defer cancel(); fail(eng.Run(ctx)) })
	}
	wg.Go(func() { defer cancel(); fail(srv.ListenAndServe(ctx, cfg.Server.Listen)) })
	wg.Wait()

	log.Info("shutdown complete")
	select {
	case err := <-errc:
		return err
	default:
		return nil
	}
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
