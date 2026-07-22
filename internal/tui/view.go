package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/klippelism/stugan/internal/core"
)

// layout recomputes the message viewport size from the current terminal size
// and side-panel visibility. Called on resize and when panels toggle.
func (m *model) layout() {
	memW := 0
	if m.showMem && m.channelOf(m.active) != nil {
		memW = membersWidth
	}
	// Rows: topic (1) + messages + input (1) + status (1).
	msgW := m.width - sidebarWidth - memW
	if msgW < minWidth-sidebarWidth {
		msgW = m.width - sidebarWidth // drop members on very narrow terminals
	}
	msgH := m.height - 3
	if msgH < 1 {
		msgH = 1
	}
	if !m.ready {
		m.vp = viewport.New(msgW, msgH)
		return
	}
	m.vp.Width = msgW
	m.vp.Height = msgH
	m.input.Width = msgW - 2
}

func (m *model) View() string {
	if !m.ready {
		return m.st.help.Render("connecting to stugan…")
	}
	base := m.baseView()
	if m.ov != nil {
		return m.overlayOn(base, m.ov.View(m))
	}
	return base
}

// baseView renders the three-column chat layout.
func (m *model) baseView() string {
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		m.renderSidebar(),
		m.renderMain(),
	)
	if m.showMem && m.channelOf(m.active) != nil {
		body = lipgloss.JoinHorizontal(lipgloss.Top, body, m.renderMembers())
	}
	return body
}

// renderMain is the topic bar + messages + input + status column.
func (m *model) renderMain() string {
	w := m.vp.Width
	topic := m.renderTopic(w)
	status := m.renderStatus(w)
	input := m.st.inputPrompt.Render("> ") + m.input.View()
	input = m.st.input.Width(w).Render(truncate(input, w))
	return lipgloss.JoinVertical(lipgloss.Left, topic, m.vp.View(), input, status)
}

func (m *model) renderTopic(w int) string {
	ch := m.channelOf(m.active)
	title := m.active.name
	if m.active.zero() {
		title = "stugan"
	}
	sub := ""
	if ch != nil {
		if ch.Topic != "" {
			sub = " — " + ch.Topic
		}
		if n := len(ch.Members); n > 0 {
			title = fmt.Sprintf("%s (%d)", title, n)
		}
	}
	return m.st.topic.Width(w).Render(truncate(" "+title+sub, w))
}

func (m *model) renderStatus(w int) string {
	left := m.status
	if left == "" {
		n := m.networkOf(m.active.net)
		nick, state := "", ""
		if n != nil {
			nick, state = n.Nick, string(n.State)
		}
		if t := m.typingLine(); t != "" {
			left = t
		} else {
			left = fmt.Sprintf("%s@%s [%s]", nick, m.active.net, state)
		}
	}
	right := "f1 help · ^k switch · ^o nets · ^l list · ^w members"
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return m.st.statusBar.Width(w).Render(truncate(" "+left, w))
	}
	return m.st.statusBar.Width(w).Render(" " + left + strings.Repeat(" ", gap-1) + right)
}

func (m *model) typingLine() string {
	nicks := m.typing[m.active.key()]
	if len(nicks) == 0 {
		return ""
	}
	names := make([]string, 0, len(nicks))
	for n := range nicks {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 1 {
		return names[0] + " is typing…"
	}
	return strings.Join(names, ", ") + " are typing…"
}

// renderSidebar draws the network→buffer tree with unread badges.
func (m *model) renderSidebar() string {
	var b strings.Builder
	rows := 0
	maxRows := m.height
	for _, n := range m.snap.Networks {
		head := n.Name
		if n.State != core.StateRegistered {
			head += " ○"
		}
		b.WriteString(m.st.netHeader.Width(sidebarWidth).Render(truncate(head, sidebarWidth)))
		b.WriteByte('\n')
		rows++
		for _, ch := range n.Channels {
			ref := bufRef{net: n.ID, name: ch.Name}
			label := "  " + bufferGlyph(ch) + ch.Name
			line := truncate(label, sidebarWidth-4)
			if hi := m.highlite[ref.key()]; hi > 0 {
				line += " " + m.st.badgeHi.Render(fmt.Sprintf("%d", hi))
			} else if u := m.unread[ref.key()]; u > 0 {
				line += " " + m.st.badge.Render(fmt.Sprintf("%d", u))
			}
			style := m.st.buffer
			switch {
			case ref.eq(m.active):
				style = m.st.bufferActive
			case m.unread[ref.key()] > 0:
				style = m.st.bufferUnread
			}
			b.WriteString(style.Width(sidebarWidth).Render(line))
			b.WriteByte('\n')
			rows++
			if rows >= maxRows {
				break
			}
		}
		if rows >= maxRows {
			break
		}
	}
	return m.st.sidebar.Width(sidebarWidth).Height(m.height).Render(b.String())
}

func bufferGlyph(ch *core.Channel) string {
	switch ch.Kind {
	case core.KindQuery:
		return "@"
	case core.KindStatus:
		return "*"
	default:
		return "" // channels already start with #/&; no extra glyph
	}
}

// renderMembers draws the member list of the active channel.
func (m *model) renderMembers() string {
	ch := m.channelOf(m.active)
	if ch == nil {
		return ""
	}
	type mem struct {
		nick   string
		prefix string
		rank   int
	}
	list := make([]mem, 0, len(ch.Members))
	for _, mm := range ch.Members {
		list = append(list, mem{nick: mm.Nick, prefix: mm.Modes, rank: rankOf(mm.Modes)})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].rank != list[j].rank {
			return list[i].rank < list[j].rank
		}
		return strings.ToLower(list[i].nick) < strings.ToLower(list[j].nick)
	})
	var b strings.Builder
	b.WriteString(m.st.label.Render(fmt.Sprintf("Members %d", len(list))))
	b.WriteByte('\n')
	for i, mm := range list {
		if i >= m.height-1 {
			break
		}
		style := m.st.memberNormal
		switch mm.rank {
		case 0, 1:
			style = m.st.memberOp
		case 2, 3:
			style = m.st.memberVoice
		}
		b.WriteString(style.Render(truncate(mm.prefix+mm.nick, membersWidth)))
		b.WriteByte('\n')
	}
	return m.st.members.Width(membersWidth).Height(m.height).Render(b.String())
}

