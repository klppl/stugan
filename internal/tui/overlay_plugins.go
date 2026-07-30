package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/klippelism/stugan/internal/core"
)

// pluginsOverlay lists Lua plugins and toggles/reloads them (Ctrl-G).
type pluginsOverlay struct {
	list []core.PluginInfo
	sel  int
}

func newPluginsOverlay(m *model) *pluginsOverlay {
	p := &pluginsOverlay{}
	p.refresh(m)
	return p
}

func (p *pluginsOverlay) refresh(m *model) {
	p.list = m.eng.Plugins()
	if p.sel >= len(p.list) {
		p.sel = max(0, len(p.list)-1)
	}
}

func (p *pluginsOverlay) Update(m *model, msg tea.Msg) (overlay, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return p, nil
	}
	switch km.String() {
	case "esc", "ctrl+g":
		return nil, nil
	case "up", "ctrl+p":
		p.sel = max(0, p.sel-1)
	case "down", "ctrl+n":
		p.sel = min(len(p.list)-1, p.sel+1)
	case "enter", " ": // toggle load/unload
		if p.sel < len(p.list) {
			pl := p.list[p.sel]
			var err error
			if pl.Loaded {
				err = m.eng.UnloadPlugin(pl.Name)
			} else {
				err = m.eng.LoadPlugin(pl.Name)
			}
			if err != nil {
				m.setStatus("plugin: " + err.Error())
			}
			p.refresh(m)
		}
	case "r": // reload
		if p.sel < len(p.list) {
			if err := m.eng.ReloadPlugin(p.list[p.sel].Name); err != nil {
				m.setStatus("reload: " + err.Error())
			}
			p.refresh(m)
		}
	}
	return p, nil
}

func (p *pluginsOverlay) View(m *model) string {
	var b strings.Builder
	b.WriteString(m.st.modalTitle.Render("Plugins"))
	b.WriteByte('\n')
	if len(p.list) == 0 {
		b.WriteString(m.st.help.Render("  no scripts in the plugins directory"))
		b.WriteByte('\n')
	}
	for i, pl := range p.list {
		state := m.st.help.Render("off")
		switch {
		case pl.Disabled:
			state = m.st.errText.Render("disabled")
		case pl.Loaded:
			state = m.st.self.Render("on ")
		}
		head := fmt.Sprintf("%-18s %s", truncate(pl.Name, 18), state)
		desc := pl.Description
		if pl.Errors > 0 {
			desc = fmt.Sprintf("%d errors · %s", pl.Errors, desc)
		}
		line := "  " + head + "  " + m.st.label.Render(truncate(desc, 34))
		if i == p.sel {
			line = m.st.fieldOn.Render("› "+head) + "  " + m.st.label.Render(truncate(desc, 34))
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString(m.st.help.Render("↵/space toggle · r reload · esc close"))
	return m.st.modal.Width(64).Render(b.String())
}
