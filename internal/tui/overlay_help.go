package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// helpOverlay is a static key reference.
type helpOverlay struct{}

func newHelpOverlay() *helpOverlay { return &helpOverlay{} }

func (h *helpOverlay) Update(m *model, msg tea.Msg) (overlay, tea.Cmd) {
	if _, ok := msg.(tea.KeyMsg); ok {
		return nil, nil // any key closes
	}
	return h, nil
}

func (h *helpOverlay) View(m *model) string {
	rows := [][2]string{
		{"type + enter", "send a message or /command"},
		{"^n / ^p", "next / previous buffer"},
		{"⌥↑ / ⌥↓", "previous / next network"},
		{"^k", "quick switcher"},
		{"^o", "networks (add/edit/connect/remove)"},
		{"^l", "channel list browser"},
		{"^g", "plugins"},
		{"^w", "toggle member list"},
		{"^x", "close current buffer"},
		{"pgup / pgdn", "scroll history"},
		{"f1", "this help"},
		{"^c", "quit"},
	}
	var b strings.Builder
	b.WriteString(m.st.modalTitle.Render("stugan — keys"))
	b.WriteString("\n\n")
	for _, r := range rows {
		b.WriteString(m.st.fieldOn.Render(pad(r[0], 14)))
		b.WriteString(m.st.label.Render(r[1]))
		b.WriteByte('\n')
	}
	b.WriteString("\n")
	b.WriteString(m.st.help.Render("any key to close"))
	return m.st.modal.Render(b.String())
}

func pad(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}
