package tui

import (
	"context"
	"time"

	"github.com/charmbracelet/ssh"
	"github.com/klippelism/stugan/internal/core"
)

// History is the slice of the store the TUI reads: backlog paging, unread
// tallies, and advancing the read marker. Mirrors the web server's History
// interface so the same *store.Store satisfies both.
type History interface {
	Backlog(ctx context.Context, network, buffer string, beforeSeq int64, limit int) ([]core.Message, bool, error)
	UnreadCounts(ctx context.Context) ([]core.UnreadCount, error)
	MarkRead(ctx context.Context, network, buffer string, ts time.Time) error
}

// Tenant is one user's engine and history — the same shape the web server's
// Tenant carries, minus the browser-only preferences.
type Tenant struct {
	UserID  string
	Engine  *core.Engine
	History History
}

// Resolver decides which user a public key may authenticate as and hands the
// SSH server that user's tenant. The composition root (cmd/stugan) implements
// it against config and the per-user engines.
type Resolver interface {
	// Authorize reports the user id an offered public key may log in as for
	// the requested SSH username, or ok=false to reject. Called from the SSH
	// auth handler.
	Authorize(sshUser string, key ssh.PublicKey) (userID string, ok bool)
	// Tenant returns a user's engine/history, or ok=false if unknown.
	Tenant(userID string) (*Tenant, bool)
}
