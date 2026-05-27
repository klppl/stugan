package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/klippelism/stugan/internal/core"
)

// pushManager handles Web Push: it holds the VAPID keypair and the set of
// browser subscriptions, and sends a notification when a highlight arrives
// while no browser is connected (i.e. the user is away).
type pushManager struct {
	dir     string
	pubKey  string
	privKey string

	mu   sync.Mutex
	subs map[string]webpush.Subscription // keyed by endpoint
}

// newPushManager loads or generates VAPID keys and loads saved subscriptions
// from dir. A nil manager (dir == "") disables push.
func newPushManager(dir string) (*pushManager, error) {
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	p := &pushManager{dir: dir, subs: map[string]webpush.Subscription{}}
	if err := p.loadOrCreateKeys(); err != nil {
		return nil, err
	}
	p.loadSubs()
	return p, nil
}

func (p *pushManager) loadOrCreateKeys() error {
	path := filepath.Join(p.dir, "vapid.json")
	if data, err := os.ReadFile(path); err == nil {
		var k struct{ Public, Private string }
		if json.Unmarshal(data, &k) == nil && k.Public != "" {
			p.pubKey, p.privKey = k.Public, k.Private
			return nil
		}
	}
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return err
	}
	p.pubKey, p.privKey = pub, priv
	data, _ := json.Marshal(struct{ Public, Private string }{pub, priv})
	return os.WriteFile(path, data, 0o600)
}

func (p *pushManager) subsPath() string { return filepath.Join(p.dir, "push-subs.json") }

func (p *pushManager) loadSubs() {
	data, err := os.ReadFile(p.subsPath())
	if err != nil {
		return
	}
	var list []webpush.Subscription
	if json.Unmarshal(data, &list) != nil {
		return
	}
	for _, s := range list {
		p.subs[s.Endpoint] = s
	}
}

func (p *pushManager) saveSubs() {
	list := make([]webpush.Subscription, 0, len(p.subs))
	for _, s := range p.subs {
		list = append(list, s)
	}
	data, _ := json.Marshal(list)
	_ = os.WriteFile(p.subsPath(), data, 0o600)
}

func (p *pushManager) add(s webpush.Subscription) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subs[s.Endpoint] = s
	p.saveSubs()
}

func (p *pushManager) remove(endpoint string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.subs, endpoint)
	p.saveSubs()
}

// pushPayload is the JSON the service worker receives.
type pushPayload struct {
	Title   string `json:"title"`
	Body    string `json:"body"`
	Network string `json:"network"`
	Buffer  string `json:"buffer"`
}

// notify sends a payload to every subscription, pruning ones the push
// service reports as gone (404/410).
func (p *pushManager) notify(pl pushPayload, log loggerW) {
	body, _ := json.Marshal(pl)
	p.mu.Lock()
	subs := make([]webpush.Subscription, 0, len(p.subs))
	for _, s := range p.subs {
		subs = append(subs, s)
	}
	p.mu.Unlock()

	for _, s := range subs {
		sub := s
		resp, err := webpush.SendNotification(body, &sub, &webpush.Options{
			Subscriber:      "stugan@localhost",
			VAPIDPublicKey:  p.pubKey,
			VAPIDPrivateKey: p.privKey,
			TTL:             60,
		})
		if err != nil {
			log.Warn("push send failed", "err", err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
			p.remove(sub.Endpoint)
		}
	}
}

// loggerW is the slice of *slog.Logger this file needs.
type loggerW interface {
	Warn(msg string, args ...any)
}

// --- HTTP handlers ---------------------------------------------------------

// handleVAPID returns the public VAPID key the browser subscribes with.
func (s *Server) handleVAPID(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"key": s.push.pubKey})
}

// handleSubscribe stores a browser push subscription.
func (s *Server) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var sub webpush.Subscription
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&sub); err != nil || sub.Endpoint == "" {
		http.Error(w, "bad subscription", http.StatusBadRequest)
		return
	}
	s.push.add(sub)
	w.WriteHeader(http.StatusNoContent)
}

// maybePush sends a push notification for a highlight if the user is away
// (no browser connected). Called from the sink path; runs the send async so
// it never blocks the engine loop.
func (s *Server) maybePush(m core.Message) {
	if s.push == nil || !m.Highlight || m.Self {
		return
	}
	s.mu.Lock()
	connected := len(s.clients)
	s.mu.Unlock()
	if connected > 0 {
		return // user is here; the in-app highlight is enough
	}
	pl := pushPayload{
		Title:   m.From + " in " + m.Buffer,
		Body:    m.Text,
		Network: m.Network,
		Buffer:  m.Buffer,
	}
	go s.push.notify(pl, s.log)
}
