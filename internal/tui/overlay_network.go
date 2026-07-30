package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/klippelism/stugan/internal/core"
)

// netListOverlay manages networks: connect/disconnect, add, edit, remove
// (Ctrl-O). Enter toggles connection; a/e/d add/edit/delete.
type netListOverlay struct {
	sel int
}

func newNetListOverlay(m *model) *netListOverlay { return &netListOverlay{} }

func (o *netListOverlay) Update(m *model, msg tea.Msg) (overlay, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return o, nil
	}
	nets := m.snap.Networks
	switch km.String() {
	case "esc", "ctrl+o":
		return nil, nil
	case "up", "ctrl+p":
		o.sel = max(0, o.sel-1)
	case "down", "ctrl+n":
		o.sel = min(len(nets)-1, o.sel+1)
	case "enter", " ": // toggle connection
		if o.sel < len(nets) {
			n := nets[o.sel]
			connect := n.State == core.StateDisconnected
			if err := m.eng.SetConnected(n.ID, connect); err != nil {
				m.setStatus("net: " + err.Error())
			}
			return o, m.refreshSnapshot
		}
	case "a": // add
		return newNetFormOverlay(m, ""), nil
	case "e": // edit
		if o.sel < len(nets) {
			return newNetFormOverlay(m, nets[o.sel].ID), nil
		}
	case "d": // remove
		if o.sel < len(nets) {
			id := nets[o.sel].ID
			if err := m.eng.RemoveNetwork(id); err != nil {
				m.setStatus("remove: " + err.Error())
			}
			o.sel = max(0, o.sel-1)
			return o, m.refreshSnapshot
		}
	}
	return o, nil
}

