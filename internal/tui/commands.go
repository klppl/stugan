package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/klippelism/stugan/internal/core"
)

// refreshSnapshot pulls a fresh deep copy of the user's tree. Returned as a
// snapshotMsg so Update can rebuild the sidebar on the model goroutine.
func (m *model) refreshSnapshot() tea.Msg {
	return snapshotMsg{snap: m.eng.Snapshot()}
}

// snapshotMsg carries a fresh engine snapshot.
type snapshotMsg struct{ snap *core.User }

// loadUnread fetches persisted unread/highlight tallies at start.
func (m *model) loadUnread() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	counts, err := m.hist.UnreadCounts(ctx)
	if err != nil {
		return errMsg{err}
	}
	return unreadMsg{counts: counts}
}

// loadBacklog returns a command that pages the newest backlogPage lines for a
// buffer from history.
func (m *model) loadBacklog(b bufRef) tea.Cmd {
	hist := m.hist
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		msgs, more, err := hist.Backlog(ctx, b.net, b.name, 0, backlogPage)
		return backlogMsg{network: b.net, buffer: b.name, msgs: msgs, more: more, err: err}
	}
}

// markRead advances the buffer's read marker to now.
func (m *model) markRead(b bufRef) tea.Cmd {
	if b.zero() {
		return nil
	}
	hist := m.hist
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hist.MarkRead(ctx, b.net, b.name, time.Time{})
		return nil
	}
}

// tick drives the transient status-line expiry and typing-indicator decay.
func tick() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

type tickMsg time.Time
