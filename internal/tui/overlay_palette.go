package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// paletteOverlay is a fuzzy quick-switcher over all buffers (Ctrl-K).
type paletteOverlay struct {
	in      textinput.Model
	matches []paletteItem
	sel     int
}

type paletteItem struct {
	ref   bufRef
	label string
}

func newPaletteOverlay(m *model) *paletteOverlay {
	in := textinput.New()
	in.Prompt = "› "
	in.Placeholder = "jump to buffer…"
	in.Focus()
	p := &paletteOverlay{in: in}
	p.filter(m)
	return p
}

func (p *paletteOverlay) filter(m *model) {
	q := strings.ToLower(strings.TrimSpace(p.in.Value()))
	p.matches = p.matches[:0]
	for _, n := range m.snap.Networks {
		for _, ch := range n.Channels {
			label := n.Name + " / " + ch.Name
			if q == "" || strings.Contains(strings.ToLower(label), q) {
				p.matches = append(p.matches, paletteItem{ref: bufRef{net: n.ID, name: ch.Name}, label: label})
			}
		}
	}
	if p.sel >= len(p.matches) {
		p.sel = max(0, len(p.matches)-1)
	}
}

func (p *paletteOverlay) Update(m *model, msg tea.Msg) (overlay, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return p, nil
	}
	switch km.String() {
	case "esc":
		return nil, nil
	case "up", "ctrl+p":
		p.sel = max(0, p.sel-1)
		return p, nil
	case "down", "ctrl+n":
		p.sel = min(len(p.matches)-1, p.sel+1)
		return p, nil
	case "enter":
		if p.sel < len(p.matches) {
			ref := p.matches[p.sel].ref
			for i, b := range m.order {
				if b.eq(ref) {
					return nil, m.selectIndex(i)
				}
			}
		}
		return nil, nil
	}
	var cmd tea.Cmd
	p.in, cmd = p.in.Update(km)
	p.filter(m)
	return p, cmd
}

func (p *paletteOverlay) View(m *model) string {
	var b strings.Builder
	b.WriteString(m.st.modalTitle.Render("Switch buffer"))
	b.WriteByte('\n')
	b.WriteString(p.in.View())
	b.WriteByte('\n')
	shown := p.matches
	const maxRows = 10
	if len(shown) > maxRows {
		shown = shown[:maxRows]
	}
	for i, it := range shown {
		line := "  " + it.label
		if i == p.sel {
			line = m.st.fieldOn.Render("› " + it.label)
			if u := m.unread[it.ref.key()]; u > 0 {
				line += m.st.label.Render(fmt.Sprintf("  (%d)", u))
			}
		} else if u := m.unread[it.ref.key()]; u > 0 {
			line += m.st.label.Render(fmt.Sprintf("  (%d)", u))
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if len(p.matches) == 0 {
		b.WriteString(m.st.help.Render("  no matches"))
		b.WriteByte('\n')
	}
	b.WriteString(m.st.help.Render("↑↓ move · ↵ open · esc cancel"))
	return m.st.modal.Width(40).Render(b.String())
}
