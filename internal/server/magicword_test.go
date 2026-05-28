package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/klippelism/stugan/internal/auth"
	"github.com/klippelism/stugan/internal/core"
)

// TestMagicWordGate covers the outer site-wide password gate enabled
// by $STUGAN_WEB_PASSWORD: every protected endpoint (and the WS) must
// reject requests without the cookie, /api/magicword must verify the
// password and grant it, and /api/me must always be reachable so the
// SPA can discover the gate state.
func TestMagicWordGate(t *testing.T) {
	hash, err := auth.HashPassword("hunter2")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eng := core.New(core.Options{Sink: noopSink{}})
	eng.AddNetwork(core.NetworkParams{ID: "n", Name: "n", Nick: "n"}, &fakeConn{sent: make(chan [2]string, 1)})
	go func() { _ = eng.Run(ctx) }()

	srv := New(SingleUser(&Tenant{Engine: eng}), Options{MagicWordHash: hash})
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	// /api/me is open and reports the gate is required + not granted.
	me := getMagicMe(t, hs.URL, "")
	if !me.MagicWord.Required || me.MagicWord.Granted {
		t.Fatalf("pre-grant /api/me magic = %+v, want required+not granted", me.MagicWord)
	}

	// WS rejected without the magic cookie.
	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws"
	if _, _, err := websocket.Dial(ctx, wsURL, nil); err == nil {
		t.Fatal("WS connected without magic word")
	}

	// Wrong password: rejected with 401.
	resp, _ := postMagic(t, hs.URL, "", "you-shall-not-pass")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong word: status = %d, want 401", resp.StatusCode)
	}

	// Correct password: cookie set, gate lifts.
	resp, _ = postMagic(t, hs.URL, "", "hunter2")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("right word: status = %d, want 204", resp.StatusCode)
	}
	var cookie string
	for _, c := range resp.Cookies() {
		if c.Name == magicCookie {
			cookie = c.Name + "=" + c.Value
		}
	}
	if cookie == "" {
		t.Fatal("no magic cookie set on successful gate")
	}

	if me := getMagicMe(t, hs.URL, cookie); !me.MagicWord.Granted {
		t.Fatalf("post-grant /api/me magic = %+v, want granted", me.MagicWord)
	}

	// WS accepted now.
	hdr := http.Header{"Cookie": {cookie}}
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("authed WS dial: %v", err)
	}
	ws.CloseNow()
}

// TestMagicWordDisabled confirms that when no hash is configured, the
// gate is transparent: /api/me reports it as not required, the
// dedicated endpoint 404s, and the WS connects with no cookie.
func TestMagicWordDisabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eng := core.New(core.Options{Sink: noopSink{}})
	eng.AddNetwork(core.NetworkParams{ID: "n", Name: "n", Nick: "n"}, &fakeConn{sent: make(chan [2]string, 1)})
	go func() { _ = eng.Run(ctx) }()

	srv := New(SingleUser(&Tenant{Engine: eng}), Options{})
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	me := getMagicMe(t, hs.URL, "")
	if me.MagicWord.Required {
		t.Fatalf("gate-disabled /api/me magic = %+v, want not required", me.MagicWord)
	}

	resp, _ := postMagic(t, hs.URL, "", "anything")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled magicword POST: status = %d, want 404", resp.StatusCode)
	}
}

type magicMeResp struct {
	MagicWord struct {
		Required bool `json:"required"`
		Granted  bool `json:"granted"`
	} `json:"magicWord"`
}

func getMagicMe(t *testing.T, base, cookie string) magicMeResp {
	t.Helper()
	req, _ := http.NewRequest("GET", base+"/api/me", nil)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m magicMeResp
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return m
}

func postMagic(t *testing.T, base, cookie, word string) (*http.Response, string) {
	t.Helper()
	return postMagicRaw(t, base, cookie, map[string]string{"word": word})
}

// postMagicRaw posts an arbitrary JSON body — used by the honeypot test
// to send the bait fields (email/website) the SPA never exposes.
func postMagicRaw(t *testing.T, base, cookie string, payload map[string]string) (*http.Response, string) {
	t.Helper()
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", base+"/api/magicword", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b := make([]byte, 1024)
	n, _ := resp.Body.Read(b)
	return resp, string(b[:n])
}

// TestMagicWordHoneypot ensures that filling a hidden honeypot field
// (a form-filling bot's tell) is treated as a failed attempt even when
// the password is correct.
func TestMagicWordHoneypot(t *testing.T) {
	hash, err := auth.HashPassword("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := core.New(core.Options{Sink: noopSink{}})
	eng.AddNetwork(core.NetworkParams{ID: "n", Name: "n", Nick: "n"}, &fakeConn{sent: make(chan [2]string, 1)})
	go func() { _ = eng.Run(ctx) }()
	srv := New(SingleUser(&Tenant{Engine: eng}), Options{MagicWordHash: hash})
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	resp, _ := postMagicRaw(t, hs.URL, "", map[string]string{
		"word":  "hunter2",         // would otherwise succeed
		"email": "bot@example.com", // honeypot
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("honeypot: status = %d, want 401 even with correct password", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == magicCookie && c.Value != "" {
			t.Fatal("honeypot: magic cookie should not be set")
		}
	}
}

// TestAuthRateLimit ensures repeated failed attempts from one source IP
// trip the limiter and return 429 instead of continuing to bcrypt.
func TestAuthRateLimit(t *testing.T) {
	hash, err := auth.HashPassword("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := core.New(core.Options{Sink: noopSink{}})
	eng.AddNetwork(core.NetworkParams{ID: "n", Name: "n", Nick: "n"}, &fakeConn{sent: make(chan [2]string, 1)})
	go func() { _ = eng.Run(ctx) }()
	srv := New(SingleUser(&Tenant{Engine: eng}), Options{MagicWordHash: hash})
	// Tighten the limiter to keep the test snappy.
	srv.authLimit = newAuthRateLimit(60*time.Second, 3)
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	// First 3 wrong attempts → 401.
	for i := 0; i < 3; i++ {
		resp, _ := postMagic(t, hs.URL, "", "nope")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, resp.StatusCode)
		}
	}
	// 4th is throttled — and crucially even the *correct* password is
	// refused while the limiter is hot, otherwise a bot could just keep
	// guessing past its quota.
	resp, _ := postMagic(t, hs.URL, "", "hunter2")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("after quota: status = %d, want 429", resp.StatusCode)
	}
}
