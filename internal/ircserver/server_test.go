package ircserver

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klippelism/stugan/internal/core"
	"github.com/klippelism/stugan/internal/server"
	"golang.org/x/crypto/bcrypt"
)

type mockHistory struct {
	msgs []core.Message
}

func (m *mockHistory) Backlog(ctx context.Context, network, buffer string, beforeSeq int64, limit int) ([]core.Message, bool, error) {
	var out []core.Message
	for _, msg := range m.msgs {
		if msg.Network == network && core.EqualIRC(msg.Buffer, buffer) {
			out = append(out, msg)
		}
	}
	return out, false, nil
}

func (m *mockHistory) BacklogAround(ctx context.Context, network, buffer, anchor string, around time.Time, limit int) ([]core.Message, bool, bool, error) {
	return nil, false, false, nil
}

func (m *mockHistory) Search(ctx context.Context, query, network, buffer string, limit int) ([]core.Message, error) {
	return nil, nil
}

func (m *mockHistory) MarkRead(ctx context.Context, network, buffer string, ts time.Time) error {
	return nil
}

func (m *mockHistory) ReadMarkers(ctx context.Context) (map[string]int64, error) {
	return nil, nil
}

func (m *mockHistory) UnreadCounts(ctx context.Context) ([]core.UnreadCount, error) {
	return nil, nil
}

func (m *mockHistory) MissedHighlights(ctx context.Context, limit int) ([]core.Message, error) {
	return nil, nil
}

type mockHub struct {
	users   map[string]string // user -> password
	tenants map[string]*server.Tenant
}

func (h *mockHub) AuthEnabled() bool {
	return len(h.users) > 0
}

func (h *mockHub) Login(username, password string) (string, bool) {
	if pw, ok := h.users[username]; ok && pw == password {
		return username, true
	}
	return "", false
}

func (h *mockHub) Tenant(userID string) (*server.Tenant, bool) {
	t, ok := h.tenants[userID]
	return t, ok
}

func (h *mockHub) Users() []string {
	var list []string
	for u := range h.tenants {
		list = append(list, u)
	}
	return list
}

func setupTestEnvironment(t *testing.T) (*Server, *core.Engine, *mockHistory, string, func()) {
	hist := &mockHistory{}
	eng := core.New(core.Options{
		User: &core.User{
			ID:   "alice",
			Name: "alice",
			Networks: []*core.Network{
				{
					ID:    "libera",
					Name:  "libera",
					Nick:  "alice",
					State: core.StateRegistered,
					Params: core.NetworkParams{
						ID:   "libera",
						Name: "libera",
						Addr: "irc.libera.chat:6697",
						Nick: "alice",
						TLS:  true,
					},
					Channels: []*core.Channel{
						{
							Name:  "#stugan",
							Kind:  core.KindChannel,
							Topic: "Welcome to stugan channel",
							Members: map[string]*core.Member{
								"alice": {Nick: "alice", Modes: "@"},
								"bob":   {Nick: "bob", Modes: "+"},
							},
						},
					},
				},
			},
		},
	})

	hub := &mockHub{
		users: map[string]string{
			"alice": "password123",
		},
		tenants: map[string]*server.Tenant{
			"alice": {
				Engine:  eng,
				History: hist,
			},
		},
	}

	srv := New(hub, Options{
		MaxPlayback: 10,
	})
	eng.AddSink(srv.Sink("alice"))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}

	_, cancel := context.WithCancel(context.Background())
	srv.mu.Lock()
	srv.listener = ln
	srv.mu.Unlock()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			sess := newSession(conn, srv, srv.hub)
			go sess.serve()
		}
	}()

	addr := ln.Addr().String()
	cleanup := func() {
		cancel()
		_ = ln.Close()
	}

	return srv, eng, hist, addr, cleanup
}

