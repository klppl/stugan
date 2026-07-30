package tui

import (
	"log/slog"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/klippelism/stugan/internal/core"
)

// bufRef identifies a buffer across networks.
type bufRef struct{ net, name string }

func (b bufRef) key() string      { return b.net + "\x00" + b.name }
func (b bufRef) zero() bool       { return b.net == "" && b.name == "" }
func (b bufRef) eq(o bufRef) bool { return b.net == o.net && b.name == o.name }

// buf is one buffer's loaded state: its messages and whether older history
// remains to page in.
type buf struct {
	msgs   []core.Message
	loaded bool
	more   bool
}

// overlay is a modal layered over the main view (network manager, channel
// browser, quick switcher, …). Update returns the next overlay (nil closes
// it) plus a command; View renders it centered over the base.
type overlay interface {
	Update(m *model, msg tea.Msg) (overlay, tea.Cmd)
	View(m *model) string
}

// model is one SSH session's Bubble Tea model.
type model struct {
	eng  *core.Engine
	hist History
	user string
	log  *slog.Logger

	st   *styles
	keys keymap

	width, height int
	ready         bool

	snap   *core.User // latest full snapshot of this user's tree
	order  []bufRef   // selectable buffers, sidebar order
	sel    int        // index into order
	active bufRef

	bufs     map[string]*buf // keyed by bufRef.key()
	unread   map[string]int
	highlite map[string]int
	typing   map[string]map[string]time.Time // bufkey → nick → last seen

	vp       viewport.Model
	input    textinput.Model
	atBottom bool
	showMem  bool

	ov          overlay
	status      string
	statusUntil time.Time
}

func newModel(t *Tenant, r *lipgloss.Renderer, log *slog.Logger, w, h int) *model {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = "message, or /command"
	in.CharLimit = 0
	in.Focus()

	m := &model{
		eng:      t.Engine,
		hist:     t.History,
		user:     t.UserID,
		log:      log,
		st:       newStyles(r),
		keys:     defaultKeymap(),
		width:    max(w, minWidth),
		height:   max(h, minHeight),
		bufs:     map[string]*buf{},
		unread:   map[string]int{},
		highlite: map[string]int{},
		typing:   map[string]map[string]time.Time{},
		atBottom: true,
		showMem:  true,
		input:    in,
	}
	return m
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.refreshSnapshot, m.loadUnread, textinput.Blink)
}

// -- snapshot / sidebar ------------------------------------------------------

// rebuildOrder flattens the snapshot into the selectable buffer list, keeping
// the current active buffer selected when it still exists.
func (m *model) rebuildOrder() {
	prev := m.active
	m.order = m.order[:0]
	if m.snap == nil {
		return
	}
	for _, n := range m.snap.Networks {
		for _, ch := range n.Channels {
			m.order = append(m.order, bufRef{net: n.ID, name: ch.Name})
		}
	}
	// Re-point selection at the previously active buffer, else clamp.
	m.sel = 0
	for i, b := range m.order {
		if b.eq(prev) {
			m.sel = i
		}
	}
	if m.sel >= len(m.order) {
		m.sel = max(0, len(m.order)-1)
	}
	if len(m.order) > 0 {
		m.active = m.order[m.sel]
	} else {
		m.active = bufRef{}
	}
}

// networkOf returns the snapshot network for an id.
func (m *model) networkOf(id string) *core.Network {
	if m.snap == nil {
		return nil
	}
	return m.snap.Network(id)
}

// channelOf returns the snapshot channel for a buffer ref.
func (m *model) channelOf(b bufRef) *core.Channel {
	n := m.networkOf(b.net)
	if n == nil {
		return nil
	}
	for _, ch := range n.Channels {
		if strings.EqualFold(ch.Name, b.name) {
			return ch
		}
	}
	return nil
}

// selectIndex switches the active buffer to order[i], loading backlog on
// first visit and marking it read.
func (m *model) selectIndex(i int) tea.Cmd {
	if len(m.order) == 0 {
		return nil
	}
	i = clamp(i, 0, len(m.order)-1)
	m.sel = i
	m.active = m.order[i]
	m.atBottom = true
	m.clearUnread(m.active)
	m.renderMessages()
	m.vp.GotoBottom()

	var cmds []tea.Cmd
	if b := m.bufs[m.active.key()]; b == nil || !b.loaded {
		cmds = append(cmds, m.loadBacklog(m.active))
	}
	cmds = append(cmds, m.markRead(m.active))
	return tea.Batch(cmds...)
}

func (m *model) clearUnread(b bufRef) {
	delete(m.unread, b.key())
	delete(m.highlite, b.key())
}

// -- messages ----------------------------------------------------------------

// appendMessage records a live line; bumps unread when it isn't the active
// buffer, and rerenders/scrolls when it is.
func (m *model) appendMessage(msg core.Message) tea.Cmd {
	b := bufRef{net: msg.Network, name: msg.Buffer}
	bb := m.bufs[b.key()]
	if bb == nil {
		bb = &buf{loaded: true}
		m.bufs[b.key()] = bb
	}
	bb.msgs = append(bb.msgs, msg)

	if b.eq(m.active) {
		m.renderMessages()
		if m.atBottom {
			m.vp.GotoBottom()
		}
		return m.markRead(b)
	}
	if !msg.Self {
		m.unread[b.key()]++
		if msg.Highlight {
			m.highlite[b.key()]++
		}
	}
	return nil
}

func (m *model) setStatus(text string) {
	m.status = text
	m.statusUntil = time.Now().Add(6 * time.Second)
}

// clamp / max are tiny helpers (max is builtin in 1.21+ for ints via the
// generic; keep a local for readability with mixed call sites).
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
