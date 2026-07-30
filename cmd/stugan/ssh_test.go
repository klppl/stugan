package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"log/slog"
	"strings"
	"testing"

	"github.com/charmbracelet/ssh"
	xssh "golang.org/x/crypto/ssh"

	"github.com/klippelism/stugan/internal/config"
)

// newEd25519 generates a fresh ed25519 key and returns its ssh.PublicKey plus
// the OpenSSH authorized_keys line for it.
func newEd25519(t *testing.T) (ssh.PublicKey, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := xssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(xssh.MarshalAuthorizedKey(sshPub)))
	return sshPub, line
}

func TestSSHResolverAuthorize(t *testing.T) {
	alicePub, aliceLine := newEd25519(t)
	bobPub, bobLine := newEd25519(t)
	strangerPub, _ := newEd25519(t)

	cfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "alice", AuthorizedKeys: []string{aliceLine}},
			{Name: "bob", AuthorizedKeys: []string{bobLine}},
		},
	}
	r := newSSHResolver(cfg, &hub{}, slog.Default())

	tests := []struct {
		name    string
		sshUser string
		key     ssh.PublicKey
		wantID  string
		wantOK  bool
	}{
		{"alice's key as alice", "alice", alicePub, "alice", true},
		{"bob's key as bob", "bob", bobPub, "bob", true},
		{"alice's key as bob is rejected", "bob", alicePub, "", false},
		{"bob's key as alice is rejected", "alice", bobPub, "", false},
		{"unknown key rejected", "alice", strangerPub, "", false},
		{"unknown user rejected", "carol", alicePub, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, ok := r.Authorize(tt.sshUser, tt.key)
			if ok != tt.wantOK || id != tt.wantID {
				t.Fatalf("Authorize(%q) = (%q, %v), want (%q, %v)",
					tt.sshUser, id, ok, tt.wantID, tt.wantOK)
			}
		})
	}
}

// TestSSHResolverBadKeySkipped checks that a malformed authorized_keys entry
// is skipped without aborting, and a valid sibling still authenticates.
func TestSSHResolverBadKeySkipped(t *testing.T) {
	pub, line := newEd25519(t)
	cfg := &config.Config{
		Users: []config.UserConfig{
			{Name: "alice", AuthorizedKeys: []string{"not-a-key", line}},
		},
	}
	r := newSSHResolver(cfg, &hub{}, slog.Default())
	if _, ok := r.Authorize("alice", pub); !ok {
		t.Fatal("valid key alongside a malformed one should still authorize")
	}
	if !r.hasKeys() {
		t.Fatal("hasKeys should be true")
	}
}

// TestSSHResolverSingleUserKeys checks single-user mode reads keys from the
// [ssh] block for the implicit default user.
func TestSSHResolverSingleUserKeys(t *testing.T) {
	pub, line := newEd25519(t)
	cfg := &config.Config{SSH: config.SSHConfig{AuthorizedKeys: []string{line}}}
	r := newSSHResolver(cfg, &hub{}, slog.Default())
	if id, ok := r.Authorize("default", pub); !ok || id != "default" {
		t.Fatalf("default user key should authorize, got (%q, %v)", id, ok)
	}
}