func TestBouncerAttachAndStateReplay(t *testing.T) {
	srv, _, hist, addr, cleanup := setupTestEnvironment(t)
	defer cleanup()

	hist.msgs = append(hist.msgs, core.Message{
		ID:      "msg-123",
		Network: "libera",
		Buffer:  "#stugan",
		From:    "bob",
		Text:    "hello from history",
		Time:    time.Now().Add(-5 * time.Minute),
		Kind:    core.MsgPrivmsg,
	})

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)

	// Step 1: Send registration lines
	fmt.Fprintf(conn, "CAP LS 302\r\n")
	fmt.Fprintf(conn, "CAP REQ :server-time message-tags echo-message\r\n")
	fmt.Fprintf(conn, "PASS alice/libera:password123\r\n")
	fmt.Fprintf(conn, "NICK alice\r\n")
	fmt.Fprintf(conn, "USER alice 0 * :Alice\r\n")
	fmt.Fprintf(conn, "CAP END\r\n")

	// Read lines until 422 MOTD missing or playback received
	var received []string
	deadline := time.After(3 * time.Second)

	done := false
	for !done {
		select {
		case <-deadline:
			t.Fatalf("Timeout waiting for welcome/state burst. Received so far:\n%s", strings.Join(received, "\n"))
		default:
			_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("Read error: %v (received so far: %s)", err, strings.Join(received, "\n"))
			}
			line = strings.TrimRight(line, "\r\n")
			received = append(received, line)
			if strings.Contains(line, "hello from history") {
				done = true
			}
		}
	}

	fullText := strings.Join(received, "\n")
	if !strings.Contains(fullText, "001 alice :Welcome to the stugan bouncer, alice") {
		t.Errorf("Missing 001 welcome line in:\n%s", fullText)
	}
	if !strings.Contains(fullText, "JOIN #stugan") {
		t.Errorf("Missing JOIN #stugan in:\n%s", fullText)
	}
	if !strings.Contains(fullText, "332 alice #stugan :Welcome to stugan channel") {
		t.Errorf("Missing 332 TOPIC in:\n%s", fullText)
	}
	if !strings.Contains(fullText, "353 alice = #stugan") {
		t.Errorf("Missing 353 NAMES in:\n%s", fullText)
	}
	if !strings.Contains(fullText, "hello from history") {
		t.Errorf("Missing history playback in:\n%s", fullText)
	}

	// Test live Sink Print message
	srv.broadcastPrint("alice", core.Message{
		ID:      "msg-124",
		Network: "libera",
		Buffer:  "#stugan",
		From:    "bob",
		Text:    "live chat message",
		Time:    time.Now(),
		Kind:    core.MsgPrivmsg,
	})

	_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("Failed to read live message: %v", err)
	}
	if !strings.Contains(line, "live chat message") {
		t.Errorf("Expected live chat message, got: %s", line)
	}

	// Test QUIT
	fmt.Fprintf(conn, "QUIT :Detaching\r\n")
}

func TestBouncerSaslPlain(t *testing.T) {
	_, _, _, addr, cleanup := setupTestEnvironment(t)
	defer cleanup()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)

	fmt.Fprintf(conn, "CAP LS 302\r\n")
	fmt.Fprintf(conn, "CAP REQ :sasl\r\n")

	// Read CAP LS and ACK
	for i := 0; i < 2; i++ {
		_, _ = reader.ReadString('\n')
	}

	fmt.Fprintf(conn, "AUTHENTICATE PLAIN\r\n")
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "AUTHENTICATE +") {
		t.Fatalf("Expected 'AUTHENTICATE +', got: %s (err: %v)", line, err)
	}

	// Send base64 payload "\0alice/libera\0password123"
	payload := base64.StdEncoding.EncodeToString([]byte("\x00alice/libera\x00password123"))
	fmt.Fprintf(conn, "AUTHENTICATE %s\r\n", payload)

	// Read 900 and 903
	line900, _ := reader.ReadString('\n')
	if !strings.Contains(line900, "900") {
		t.Errorf("Expected numeric 900, got: %s", line900)
	}
	line903, _ := reader.ReadString('\n')
	if !strings.Contains(line903, "903") {
		t.Errorf("Expected numeric 903, got: %s", line903)
	}

	fmt.Fprintf(conn, "NICK alice\r\n")
	fmt.Fprintf(conn, "USER alice 0 * :Alice\r\n")
	fmt.Fprintf(conn, "CAP END\r\n")

	// Verify welcome 001
	welcomeLine, _ := reader.ReadString('\n')
	if !strings.Contains(welcomeLine, "001 alice :Welcome to the stugan bouncer, alice") {
		t.Errorf("Expected welcome 001, got: %s", welcomeLine)
	}
}