func (o *netListOverlay) View(m *model) string {
	var b strings.Builder
	b.WriteString(m.st.modalTitle.Render("Networks"))
	b.WriteByte('\n')
	for i, n := range m.snap.Networks {
		state := m.st.help.Render("disconnected")
		switch n.State {
		case core.StateRegistered:
			state = m.st.self.Render("connected")
		case core.StateConnecting:
			state = m.st.notice.Render("connecting")
		}
		head := pad(truncate(n.Name, 18), 18) + " " + state
		line := "  " + head
		if i == o.sel {
			line = m.st.fieldOn.Render("› " + head)
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if len(m.snap.Networks) == 0 {
		b.WriteString(m.st.help.Render("  no networks — press a to add"))
		b.WriteByte('\n')
	}
	b.WriteString(m.st.help.Render("↵ connect/disconnect · a add · e edit · d remove · esc close"))
	return m.st.modal.Width(48).Render(b.String())
}

// -- add / edit form ---------------------------------------------------------

// netFormOverlay adds or edits a network. editID is "" for add; otherwise the
// existing network whose non-form fields (Perform, CertPEM, keys) are carried
// through unchanged.
type netFormOverlay struct {
	editID string
	base   core.NetworkParams // existing params carried through on edit
	texts  []labeledInput
	bools  []labeledBool
	focus  int // over texts then bools
	err    string
}

type labeledInput struct {
	label string
	in    textinput.Model
}

type labeledBool struct {
	label string
	val   bool
}

func textField(label, val string, mask bool) labeledInput {
	in := textinput.New()
	in.Prompt = ""
	in.SetValue(val)
	if mask {
		in.EchoMode = textinput.EchoPassword
	}
	return labeledInput{label: label, in: in}
}

func newNetFormOverlay(m *model, editID string) *netFormOverlay {
	f := &netFormOverlay{editID: editID}
	var p core.NetworkParams
	if editID != "" {
		if got, ok := m.eng.NetworkConfig(editID); ok {
			p = got
		}
	} else {
		p.TLS = true
	}
	f.base = p
	f.texts = []labeledInput{
		textField("Name", p.Name, false),
		textField("Address (host:port)", p.Addr, false),
		textField("Nick", p.Nick, false),
		textField("Channels (comma)", strings.Join(p.Channels, ", "), false),
		textField("SASL user", p.SASLUser, false),
		textField("SASL pass", p.SASLPass, true),
		textField("Server pass", p.ServerPass, true),
	}
	if editID != "" {
		f.texts[0].in.SetValue(p.Name) // name is read-only on edit; shown for context
	}
	f.bools = []labeledBool{
		{label: "TLS", val: p.TLS},
		{label: "Allow self-signed (insecure)", val: p.Insecure},
		{label: "SASL EXTERNAL", val: p.SASLExternal},
	}
	f.setFocus(0)
	return f
}

func (f *netFormOverlay) setFocus(i int) {
	n := len(f.texts) + len(f.bools)
	f.focus = ((i % n) + n) % n
	for j := range f.texts {
		if j == f.focus {
			f.texts[j].in.Focus()
		} else {
			f.texts[j].in.Blur()
		}
	}
}

func (f *netFormOverlay) Update(m *model, msg tea.Msg) (overlay, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return f, nil
	}
	switch km.String() {
	case "esc":
		return newNetListOverlay(m), nil
	case "tab", "down":
		f.setFocus(f.focus + 1)
		return f, nil
	case "shift+tab", "up":
		f.setFocus(f.focus - 1)
		return f, nil
	case "ctrl+s":
		return f.save(m)
	case " ":
		if bi := f.focus - len(f.texts); bi >= 0 {
			f.bools[bi].val = !f.bools[bi].val
			return f, nil
		}
	}
	// Edit the focused text field.
	if f.focus < len(f.texts) {
		var cmd tea.Cmd
		f.texts[f.focus].in, cmd = f.texts[f.focus].in.Update(km)
		return f, cmd
	}
	return f, nil
}

func (f *netFormOverlay) save(m *model) (overlay, tea.Cmd) {
	get := func(i int) string { return strings.TrimSpace(f.texts[i].in.Value()) }
	name := get(0)
	addr := get(1)
	if name == "" || addr == "" {
		f.err = "name and address are required"
		return f, nil
	}
	p := f.base // carry Perform/CertPEM/keys/monitor through unchanged
	p.Name = name
	p.Addr = addr
	p.Nick = get(2)
	p.Channels = splitComma(get(3))
	p.SASLUser = get(4)
	p.SASLPass = f.texts[5].in.Value()
	p.ServerPass = f.texts[6].in.Value()
	p.TLS = f.bools[0].val
	p.Insecure = f.bools[1].val
	p.SASLExternal = f.bools[2].val

	var err error
	if f.editID == "" {
		p.ID = name
		err = m.eng.AddNetworkLive(p)
	} else {
		p.ID = f.editID
		p.Name = f.editID // id/name are the same key; not renamed here
		err = m.eng.UpdateNetwork(p)
	}
	if err != nil {
		f.err = err.Error()
		return f, nil
	}
	m.setStatus("saved " + name)
	return newNetListOverlay(m), m.refreshSnapshot
}

func (f *netFormOverlay) View(m *model) string {
	var b strings.Builder
	title := "Add network"
	if f.editID != "" {
		title = "Edit " + f.editID
	}
	b.WriteString(m.st.modalTitle.Render(title))
	b.WriteByte('\n')
	for i, t := range f.texts {
		lbl := m.st.label.Render(pad(t.label, 22))
		val := t.in.View()
		if i == f.focus {
			lbl = m.st.fieldOn.Render(pad("› "+t.label, 22))
		}
		b.WriteString(lbl + " " + val)
		b.WriteByte('\n')
	}
	for i, bl := range f.bools {
		box := "[ ]"
		if bl.val {
			box = "[x]"
		}
		lbl := m.st.label.Render(pad(bl.label, 22))
		if len(f.texts)+i == f.focus {
			lbl = m.st.fieldOn.Render(pad("› "+bl.label, 22))
		}
		b.WriteString(lbl + " " + box)
		b.WriteByte('\n')
	}
	if f.err != "" {
		b.WriteString(m.st.errText.Render(f.err))
		b.WriteByte('\n')
	}
	b.WriteString(m.st.help.Render("tab move · space toggle · ^s save · esc back"))
	return m.st.modal.Width(60).Render(b.String())
}

func splitComma(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
