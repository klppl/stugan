package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/klippelism/stugan/internal/core"
)

// browserOverlay is the channel-list (LIST) browser for the active network
// (Ctrl-L). It fires a LIST, shows results, filters client-side, and joins
// the selected channel.
type browserOverlay struct {
	network string
	in      textinput.Model
	all     []core.ChannelListItem
	shown   []core.ChannelListItem
	sel     int
	loading bool
}

func newBrowserOverlay(m *model) *browserOverlay {
	in := textinput.New()
	in.Prompt = "filter › "
	in.Focus()
	return &browserOverlay{network: m.active.net, in: in, loading: true}
}

// request kicks off the LIST for the network. Results arrive as
// channelListMsg and are handed back via setResults.
func (b *browserOverlay) request(m *model) tea.Cmd {
	net := b.network
	eng := m.eng
	return func() tea.Msg {
		if err := eng.ListChannels(net, ""); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

func (b *browserOverlay) setResults(items []core.ChannelListItem) {
	b.loading = false
	b.all = items
	sort.Slice(b.all, func(i, j int) bool { return b.all[i].Users > b.all[j].Users })
	b.applyFilter()
}

func (b *browserOverlay) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(b.in.Value()))
	b.shown = b.shown[:0]
	for _, it := range b.all {
		if q == "" || strings.Contains(strings.ToLower(it.Name), q) || strings.Contains(strings.ToLower(it.Topic), q) {
			b.shown = append(b.shown, it)
		}
	}
	if b.sel >= len(b.shown) {
		b.sel = max(0, len(b.shown)-1)
	}
}

func (b *browserOverlay) Update(m *model, msg tea.Msg) (overlay, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return b, nil
	}
	switch km.String() {
	case "esc":
		return nil, nil
	case "up", "ctrl+p":
		b.sel = max(0, b.sel-1)
		return b, nil
	case "down", "ctrl+n":
		b.sel = min(len(b.shown)-1, b.sel+1)
		return b, nil
	case "enter":
		if b.sel < len(b.shown) {
			ch := b.shown[b.sel].Name
			m.eng.SendInput(b.network, "", "/join "+ch)
			m.setStatus("joining " + ch)
			return nil, m.refreshSnapshot
		}
		return nil, nil
	}
	var cmd tea.Cmd
	b.in, cmd = b.in.Update(km)
	b.applyFilter()
	return b, cmd
}

func (b *browserOverlay) View(m *model) string {
	var sb strings.Builder
	sb.WriteString(m.st.modalTitle.Render("Channels on " + b.network))
	sb.WriteByte('\n')
	sb.WriteString(b.in.View())
	sb.WriteByte('\n')
	switch {
	case b.loading:
		sb.WriteString(m.st.help.Render("  requesting list…"))
		sb.WriteByte('\n')
	case len(b.all) == 0:
		sb.WriteString(m.st.help.Render("  (empty or not received yet)"))
		sb.WriteByte('\n')
	}
	shown := b.shown
	const maxRows = 12
	if len(shown) > maxRows {
		shown = shown[:maxRows]
	}
	for i, it := range shown {
		head := fmt.Sprintf("%-24s %5d", truncate(it.Name, 24), it.Users)
		line := "  " + head + "  " + m.st.label.Render(truncate(it.Topic, 34))
		if i == b.sel {
			line = m.st.fieldOn.Render("› "+head) + "  " + m.st.label.Render(truncate(it.Topic, 34))
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	sb.WriteString(m.st.help.Render(fmt.Sprintf("%d shown · ↵ join · esc close", len(b.shown))))
	return m.st.modal.Width(70).Render(sb.String())
}