func TestBouncerChatHistoryAndNetworks(t *testing.T) {
	_, _, hist, addr, cleanup := setupTestEnvironment(t)
	defer cleanup()

	hist.msgs = append(hist.msgs, core.Message{
		ID:      "msg-hist-1",
		Network: "libera",
		Buffer:  "#stugan",
		From:    "bob",
		Text:    "scrollback line 1",
		Time:    time.Now().Add(-10 * time.Minute),
		Kind:    core.MsgPrivmsg,
	})

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)

	fmt.Fprintf(conn, "CAP LS 302\r\n")
	fmt.Fprintf(conn, "CAP REQ :batch draft/chathistory soju.im/bouncer-networks\r\n")
	fmt.Fprintf(conn, "PASS alice/libera:password123\r\n")
	fmt.Fprintf(conn, "NICK alice\r\n")
	fmt.Fprintf(conn, "USER alice 0 * :Alice\r\n")
	fmt.Fprintf(conn, "CAP END\r\n")

	// Discard burst
	time.Sleep(50 * time.Millisecond)
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	for {
		_, err := reader.ReadString('\n')
		if err != nil {
			break
		}
	}
	_ = conn.SetReadDeadline(time.Time{})

	// Test BOUNCER LISTNETWORKS
	fmt.Fprintf(conn, "BOUNCER LISTNETWORKS\r\n")
	var bouncerLines []string
	for i := 0; i < 3; i++ {
		l, err := reader.ReadString('\n')
		if err == nil {
			bouncerLines = append(bouncerLines, strings.TrimRight(l, "\r\n"))
		}
	}
	fullBouncer := strings.Join(bouncerLines, "\n")
	if !strings.Contains(fullBouncer, "BOUNCER NETWORK libera") {
		t.Errorf("Expected BOUNCER NETWORK line in:\n%s", fullBouncer)
	}

	// Test CHATHISTORY LATEST
	fmt.Fprintf(conn, "CHATHISTORY LATEST #stugan * 5\r\n")
	var historyLines []string
	for i := 0; i < 3; i++ {
		l, err := reader.ReadString('\n')
		if err == nil {
			historyLines = append(historyLines, strings.TrimRight(l, "\r\n"))
		}
	}
	fullHist := strings.Join(historyLines, "\n")
	if !strings.Contains(fullHist, "scrollback line 1") {
		t.Errorf("Expected chathistory line in:\n%s", fullHist)
	}
}

func TestSetupTLSSelfSigned(t *testing.T) {
	dir := t.TempDir()
	res, err := SetupTLS("", "", dir)
	if err != nil {
		t.Fatalf("SetupTLS failed: %v", err)
	}
	if res.Source != "self-signed" {
		t.Errorf("Expected source self-signed, got %q", res.Source)
	}
	if res.Fingerprint == "" {
		t.Errorf("Expected non-empty fingerprint")
	}
}

func TestBouncerReadMarker(t *testing.T) {
	srv, eng, _, addr, cleanup := setupTestEnvironment(t)
	defer cleanup()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)

	fmt.Fprintf(conn, "CAP LS 302\r\n")
	fmt.Fprintf(conn, "CAP REQ :draft/read-marker\r\n")
	fmt.Fprintf(conn, "PASS alice/libera:password123\r\n")
	fmt.Fprintf(conn, "NICK alice\r\n")
	fmt.Fprintf(conn, "USER alice 0 * :Alice\r\n")
	fmt.Fprintf(conn, "CAP END\r\n")

	// Discard burst
	time.Sleep(50 * time.Millisecond)
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	for {
		_, err := reader.ReadString('\n')
		if err != nil {
			break
		}
	}
	_ = conn.SetReadDeadline(time.Time{})

	// Downstream sends MARKREAD
	fmt.Fprintf(conn, "MARKREAD #stugan timestamp=2026-08-16T20:00:00.000Z\r\n")

	// Verify server sink broadcast
	eng.MarkRead("libera", "#stugan", time.Date(2026, 8, 16, 20, 0, 0, 0, time.UTC))

	_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("Failed to read MARKREAD broadcast: %v", err)
	}
	if !strings.Contains(line, "MARKREAD #stugan") {
		t.Errorf("Expected MARKREAD broadcast line, got: %s", line)
	}
	_ = srv
}

