package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/klippelism/stugan/internal/core"
)

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(msg.Width, minWidth)
		m.height = max(msg.Height, minHeight)
		m.layout()
		m.renderMessages()
		return m, nil

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)

	// -- sink / async messages ------------------------------------------------
	case snapshotMsg:
		m.snap = msg.snap
		first := !m.ready
		m.ready = true
		m.rebuildOrder()
		if first {
			m.layout()
			if cmd := m.selectIndex(m.sel); cmd != nil {
				return m, tea.Batch(cmd, tick())
			}
			return m, tick()
		}
		m.renderMessages()
		return m, nil

	case netChangedMsg, netRemovedMsg, netReorderMsg:
		// Any structural change: re-snapshot and rebuild. Cheap (deep copy of
		// one user's tree) and keeps the sidebar authoritative.
		return m, m.refreshSnapshot

	case printMsg:
		return m, m.appendMessage(msg.m)

	case backlogMsg:
		b := bufRef{net: msg.network, name: msg.buffer}
		bb := m.bufs[b.key()]
		if bb == nil {
			bb = &buf{}
			m.bufs[b.key()] = bb
		}
		bb.loading = false
		if msg.err != nil {
			m.setStatus("history: " + msg.err.Error())
			return m, nil
		}
		oldLines, oldOffset := m.vp.TotalLineCount(), m.vp.YOffset
		// A live line can be persisted while the async backlog query is in
		// flight. Merge by stable identity so that overlap is shown only once.
		bb.msgs = mergeBacklog(msg.msgs, bb.msgs)
		bb.loaded = true
		bb.more = msg.more
		if b.eq(m.active) {
			m.renderMessages()
			if msg.beforeSeq == 0 {
				m.vp.GotoBottom()
			} else {
				// Keep the previously visible line stationary when older content is
				// inserted above it.
				m.vp.SetYOffset(oldOffset + m.vp.TotalLineCount() - oldLines)
			}
		}
		return m, nil

	case unreadMsg:
		for _, c := range msg.counts {
			b := bufRef{net: c.Network, name: c.Buffer}
			if b.eq(m.active) {
				continue
			}
			m.unread[b.key()] = c.Unread
			if c.Highlight > 0 {
				m.highlite[b.key()] = c.Highlight
			}
		}
		return m, nil

	case typingMsg:
		bk := bufRef{net: msg.network, name: msg.buffer}.key()
		if m.typing[bk] == nil {
			m.typing[bk] = map[string]time.Time{}
		}
		if msg.state == "done" {
			delete(m.typing[bk], msg.nick)
		} else {
			m.typing[bk][msg.nick] = time.Now()
		}
		return m, nil

	case reactMsg:
		m.setStatus(msg.nick + " reacted " + msg.reaction)
		return m, nil

	case redactMsg:
		return m, m.refreshSnapshot

	case channelListMsg:
		if b, ok := m.ov.(*browserOverlay); ok {
			b.setResults(msg.items)
		}
		return m, nil

	case statusMsg:
		m.setStatus(msg.text)
		return m, nil

	case errMsg:
		if msg.err != nil {
			m.setStatus(msg.err.Error())
		}
		return m, nil

	case tickMsg:
		// Expire the status line and stale typing entries.
		if !m.statusUntil.IsZero() && time.Now().After(m.statusUntil) {
			m.status = ""
			m.statusUntil = time.Time{}
		}
		for bk, nicks := range m.typing {
			for n, t := range nicks {
				if time.Since(t) > 6*time.Second {
					delete(nicks, n)
				}
			}
			if len(nicks) == 0 {
				delete(m.typing, bk)
			}
		}
		return m, tick()
	}

	return m, nil
}

