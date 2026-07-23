package main

import (
	"log/slog"

	"github.com/charmbracelet/ssh"
	xssh "golang.org/x/crypto/ssh"

	"github.com/klippelism/stugan/internal/config"
	"github.com/klippelism/stugan/internal/tui"
)

// sshResolver implements tui.Resolver: it maps an offered public key to a
// stugan user via each user's configured authorized_keys, and hands the TUI
// server that user's engine + history. It is the SSH counterpart of the web
// Hub.
type sshResolver struct {
	hub  *hub
	keys map[string][]ssh.PublicKey // userID → authorized public keys
	log  *slog.Logger
}

// newSSHResolver parses every user's authorized_keys once at startup. A key
// that fails to parse is logged and skipped rather than aborting boot.
func newSSHResolver(cfg *config.Config, h *hub, log *slog.Logger) *sshResolver {
	keys := map[string][]ssh.PublicKey{}
	for _, u := range cfg.EffectiveUsers() {
		for _, line := range u.AuthorizedKeys {
			pk, _, _, _, err := xssh.ParseAuthorizedKey([]byte(line))
			if err != nil {
				log.Warn("ssh: bad authorized_key", "user", u.Name, "err", err)
				continue
			}
			keys[u.Name] = append(keys[u.Name], pk)
		}
	}
	return &sshResolver{hub: h, keys: keys, log: log}
}

// hasKeys reports whether any user registered an SSH key — if none did, the
// SSH server would accept no logins and is not worth starting.
func (r *sshResolver) hasKeys() bool {
	for _, ks := range r.keys {
		if len(ks) > 0 {
			return true
		}
	}
	return false
}

// Authorize accepts the key when it is listed for the requested SSH username.
// The SSH username must name a real stugan user; there is no cross-user
// fallback, so one user's key can never open another's session.
func (r *sshResolver) Authorize(sshUser string, key ssh.PublicKey) (string, bool) {
	for _, allowed := range r.keys[sshUser] {
		if ssh.KeysEqual(key, allowed) {
			return sshUser, true
		}
	}
	return "", false
}

// Tenant adapts the web server's Tenant to the TUI's, sharing the same engine
// and store.
func (r *sshResolver) Tenant(userID string) (*tui.Tenant, bool) {
	t, ok := r.hub.Tenant(userID)
	if !ok {
		return nil, false
	}
	return &tui.Tenant{UserID: userID, Engine: t.Engine, History: t.History}, true
}