// attachAlice registers a bouncer client bound to libera and drains the
// attach burst (up to the history-free 422/notice tail).
func attachAlice(t *testing.T, addr string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	reader := bufio.NewReader(conn)
	fmt.Fprintf(conn, "PASS alice/libera:password123\r\nNICK alice\r\nUSER alice 0 * :Alice\r\n")
	for {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("reading burst: %v", err)
		}
		if strings.Contains(line, " 366 ") { // end of NAMES: burst done
			return conn, reader
		}
	}
}

// TestBouncerNetworkChangedOnlyOnTransition: NetworkChanged fires on every
// membership change; the client must hear about it only when the connection
// state actually changes.
func TestBouncerNetworkChangedOnlyOnTransition(t *testing.T) {
	srv, eng, _, addr, cleanup := setupTestEnvironment(t)
	defer cleanup()
	conn, reader := attachAlice(t, addr)
	defer conn.Close()

	n := eng.SnapshotNetwork("libera")
	for range 5 { // e.g. five joins: same state, no notice
		srv.broadcastNetworkChanged("alice", n)
	}
	n.State = core.StateDisconnected
	srv.broadcastNetworkChanged("alice", n)

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(line, "is disconnected") {
		t.Fatalf("first line after unchanged updates = %q, want the disconnect notice", line)
	}
}

// TestBouncerCapReqAfterRegistrationRace exercises a post-registration CAP
// REQ concurrently with sink fan-out (which reads the caps). Under -race this
// used to report a concurrent map read/write, a fatal error in production.
func TestBouncerCapReqAfterRegistrationRace(t *testing.T) {
	srv, _, _, addr, cleanup := setupTestEnvironment(t)
	defer cleanup()
	conn, reader := attachAlice(t, addr)
	defer conn.Close()
	go func() { // keep the socket drained
		for {
			if _, err := reader.ReadString('\n'); err != nil {
				return
			}
		}
	}()

	stop := make(chan struct{})
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			srv.broadcastTyping("alice", "libera", "#stugan", "bob", "active")
		}
	}()
	for range 50 {
		fmt.Fprintf(conn, "CAP REQ :message-tags\r\nCAP REQ :-message-tags\r\n")
	}
	time.Sleep(100 * time.Millisecond)
	close(stop)
}

type mapPrefs struct {
	mu sync.Mutex
	m  map[string]string
}

func (p *mapPrefs) Pref(key string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.m[key], nil
}

func (p *mapPrefs) SetPref(key, value string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[key] = value
	return nil
}

// TestBouncerAccessToggle: an account that turned bouncer access off in
// Settings is refused at registration, and DisconnectUser drops clients that
// are already attached.
func TestBouncerAccessToggle(t *testing.T) {
	srv, _, _, addr, cleanup := setupTestEnvironment(t)
	defer cleanup()
	prefs := &mapPrefs{m: map[string]string{"bouncer": "off"}}
	srv.hub.(*mockHub).tenants["alice"].Prefs = prefs

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	reader := bufio.NewReader(conn)
	fmt.Fprintf(conn, "PASS alice/libera:password123\r\nNICK alice\r\nUSER alice 0 * :Alice\r\n")
	refused := false
	for !refused {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("expected 464 refusal, got read error: %v", err)
		}
		if strings.Contains(line, " 001 ") {
			t.Fatalf("registered despite bouncer access being off: %q", line)
		}
		refused = strings.Contains(line, " 464 ") && strings.Contains(line, "disabled")
	}
	_ = conn.Close()

	_ = prefs.SetPref("bouncer", "on")
	conn, reader = attachAlice(t, addr)
	defer conn.Close()

	srv.DisconnectUser("alice", "IRC client access disabled in Settings")
	for {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := reader.ReadString('\n')
		if err != nil {
			return // socket closed: client was dropped
		}
		if strings.HasPrefix(line, "ERROR ") {
			return
		}
	}
}

