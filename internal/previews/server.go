package previews

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/klippelism/stugan/internal/previewproto"
)

const (
	cardTTL        = time.Hour
	negativeTTL    = 5 * time.Minute
	cacheMax       = 2000
	resolveTimeout = 12 * time.Second
)

// Options configures a Server.
type Options struct {
	// Secret is the shared secret stugan presents as a Bearer token.
	Secret   string
	Registry *Registry
	Fetcher  *Fetcher
	Logger   *slog.Logger
}

// Server is the sidecar's HTTP front end.
type Server struct {
	secret   []byte
	registry *Registry
	fetcher  *Fetcher
	log      *slog.Logger

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	card    *Card // nil for a cached failure
	status  int   // HTTP status to replay for a cached failure
	expires time.Time
}

// New builds a Server. Secret must be at least previewproto.MinSecretLen.
func New(o Options) (*Server, error) {
	if len(o.Secret) < previewproto.MinSecretLen {
		return nil, errors.New("secret too short")
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Fetcher == nil {
		o.Fetcher = NewFetcher()
	}
	if o.Registry == nil {
		o.Registry = NewRegistry(Providers(OSEnv), o.Logger)
	}
	return &Server{
		secret: []byte(o.Secret), registry: o.Registry, fetcher: o.Fetcher,
		log: o.Logger, cache: map[string]cacheEntry{},
	}, nil
}

// Handler serves /v1/preview, /v1/proxy and /healthz.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(previewproto.PathHealth, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(previewproto.PathPreview, s.auth(s.handlePreview))
	mux.HandleFunc(previewproto.PathProxy, s.auth(s.handleProxy))
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(tok), s.secret) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	target, ok := ValidTarget(r.URL.Query().Get("url"))
	if !ok {
		http.Error(w, "bad url", http.StatusBadRequest)
		return
	}
	if e, hit := s.lookup(target); hit {
		s.reply(w, e)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), resolveTimeout)
	defer cancel()
	card, err := s.registry.Resolve(ctx, s.fetcher, target)
	var e cacheEntry
	switch {
	case err == nil:
		e = cacheEntry{card: card, expires: time.Now().Add(cardTTL)}
	case errors.Is(err, ErrNotHTML):
		e = cacheEntry{status: http.StatusUnsupportedMediaType, expires: time.Now().Add(negativeTTL)}
	default:
		if r.Context().Err() != nil {
			return // the caller went away; don't cache that
		}
		e = cacheEntry{status: http.StatusBadGateway, expires: time.Now().Add(negativeTTL)}
	}
	s.store(target, e)
	s.reply(w, e)
}

func (s *Server) reply(w http.ResponseWriter, e cacheEntry) {
	if e.card == nil {
		http.Error(w, http.StatusText(e.status), e.status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_ = json.NewEncoder(w).Encode(e.card)
}

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	target, ok := ValidTarget(r.URL.Query().Get("url"))
	if !ok {
		http.Error(w, "bad url", http.StatusBadRequest)
		return
	}
	ServeMedia(w, s.fetcher.Client, target)
}

func (s *Server) lookup(key string) (cacheEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.cache[key]
	if !ok || time.Now().After(e.expires) {
		delete(s.cache, key)
		return cacheEntry{}, false
	}
	return e, true
}

func (s *Server) store(key string, e cacheEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cache) >= cacheMax {
		now := time.Now()
		for k, v := range s.cache {
			if now.After(v.expires) {
				delete(s.cache, k)
			}
		}
		for k := range s.cache { // still full: evict arbitrary entries
			if len(s.cache) < cacheMax*9/10 {
				break
			}
			delete(s.cache, k)
		}
	}
	s.cache[key] = e
}
