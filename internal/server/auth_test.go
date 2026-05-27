package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/klippelism/stugan/internal/core"
)

// fakeHub is a minimal multi-user Hub for tests.
type fakeHub struct {
	creds   map[string]string  // user → password
	tenants map[string]*Tenant // user → tenant
	sess    map[string]string  // token → user
	n       int
}

func (h *fakeHub) AuthEnabled() bool { return true }
func (h *fakeHub) Login(u, p string) (string, bool) {
	if h.creds[u] == p && p != "" {
		return u, true
	}
	return "", false
}
func (h *fakeHub) Session(tok string) (string, bool) { u, ok := h.sess[tok]; return u, ok }
func (h *fakeHub) StartSession(u string) (string, int) {
	h.n++
	tok := "tok" + string(rune('0'+h.n))
	h.sess[tok] = u
	return tok, 3600
}
func (h *fakeHub) EndSession(tok string)           { delete(h.sess, tok) }
func (h *fakeHub) Tenant(u string) (*Tenant, bool) { t, ok := h.tenants[u]; return t, ok }
func (h *fakeHub) Users() []string                 { return []string{"alice", "bob"} }

func startEngine(t *testing.T, ctx context.Context, network string) *core.Engine {
	eng := core.New(core.Options{Sink: noopSink{}})
	eng.AddNetwork(core.NetworkSpec{ID: network, Name: network, Nick: "me"}, &fakeConn{sent: make(chan [2]string, 1)})
	go func() { _ = eng.Run(ctx) }()
	return eng
}

func TestAuthAndIsolation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hub := &fakeHub{
		creds: map[string]string{"alice": "pw-a", "bob": "pw-b"},
		sess:  map[string]string{},
		tenants: map[string]*Tenant{
			"alice": {Engine: startEngine(t, ctx, "alicenet")},
			"bob":   {Engine: startEngine(t, ctx, "bobnet")},
		},
	}
	srv := New(hub, Options{})
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	// /api/me unauthenticated.
	me := getMe(t, hs.URL, "")
	if me.AuthEnabled != true || me.Authenticated != false {
		t.Fatalf("unauth /api/me = %+v", me)
	}

	// Wrong password rejected.
	if _, code := login(t, hs.URL, "alice", "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("bad login code = %d, want 401", code)
	}

	// Correct login yields a session cookie.
	cookie, code := login(t, hs.URL, "alice", "pw-a")
	if code != http.StatusOK || cookie == "" {
		t.Fatalf("login code = %d cookie=%q", code, cookie)
	}
	if me := getMe(t, hs.URL, cookie); !me.Authenticated || me.User != "alice" {
		t.Fatalf("authed /api/me = %+v", me)
	}

	// WS without a cookie is rejected.
	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws"
	if _, _, err := websocket.Dial(ctx, wsURL, nil); err == nil {
		t.Fatal("WS connected without authentication")
	}

	// WS with alice's cookie sees only alice's network.
	hdr := http.Header{"Cookie": {cookie}}
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("authed dial: %v", err)
	}
	defer ws.CloseNow()
	readFrame(t, ctx, ws) // hello
	env := readFrame(t, ctx, ws)
	var init initState
	if err := json.Unmarshal(env.D, &init); err != nil {
		t.Fatal(err)
	}
	if len(init.Networks) != 1 || init.Networks[0].ID != "alicenet" {
		t.Fatalf("alice saw networks %+v (isolation breach?)", init.Networks)
	}
}

type meResp struct {
	AuthEnabled   bool   `json:"authEnabled"`
	Authenticated bool   `json:"authenticated"`
	User          string `json:"user"`
}

type initState struct {
	Networks []struct {
		ID string `json:"id"`
	} `json:"networks"`
}

func getMe(t *testing.T, base, cookie string) meResp {
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
	var m meResp
	json.NewDecoder(resp.Body).Decode(&m)
	return m
}

// login posts credentials and returns the session cookie header value.
func login(t *testing.T, base, user, pass string) (cookie string, code int) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	resp, err := http.Post(base+"/api/login", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			cookie = c.Name + "=" + c.Value
		}
	}
	return cookie, resp.StatusCode
}
