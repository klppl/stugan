package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/klippelism/stugan/internal/core"
	"github.com/klippelism/stugan/internal/safehttp"
)

// pushManager handles Web Push: it holds the VAPID keypair and the set of
// browser subscriptions, and sends a notification when a highlight arrives
// while no browser is connected (i.e. the user is away).
type pushManager struct {
	dir     string
	pubKey  string
	privKey string
	// http is the client used to POST to push services. Endpoints are
	// user-supplied URLs, so this must be the SSRF-guarded, timeout-bounded
	// client — webpush-go's fallback is a bare http.Client that can hang a
	// notify goroutine forever on a stalled endpoint.
	http *http.Client

	mu   sync.Mutex
	subs map[string]map[string]webpush.Subscription // user → endpoint → sub
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
	p := &pushManager{dir: dir, subs: map[string]map[string]webpush.Subscription{}, http: safehttp.New()}
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
	var byUser map[string][]webpush.Subscription
	if json.Unmarshal(data, &byUser) != nil {
		return
	}
	for user, list := range byUser {
		p.subs[user] = map[string]webpush.Subscription{}
		for _, s := range list {
			p.subs[user][s.Endpoint] = s
		}
	}
}

// saveSubs writes the per-user subscriptions; callers hold p.mu. Written to
// a temp file and renamed so a crash mid-write can't truncate the file and
// silently drop every subscription on the next load.
func (p *pushManager) saveSubs() {
	byUser := map[string][]webpush.Subscription{}
	for user, m := range p.subs {
		for _, s := range m {
			byUser[user] = append(byUser[user], s)
		}
	}
	data, _ := json.Marshal(byUser)
	tmp := p.subsPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, p.subsPath())
}

func (p *pushManager) add(user string, s webpush.Subscription) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.subs[user] == nil {
		p.subs[user] = map[string]webpush.Subscription{}
	}
	p.subs[user][s.Endpoint] = s
	p.saveSubs()
}

func (p *pushManager) remove(user, endpoint string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.subs[user], endpoint)
	p.saveSubs()
}

// validPushEndpoint accepts only well-formed https URLs as subscription
// endpoints. Real push services are always https; anything else is a
// request to make the daemon POST somewhere it shouldn't (the safehttp
// dial guard additionally blocks non-public addresses at send time).
func validPushEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

// pushPayload is the JSON the service worker receives.
type pushPayload struct {
	Title   string `json:"title"`
	Body    string `json:"body"`
	Network string `json:"network"`
	Buffer  string `json:"buffer"`
}

// notify sends a payload to one user's subscriptions, pruning ones the push
// service reports as gone (404/410).
func (p *pushManager) notify(user string, pl pushPayload, log loggerW) {
	body, _ := json.Marshal(pl)
	p.mu.Lock()
	subs := make([]webpush.Subscription, 0, len(p.subs[user]))
	for _, s := range p.subs[user] {
		subs = append(subs, s)
	}
	p.mu.Unlock()

	for _, s := range subs {
		sub := s
		resp, err := webpush.SendNotification(body, &sub, &webpush.Options{
			HTTPClient:      p.http,
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
			p.remove(user, sub.Endpoint)
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
	user, ok := s.userOf(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var sub webpush.Subscription
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&sub); err != nil || !validPushEndpoint(sub.Endpoint) {
		http.Error(w, "bad subscription", http.StatusBadRequest)
		return
	}
	s.push.add(user, sub)
	w.WriteHeader(http.StatusNoContent)
}

// maybePush sends a push notification for a highlight if the user is away
// (no browser connected for that user). Called from the per-user sink; runs
// the send async so it never blocks the engine loop.
func (s *Server) maybePush(user string, m core.Message) {
	if s.push == nil || m.Self {
		return
	}
	// Notify on highlights and on direct messages (queries): a DM is
	// attention-worthy even when its text matches no highlight rule.
	if !m.Highlight && !isNotifyDM(m) {
		return
	}
	if s.connectedCount(user) > 0 {
		return // user is here; the in-app notification is enough
	}
	// The user is fully away, so honor their muted buffers here too: the
	// in-app desktopNotify already skips muted buffers client-side, but push
	// fires with no client connected, so the check has to live server-side.
	if t, ok := s.hub.Tenant(user); ok && isMuted(loadMuted(t), m.Network, m.Buffer) {
		return
	}
	title := m.From + " in " + m.Buffer
	if core.IsQueryBuffer(m.Buffer) {
		title = m.From // a DM's buffer name is just the sender's nick
	}
	go s.push.notify(user, pushPayload{
		Title:   title,
		Body:    m.Text,
		Network: m.Network,
		Buffer:  m.Buffer,
	}, s.log)
}

// isNotifyDM reports whether m is an incoming conversational line in a private
// query buffer, so it should notify even without a highlight-rule match.
func isNotifyDM(m core.Message) bool {
	return core.IsQueryBuffer(m.Buffer) &&
		(m.Kind == core.MsgPrivmsg || m.Kind == core.MsgNotice || m.Kind == core.MsgAction)
}
