package plugin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klippelism/stugan/internal/core"
)

func loadQauth(t *testing.T) (*Host, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{nickVal: "stugan_user"}
	content, err := os.ReadFile(filepath.Join("..", "..", "plugins", "qauth.lua"))
	if err != nil {
		t.Fatalf("read plugins/qauth.lua: %v", err)
	}
	h := newHost(t, api, map[string]string{
		"qauth.lua": string(content),
	}, nil)
	return h, api
}

func TestQauthFlow(t *testing.T) {
	h, api := loadQauth(t)

	// Configure credentials.
	h.Dispatch(context.Background(), core.Event{
		Type: core.EvCommand, Network: "quakenet", Buffer: "status",
		Command: "qauth", Args: []string{"set", "testuser", "testpass12345"},
	})

	// Connect signal triggers authentication and holds joins.
	h.Dispatch(context.Background(), core.Event{Type: core.EvConnect, Network: "quakenet"})

	api.mu.Lock()
	holds := append([]string(nil), api.holds...)
	sends := append([][2]string(nil), api.sends...)
	api.mu.Unlock()

	if len(holds) != 1 || holds[0] != "quakenet" {
		t.Fatalf("holds = %v, want [quakenet]", holds)
	}
	if len(sends) != 1 || sends[0][1] != "PRIVMSG Q@CServe.quakenet.org :CHALLENGE" {
		t.Fatalf("sends = %v, want CHALLENGE request", sends)
	}

	// Q sends CHALLENGE.
	h.Dispatch(context.Background(), core.Event{
		Type:    core.EvMessageIn,
		Network: "quakenet",
		Message: &core.Message{
			Network: "quakenet",
			Buffer:  "status",
			From:    "Q",
			Kind:    "notice",
			Text:    "CHALLENGE 12345678 HMAC-SHA-256",
		},
	})

	api.mu.Lock()
	sends = append([][2]string(nil), api.sends...)
	api.mu.Unlock()

	if len(sends) < 2 || sends[1][0] != "quakenet" {
		t.Fatalf("expected CHALLENGEAUTH sent, got %v", sends)
	}

	// Q confirms login: "You are now logged in as testuser."
	h.Dispatch(context.Background(), core.Event{
		Type:    core.EvMessageIn,
		Network: "quakenet",
		Message: &core.Message{
			Network: "quakenet",
			Buffer:  "status",
			From:    "Q",
			Kind:    "notice",
			Text:    "You are now logged in as testuser.",
		},
	})

	api.mu.Lock()
	sends = append([][2]string(nil), api.sends...)
	releases := append([]string(nil), api.releases...)
	api.mu.Unlock()

	// MODE +x should be sent immediately.
	if len(sends) < 3 || sends[2][1] != "MODE stugan_user +x" {
		t.Fatalf("expected MODE stugan_user +x, got %v", sends)
	}

	// release_joins should not have fired yet immediately because of the 1s delay.
	if len(releases) != 0 {
		t.Fatalf("releases should be delayed, got %v", releases)
	}

	// Wait for the timer to release joins.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		api.mu.Lock()
		releases = append([]string(nil), api.releases...)
		api.mu.Unlock()
		if len(releases) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if len(releases) != 1 || releases[0] != "quakenet" {
		t.Fatalf("releases = %v, want [quakenet]", releases)
	}
}
