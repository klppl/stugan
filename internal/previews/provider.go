// Package previews implements the stugan-previews sidecar: link-preview and
// media-proxy fetching, isolated from the main stugan process so untrusted
// URLs never get near user data. The shared helpers (ParseOpenGraph,
// ValidTarget, ServeMedia) are also used by stugan's in-process fallback.
package previews

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/klippelism/stugan/internal/previewproto"
)

// Card is the preview summary; see previewproto.Card.
type Card = previewproto.Card

// ErrNotHTML is returned by the generic provider when a URL doesn't serve an
// HTML page (the server maps it to 415).
var ErrNotHTML = errors.New("not html")

// Provider turns one site's URLs into a Card. Each site is a self-contained
// type in its own file; add it to Providers to enable it.
type Provider interface {
	// Name identifies the provider in logs.
	Name() string
	// Match reports whether this provider handles u. It must be cheap and
	// do no I/O.
	Match(u *url.URL) bool
	// Fetch builds the Card. Returning an error (or an empty card) makes the
	// registry fall through to the next matching provider.
	Fetch(ctx context.Context, f *Fetcher, u *url.URL) (*Card, error)
}

// Providers builds the provider chain in priority order. Site-specific
// providers come first; the Open Graph scraper matches everything and goes
// last as the catch-all. Providers read their own settings from env.
func Providers(env Env) []Provider {
	return []Provider{
		newYouTube(env),
		newInstagram(),
		newReddit(env),
		newGitHub(env),
		newHackerNews(),
		newBluesky(),
		newWikipedia(),
		newOEmbed(),
		openGraph{},
	}
}

// Registry resolves a URL through the provider chain.
type Registry struct {
	providers []Provider
	log       *slog.Logger
}

// NewRegistry wraps a provider chain.
func NewRegistry(ps []Provider, log *slog.Logger) *Registry {
	if log == nil {
		log = slog.Default()
	}
	return &Registry{providers: ps, log: log}
}

// Resolve tries each matching provider in order and returns the first
// non-empty Card. Site providers failing is routine (rate limits, markup
// changes), so it logs at debug and falls through; the last error wins when
// every provider fails.
func (r *Registry) Resolve(ctx context.Context, f *Fetcher, raw string) (*Card, error) {
	return r.resolve(ctx, f, raw, 0)
}

func (r *Registry) resolve(ctx context.Context, f *Fetcher, raw string, depth int) (*Card, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	// Short links: describe where they lead. depth guards against a
	// shortener that redirects to another shortener forever.
	if depth == 0 && isShortener(u.Hostname()) {
		if final, err := expand(ctx, f, u); err == nil && final.String() != u.String() && !isShortener(final.Hostname()) {
			if c, err := r.resolve(ctx, f, final.String(), 1); err == nil {
				c.Details = append(c.Details, "via "+strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."))
				return c, nil
			}
		}
	}
	var lastErr error = fmt.Errorf("no provider for %s", u.Host)
	for _, p := range r.providers {
		if !p.Match(u) {
			continue
		}
		c, err := p.Fetch(ctx, f, u)
		if err == nil && c != nil && !c.Empty() {
			if c.URL == "" {
				c.URL = raw
			}
			return c, nil
		}
		if err == nil {
			err = errors.New("empty card")
		}
		r.log.Debug("preview provider failed", "provider", p.Name(), "host", u.Host, "err", err)
		lastErr = err
	}
	return nil, lastErr
}
