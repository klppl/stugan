package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/klippelism/stugan/internal/core"
)

// pluginsOverlay lists Lua plugins and toggles/reloads them (Ctrl-G).
type pluginsOverlay struct {
	tab     int // 0: Installed, 1: Library
	list    []core.PluginInfo
	curated []core.CuratedPluginInfo
	sel     int
}

func newPluginsOverlay(m *model) *pluginsOverlay {
	p := &pluginsOverlay{}
	p.refresh(m)
	return p
}

func (p *pluginsOverlay) refresh(m *model) {
	if m == nil || m.eng == nil {
		return
	}
	p.list = m.eng.Plugins()
	p.curated = m.eng.CuratedPlugins()
	maxSel := len(p.list) - 1
	if p.tab == 1 {
		maxSel = len(p.curated) - 1
	}
	if p.sel > maxSel {
		p.sel = max(0, maxSel)
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
	case "tab", "left", "right":
		p.tab = 1 - p.tab
		p.sel = 0
		p.refresh(m)
		return p, nil
	case "up", "ctrl+p":
		p.sel = max(0, p.sel-1)
	case "down", "ctrl+n":
		maxSel := len(p.list) - 1
		if p.tab == 1 {
			maxSel = len(p.curated) - 1
		}
		p.sel = min(maxSel, p.sel+1)
	case "enter", " ": // toggle load/unload (tab 0) or download/install (tab 1)
		if p.tab == 0 {
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
		} else {
			if p.sel < len(p.curated) {
				cp := p.curated[p.sel]
				go func(name string) {
					if err := m.eng.DownloadPlugin(context.Background(), name); err != nil {
						m.setStatus("download plugin: " + err.Error())
					} else {
						m.setStatus("installed plugin " + name)
					}
				}(cp.Name)
			}
		}
	case "r", "u": // reload (tab 0) or update (tab 1)
		if p.tab == 0 && p.sel < len(p.list) {
			if err := m.eng.ReloadPlugin(p.list[p.sel].Name); err != nil {
				m.setStatus("reload: " + err.Error())
			}
			p.refresh(m)
		} else if p.tab == 1 && p.sel < len(p.curated) {
			cp := p.curated[p.sel]
			go func(name string) {
				if err := m.eng.UpdatePlugin(context.Background(), name); err != nil {
					m.setStatus("update plugin: " + err.Error())
				} else {
					m.setStatus("updated plugin " + name)
				}
			}(cp.Name)
		}
	}
	return p, nil
}

func (p *pluginsOverlay) View(m *model) string {
	var b strings.Builder

	tabInstalled := m.st.label.Render(" Installed ")
	tabLibrary := m.st.label.Render(" Library ")
	if p.tab == 0 {
		tabInstalled = m.st.fieldOn.Render("[ Installed ]")
	} else {
		tabLibrary = m.st.fieldOn.Render("[ Library ]")
	}

	b.WriteString(m.st.modalTitle.Render("Plugins") + "  " + tabInstalled + " " + tabLibrary)
	b.WriteByte('\n')

	if p.tab == 0 {
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
		b.WriteString(m.st.help.Render("tab switch · ↵/space toggle · r reload · esc close"))
	} else {
		if len(p.curated) == 0 {
			b.WriteString(m.st.help.Render("  no curated plugins available"))
			b.WriteByte('\n')
		}
		for i, cp := range p.curated {
			state := m.st.help.Render("remote")
			switch {
			case cp.UpdateAvailable:
				state = m.st.badgeHi.Render("update")
			case cp.Loaded:
				state = m.st.self.Render("installed")
			case cp.Installed:
				state = m.st.label.Render("installed")
			}
			head := fmt.Sprintf("%-18s %s", truncate(cp.Name, 18), state)
			line := "  " + head + "  " + m.st.label.Render(truncate(cp.Description, 34))
			if i == p.sel {
				line = m.st.fieldOn.Render("› "+head) + "  " + m.st.label.Render(truncate(cp.Description, 34))
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
		b.WriteString(m.st.help.Render("tab switch · ↵/space install/download · u update · esc close"))
	}

	return m.st.modal.Width(64).Render(b.String())
}
