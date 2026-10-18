// Command stugan-previews is the optional sidecar that fetches link previews
// and proxies media for stugan, isolated from the main process so untrusted
// URLs never touch user data. See docs/previews.md.
//
// It is configured entirely from the environment (flags override):
//
//	STUGAN_PREVIEWS_SECRET            shared secret (required, ≥16 chars)
//	STUGAN_PREVIEWS_LISTEN            listen address (default 127.0.0.1:8017)
//	STUGAN_PREVIEWS_LOG_LEVEL         debug|info|warn|error (default info)
//	STUGAN_PREVIEWS_LOG_FORMAT        text|json (default text)
//
// Provider settings follow STUGAN_PREVIEWS_<PROVIDER>_<NAME>, and each also
// accepts a <VAR>_FILE variant pointing at a secret file:
//
//	STUGAN_PREVIEWS_YOUTUBE_API_KEY   YouTube Data API v3 key (optional;
//	                                  without it YouTube uses keyless oEmbed)
//	STUGAN_PREVIEWS_GITHUB_TOKEN      GitHub token (optional; raises the API
//	                                  rate limit from 60 to 5,000 an hour)
//	STUGAN_PREVIEWS_REDDIT_CLIENT_ID  Reddit app client id (optional; with
//	STUGAN_PREVIEWS_REDDIT_CLIENT_SECRET / _USER_AGENT; without them Reddit
//	                                  uses keyless oEmbed: title + subreddit)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/klippelism/stugan/internal/logging"
	"github.com/klippelism/stugan/internal/previewproto"
	"github.com/klippelism/stugan/internal/previews"
)

// buildVersion is set by the Dockerfile via -ldflags.
var buildVersion = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "stugan-previews:", err)
		os.Exit(1)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func run() error {
	var (
		listen  = flag.String("listen", envOr("STUGAN_PREVIEWS_LISTEN", previewproto.DefaultListen), "listen address (host:port)")
		health  = flag.Bool("health", false, "probe /healthz at -listen and exit 0 if it answers (for container healthchecks)")
		showVer = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("stugan-previews", buildVersion)
		return nil
	}
	if *health {
		return probe(*listen)
	}

	log := logging.New(envOr("STUGAN_PREVIEWS_LOG_LEVEL", "info"), envOr("STUGAN_PREVIEWS_LOG_FORMAT", "text"))
	secret := previews.OSEnv("STUGAN_PREVIEWS_SECRET")
	if len(secret) < previewproto.MinSecretLen {
		return fmt.Errorf("$STUGAN_PREVIEWS_SECRET must be set to at least %d characters (try: openssl rand -hex 32)", previewproto.MinSecretLen)
	}

	providers := previews.Providers(previews.OSEnv)
	names := make([]string, 0, len(providers))
	for _, p := range providers {
		names = append(names, p.Name())
	}
	srv, err := previews.New(previews.Options{
		Secret: secret, Logger: log,
		Registry: previews.NewRegistry(providers, log),
	})
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	hs := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(sc)
	}()
	log.Info("stugan-previews listening", "version", buildVersion, "listen", *listen, "providers", names)
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("stugan-previews stopped")
	return nil
}

// probe does an unauthenticated GET /healthz.
func probe(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get("http://" + net.JoinHostPort(host, port) + previewproto.PathHealth)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz: status %d", resp.StatusCode)
	}
	return nil
}