// rankOf orders member prefixes: owner/admin/op < halfop < voice < none.
func rankOf(prefix string) int {
	if prefix == "" {
		return 5
	}
	switch prefix[0] {
	case '~', '&', '@':
		return 0
	case '%':
		return 2
	case '+':
		return 3
	default:
		return 5
	}
}

// renderMessages rebuilds the viewport content for the active buffer.
func (m *model) renderMessages() {
	if m.active.zero() {
		m.vp.SetContent("")
		return
	}
	bb := m.bufs[m.active.key()]
	if bb == nil || len(bb.msgs) == 0 {
		m.vp.SetContent(m.st.system.Render("  no messages yet"))
		return
	}
	w := m.vp.Width
	var b strings.Builder
	for i, msg := range bb.msgs {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(m.renderLine(msg, w))
	}
	m.vp.SetContent(b.String())
}

// renderLine formats one message: "HH:MM <nick> text", wrapped to width.
func (m *model) renderLine(msg core.Message, w int) string {
	ts := m.st.ts.Render(msg.Time.Local().Format("15:04"))
	body := m.formatBody(msg)
	line := ts + " " + body
	if msg.Highlight {
		return m.st.highlight.Width(w).Render(truncate(line, w*4))
	}
	return lipgloss.NewStyle().Width(w).Render(line)
}

func (m *model) formatBody(msg core.Message) string {
	switch msg.Kind {
	case core.MsgSystem, core.MsgJoin, core.MsgPart, core.MsgQuit, core.MsgNick, core.MsgTopic:
		return m.st.system.Render("— " + msg.Text)
	case core.MsgNotice:
		return m.st.notice.Render("-"+msg.From+"- ") + msg.Text
	case core.MsgAction:
		return m.st.action.Render("* " + msg.From + " " + msg.Text)
	default:
		nick := m.st.nickColor(msg.From).Render(msg.From)
		if msg.Self {
			nick = m.st.self.Render(msg.From)
		}
		return "<" + nick + "> " + msg.Text
	}
}

// overlayOn centers ov over base by compositing line by line.
func (m *model) overlayOn(base, ov string) string {
	bw, bh := m.width, m.height
	ow := lipgloss.Width(ov)
	oh := lipgloss.Height(ov)
	x := max((bw-ow)/2, 0)
	y := max((bh-oh)/2, 0)

	baseLines := strings.Split(base, "\n")
	ovLines := strings.Split(ov, "\n")
	for i, ol := range ovLines {
		row := y + i
		if row < 0 || row >= len(baseLines) {
			continue
		}
		bl := baseLines[row]
		left := truncateExact(bl, x)
		right := ""
		if rr := x + lipgloss.Width(ol); rr < lipgloss.Width(bl) {
			right = substrFrom(bl, rr)
		}
		baseLines[row] = left + ol + right
	}
	return strings.Join(baseLines, "\n")
}

// -- small text helpers ------------------------------------------------------

func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	return truncateExact(s, w-1) + "…"
}

// truncateExact returns the first w display cells of s (ANSI-aware via a
// rune walk; stugan messages are plain after IRC formatting is stripped).
func truncateExact(s string, w int) string {
	if w <= 0 {
		return ""
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= w {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

func substrFrom(s string, from int) string {
	n := 0
	for i, r := range s {
		if n >= from {
			return s[i:]
		}
		n++
		_ = r
	}
	return ""
}

var _ = time.Now
