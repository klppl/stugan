// Command stugan-engine is the optional sidecar that holds stugan's IRC
// connections open while stugan itself restarts or upgrades. See
// docs/engine.md.
//
// It is configured entirely from the environment (flags override):
//
//	STUGAN_ENGINE_SECRET             shared secret (required, ≥16 chars)
//	STUGAN_ENGINE_LISTEN             listen address (default 127.0.0.1:8016;
//	                                 host:port, tcp://host:port, unix:///path)
//	STUGAN_ENGINE_BUFFER_BYTES       per-connection backlog cap (default 4 MiB)
//	STUGAN_ENGINE_BUFFER_TOTAL_BYTES total backlog cap (default 256 MiB)
//	STUGAN_ENGINE_ORPHAN_TIMEOUT     end a socket no stugan claimed for this
//	                                 long (Go duration, default 1h; 0 = never)
//	STUGAN_ENGINE_LOG_LEVEL          debug|info|warn|error (default info)
//	STUGAN_ENGINE_LOG_FORMAT         text|json (default text)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/klippelism/stugan/internal/engine"
	"github.com/klippelism/stugan/internal/engineproto"
	"github.com/klippelism/stugan/internal/logging"
)

const defaultListen = "127.0.0.1:8016"

// minSecretLen matches stugan's own check (config.MinEngineSecretLen).
const minSecretLen = 16

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "stugan-engine:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		listen  = flag.String("listen", envOr("STUGAN_ENGINE_LISTEN", defaultListen), "listen address (host:port, tcp://host:port or unix:///path)")
		health  = flag.Bool("health", false, "probe the engine at -listen and exit 0 if it answers (for container healthchecks)")
		showVer = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("stugan-engine", version())
		return nil
	}
	if *health {
		return probe(*listen)
	}

	log := logging.New(envOr("STUGAN_ENGINE_LOG_LEVEL", "info"), envOr("STUGAN_ENGINE_LOG_FORMAT", "text"))

	secret := os.Getenv("STUGAN_ENGINE_SECRET")
	if len(secret) < minSecretLen {
		return fmt.Errorf("$STUGAN_ENGINE_SECRET must be set to at least %d characters (try: openssl rand -hex 32)", minSecretLen)
	}
	perConn, err := envBytes("STUGAN_ENGINE_BUFFER_BYTES")
	if err != nil {
		return err
	}
	total, err := envBytes("STUGAN_ENGINE_BUFFER_TOTAL_BYTES")
	if err != nil {
		return err
	}
	orphan, err := envOrphanTimeout()
	if err != nil {
		return err
	}

	srv, err := engine.New(engine.Options{
		Secret: secret, Version: version(), Logger: log,
		BufferBytes: perConn, TotalBufferBytes: total, OrphanTimeout: orphan,
	})
	if err != nil {
		return err
	}

	network, addr, err := engineproto.ParseAddr(*listen)
	if err != nil {
		return fmt.Errorf("-listen: %w", err)
	}
	if network == "unix" {
		// A stale socket file from a previous run would make Listen fail.
		if fi, err := os.Lstat(addr); err == nil && fi.Mode()&os.ModeSocket != 0 {
			_ = os.Remove(addr)
		}
	}
	ln, err := net.Listen(network, addr)
	if err != nil {
		return err
	}
	if network == "unix" {
		// Only the owner (and group) may talk to the engine; the secret is
		// still checked on top.
		_ = os.Chmod(addr, 0o660)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Info("stugan-engine listening", "version", version(), "listen", *listen,
		"protocol", fmt.Sprintf("%d.%d", engineproto.ProtocolMajor, engineproto.ProtocolMinor),
		"orphan_timeout", orphanString(orphan))
	err = srv.Serve(ctx, ln)
	log.Info("stugan-engine stopped")
	return err
}

// probe asks the engine at listen whether it is up.
func probe(listen string) error {
	network, addr, err := engineproto.ParseAddr(listen)
	if err != nil {
		return err
	}
	if network == "tcp" {
		// A wildcard bind is probed over loopback.
		if host, port, err := net.SplitHostPort(addr); err == nil && (host == "" || host == "0.0.0.0" || host == "::") {
			addr = net.JoinHostPort("127.0.0.1", port)
		}
	}
	c, err := net.DialTimeout(network, addr, 3*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	b, _ := engineproto.Encode(&engineproto.Frame{Op: engineproto.OpHealth})
	if _, err := c.Write(b); err != nil {
		return err
	}
	f, err := engineproto.NewReader(c).Next()
	if err != nil {
		return err
	}
	if f.Op != engineproto.OpHealth {
		return fmt.Errorf("unexpected answer %q", f.Op)
	}
	return nil
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// envBytes reads a byte count (digits only). Unset means the default (0).
func envBytes(key string) (int64, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < engine.MinBufferBytes {
		return 0, fmt.Errorf("$%s must be a byte count of at least %d", key, engine.MinBufferBytes)
	}
	return n, nil
}

// envOrphanTimeout reads STUGAN_ENGINE_ORPHAN_TIMEOUT: unset is the default
// (0 → 1h in engine.Options), "0" disables reaping (negative internally).
func envOrphanTimeout() (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv("STUGAN_ENGINE_ORPHAN_TIMEOUT"))
	if v == "" {
		return 0, nil
	}
	if v == "0" {
		return -1, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, errors.New("$STUGAN_ENGINE_ORPHAN_TIMEOUT must be a positive duration (e.g. 1h, 30m) or 0 to disable")
	}
	return d, nil
}

func orphanString(d time.Duration) string {
	switch {
	case d < 0:
		return "never"
	case d == 0:
		return "1h"
	default:
		return d.String()
	}
}

// version reports the build version. Replaced by -ldflags at release time.
func version() string {
	if buildVersion != "" {
		return buildVersion
	}
	return "dev"
}

var buildVersion string