func TestListenerInfo(t *testing.T) {
	cases := []struct {
		addr string
		host string
		port int
	}{
		{"0.0.0.0:6697", "", 6697},
		{":6667", "", 6667},
		{"[::]:6697", "", 6697},
		{"irc.example.com:7000", "irc.example.com", 7000},
		{"192.168.1.5:6697", "192.168.1.5", 6697},
	}
	for _, c := range cases {
		s := New(&mockHub{}, Options{Addr: c.addr, TLS: true})
		host, port, tlsOn, fp := s.Listener()
		if host != c.host || port != c.port || !tlsOn || fp != "" {
			t.Errorf("%s: got (%q, %d, %v, %q), want (%q, %d, true, \"\")", c.addr, host, port, tlsOn, fp, c.host, c.port)
		}
	}
}

// registerResult runs a raw registration and reports the first decisive
// numeric: "001" (attached), "464" (refused) or the SASL outcome "903"/"904".
func registerResult(t *testing.T, addr, script string) string {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	fmt.Fprint(conn, script)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("no decisive reply for %q: %v", script, err)
		}
		for _, code := range []string{" 001 ", " 464 ", " 903 ", " 904 "} {
			if strings.Contains(line, code) {
				return strings.TrimSpace(code)
			}
		}
	}
}

// TestBouncerSingleUserPassword: in single-user mode the web password guards
// the bouncer; without it, nobody can attach.
func TestBouncerSingleUserPassword(t *testing.T) {
	defer func(d time.Duration) { authFailDelay = d }(authFailDelay)
	authFailDelay = 0

	srv, _, _, addr, cleanup := setupTestEnvironment(t)
	defer cleanup()
	srv.hub.(*mockHub).users = nil // single-user mode
	hash, err := bcrypt.GenerateFromPassword([]byte("webpw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	srv.opts.PasswordHash = string(hash)

	sasl := func(pass string) string {
		payload := base64.StdEncoding.EncodeToString([]byte("\x00x/libera\x00" + pass))
		return "CAP LS 302\r\nCAP REQ :sasl\r\nAUTHENTICATE PLAIN\r\nAUTHENTICATE " + payload + "\r\n"
	}
	cases := []struct {
		name, script, want string
	}{
		{"no password", "NICK a\r\nUSER alice/libera 0 * :a\r\n", "464"},
		{"wrong password", "PASS nope\r\nNICK a\r\nUSER alice/libera 0 * :a\r\n", "464"},
		{"network as password (old docs)", "PASS libera\r\nNICK a\r\nUSER a 0 * :a\r\n", "464"},
		{"password + network in username", "PASS webpw\r\nNICK a\r\nUSER alice/libera 0 * :a\r\n", "001"},
		{"user/network:password", "PASS x/libera:webpw\r\nNICK a\r\nUSER a 0 * :a\r\n", "001"},
		{"sasl wrong", sasl("nope"), "904"},
		{"sasl right", sasl("webpw"), "903"},
	}
	for _, c := range cases {
		if got := registerResult(t, addr, c.script); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}

	// No web password at all: only loopback clients (this test dials
	// 127.0.0.1) may attach, with no credentials.
	srv.opts.PasswordHash = ""
	if got := registerResult(t, addr, "NICK a\r\nUSER alice/libera 0 * :a\r\n"); got != "001" {
		t.Errorf("loopback without password: got %s, want 001", got)
	}
}

func TestIsLoopback(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:5000":   true,
		"[::1]:5000":       true,
		"192.168.1.9:5000": false,
		"203.0.113.7:6697": false,
		"[2001:db8::1]:1":  false,
		"garbage":          false,
	} {
		if got := isLoopback(addr); got != want {
			t.Errorf("isLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}
