package tui

import (
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/klippelism/stugan/internal/core"
)

// session is one live SSH TUI connection: a handle on its Bubble Tea program
// so the sink can push messages into it. prog.Send is goroutine-safe and a
// no-op once the program has stopped, so a send racing a disconnect is
// harmless.
type session struct {
	user string
	prog *tea.Program
}

// registry tracks the active sessions per user. Sessions attach and detach
// from SSH handler goroutines; the sink fans out from the engine loop
// goroutine — so every access is guarded by mu.
type registry struct {
	mu       sync.RWMutex
	sessions map[string]map[*session]struct{} // userID → sessions
}

func newRegistry() *registry {
	return &registry{sessions: map[string]map[*session]struct{}{}}
}

func (r *registry) add(s *session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.sessions[s.user]
	if m == nil {
		m = map[*session]struct{}{}
		r.sessions[s.user] = m
	}
	m[s] = struct{}{}
}

func (r *registry) remove(s *session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m := r.sessions[s.user]; m != nil {
		delete(m, s)
		if len(m) == 0 {
			delete(r.sessions, s.user)
		}
	}
}

// broadcast delivers msg to every live session of user. Called from the
// engine loop goroutine via the sink.
func (r *registry) broadcast(user string, msg tea.Msg) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for s := range r.sessions[user] {
		s.prog.Send(msg)
	}
}

// sink is one user's core.Sink: it translates committed engine events into
// Bubble Tea messages and fans them out to that user's sessions. Registered
// on the engine at startup and never removed, so the engine's sink slice is
// stable while sessions churn underneath.
type sink struct {
	reg  *registry
	user string
}

var _ core.Sink = (*sink)(nil)

func (s *sink) Print(m core.Message) { s.reg.broadcast(s.user, printMsg{m}) }

func (s *sink) NetworkChanged(n *core.Network) {
	// Hand the session a fresh clone (Snapshot already deep-copies) rather
	// than a pointer into engine state; the id lets a session that doesn't
	// care skip work.
	s.reg.broadcast(s.user, netChangedMsg{id: n.ID, net: n})
}

func (s *sink) NetworkRemoved(id string) { s.reg.broadcast(s.user, netRemovedMsg{id}) }

func (s *sink) NetworksReordered(ids []string) { s.reg.broadcast(s.user, netReorderMsg{ids}) }

func (s *sink) ChannelList(network string, items []core.ChannelListItem) {
	s.reg.broadcast(s.user, channelListMsg{network: network, items: items})
}

func (s *sink) Typing(network, buffer, nick, state string) {
	s.reg.broadcast(s.user, typingMsg{network: network, buffer: buffer, nick: nick, state: state})
}

func (s *sink) React(network, buffer, target, nick, reaction string) {
	s.reg.broadcast(s.user, reactMsg{
		network: network, buffer: buffer, target: target, nick: nick, reaction: reaction,
	})
}

func (s *sink) Redact(network, buffer, target, nick, reason string) {
	s.reg.broadcast(s.user, redactMsg{
		network: network, buffer: buffer, target: target, nick: nick, reason: reason,
	})
}