// handleKey routes key input: overlays first (modal captures everything),
// then global bindings, then the text input.
func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, m.keys.Quit) {
		return m, tea.Quit
	}
	if m.ov != nil {
		ov, cmd := m.ov.Update(m, msg)
		m.ov = ov
		return m, cmd
	}

	switch {
	case key.Matches(msg, m.keys.NextBuffer):
		return m, m.selectIndex(m.sel + 1)
	case key.Matches(msg, m.keys.PrevBuffer):
		return m, m.selectIndex(m.sel - 1)
	case key.Matches(msg, m.keys.NextNetwork):
		return m, m.selectIndex(m.jumpNetwork(1))
	case key.Matches(msg, m.keys.PrevNetwork):
		return m, m.selectIndex(m.jumpNetwork(-1))
	case key.Matches(msg, m.keys.Members):
		m.showMem = !m.showMem
		m.layout()
		m.renderMessages()
		return m, nil
	case key.Matches(msg, m.keys.Palette):
		m.ov = newPaletteOverlay(m)
		return m, nil
	case key.Matches(msg, m.keys.Networks):
		m.ov = newNetListOverlay(m)
		return m, nil
	case key.Matches(msg, m.keys.Browser):
		m.ov = newBrowserOverlay(m)
		return m, m.ov.(*browserOverlay).request(m)
	case key.Matches(msg, m.keys.Plugins):
		m.ov = newPluginsOverlay(m)
		return m, nil
	case key.Matches(msg, m.keys.Help):
		m.ov = newHelpOverlay()
		return m, nil
	case key.Matches(msg, m.keys.CloseBuffer):
		return m, m.closeActive()
	case key.Matches(msg, m.keys.PageUp):
		m.atBottom = false
		m.vp.HalfViewUp()
		if m.vp.AtTop() {
			return m, m.loadOlder()
		}
		return m, nil
	case key.Matches(msg, m.keys.PageDown):
		m.vp.HalfViewDown()
		m.atBottom = m.vp.AtBottom()
		return m, nil
	case key.Matches(msg, m.keys.Send):
		return m, m.submit()
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// submit sends the input line through the engine and clears it.
func (m *model) submit() tea.Cmd {
	text := strings.TrimRight(m.input.Value(), " ")
	if text == "" || m.active.zero() {
		return nil
	}
	if err := m.eng.SendInput(m.active.net, m.active.name, text); err != nil {
		m.setStatus(err.Error())
		return nil
	}
	m.input.SetValue("")
	m.atBottom = true
	return nil
}

// loadOlder requests the next history page for the active buffer. The oldest
// persisted rowid is the exact keyset cursor; live-only rows have Seq zero and
// are skipped when choosing it.
func (m *model) loadOlder() tea.Cmd {
	bb := m.bufs[m.active.key()]
	if bb == nil || !bb.loaded || !bb.more || bb.loading {
		return nil
	}
	var beforeSeq int64
	for _, msg := range bb.msgs {
		if msg.Seq > 0 {
			beforeSeq = msg.Seq
			break
		}
	}
	if beforeSeq == 0 {
		return nil
	}
	bb.loading = true
	return m.loadBacklog(m.active, beforeSeq)
}

// mergeBacklog prepends a history page while removing any line already
// present in the live tail. IDs are stable across the sink/store paths; Seq is
// a fallback for persisted rows without a message id.
func mergeBacklog(history, existing []core.Message) []core.Message {
	seen := make(map[string]struct{}, len(history)+len(existing))
	key := func(msg core.Message) string {
		if msg.ID != "" {
			return "id\x00" + msg.ID
		}
		if msg.Seq > 0 {
			return "seq\x00" + strconv.FormatInt(msg.Seq, 10)
		}
		return fmt.Sprintf("line\x00%d\x00%s\x00%s\x00%s\x00%t", msg.Time.UnixNano(), msg.From, msg.Kind, msg.Text, msg.Self)
	}
	out := make([]core.Message, 0, len(history)+len(existing))
	for _, msgs := range [][]core.Message{history, existing} {
		for _, msg := range msgs {
			k := key(msg)
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			out = append(out, msg)
		}
	}
	return out
}

// closeActive closes the active buffer (parting a channel, closing a query).
func (m *model) closeActive() tea.Cmd {
	if m.active.zero() {
		return nil
	}
	b := m.active
	if err := m.eng.CloseBuffer(b.net, b.name); err != nil {
		m.setStatus("close: " + err.Error())
		return nil
	}
	delete(m.bufs, b.key())
	return m.refreshSnapshot
}

// jumpNetwork returns the order index of the first buffer of the network
// dir steps away from the active one (wrapping).
func (m *model) jumpNetwork(dir int) int {
	if len(m.order) == 0 {
		return 0
	}
	nets := m.snap.Networks
	cur := -1
	for i, n := range nets {
		if n.ID == m.active.net {
			cur = i
			break
		}
	}
	if cur < 0 {
		return m.sel
	}
	next := ((cur+dir)%len(nets) + len(nets)) % len(nets)
	target := nets[next].ID
	for i, b := range m.order {
		if b.net == target {
			return i
		}
	}
	return m.sel
}

func (m *model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.atBottom = false
		m.vp.LineUp(3)
		if m.vp.AtTop() {
			return m, m.loadOlder()
		}
		return m, nil
	case tea.MouseButtonWheelDown:
		m.vp.LineDown(3)
		m.atBottom = m.vp.AtBottom()
		return m, nil
	case tea.MouseButtonLeft:
		if (msg.Action == tea.MouseActionRelease || msg.Action == tea.MouseActionPress) && msg.X < sidebarWidth {
			row := 0
			for _, n := range m.snap.Networks {
				row++ // network header row
				for _, ch := range n.Channels {
					if row == msg.Y {
						ref := bufRef{net: n.ID, name: ch.Name}
						for idx, b := range m.order {
							if b.eq(ref) {
								return m, m.selectIndex(idx)
							}
						}
					}
					row++
				}
			}
		}
	}
	return m, nil
}
