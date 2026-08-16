package ircserver

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/klippelism/stugan/internal/core"
	"github.com/klippelism/stugan/internal/server"
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
