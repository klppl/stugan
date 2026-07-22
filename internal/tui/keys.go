package tui

import "github.com/charmbracelet/bubbles/key"

// keymap is the global key binding set. Buffer navigation is on Ctrl/Alt
// chords so plain typing (including "/commands") always flows to the input.
type keymap struct {
	NextBuffer  key.Binding
	PrevBuffer  key.Binding
	NextNetwork key.Binding
	PrevNetwork key.Binding
	ScrollUp    key.Binding
	ScrollDown  key.Binding
	PageUp      key.Binding
	PageDown    key.Binding
	Send        key.Binding
	Members     key.Binding
	Palette     key.Binding
	Networks    key.Binding
	Browser     key.Binding
	Plugins     key.Binding
	CloseBuffer key.Binding
	Help        key.Binding
	Quit        key.Binding
}

func defaultKeymap() keymap {
	return keymap{
		NextBuffer:  key.NewBinding(key.WithKeys("ctrl+n"), key.WithHelp("^n", "next buf")),
		PrevBuffer:  key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("^p", "prev buf")),
		NextNetwork: key.NewBinding(key.WithKeys("alt+down"), key.WithHelp("⌥↓", "next net")),
		PrevNetwork: key.NewBinding(key.WithKeys("alt+up"), key.WithHelp("⌥↑", "prev net")),
		ScrollUp:    key.NewBinding(key.WithKeys("pgup", "ctrl+u")),
		ScrollDown:  key.NewBinding(key.WithKeys("pgdown", "ctrl+d")),
		PageUp:      key.NewBinding(key.WithKeys("pgup")),
		PageDown:    key.NewBinding(key.WithKeys("pgdown")),
		Send:        key.NewBinding(key.WithKeys("enter"), key.WithHelp("↵", "send")),
		Members:     key.NewBinding(key.WithKeys("ctrl+w"), key.WithHelp("^w", "members")),
		Palette:     key.NewBinding(key.WithKeys("ctrl+k"), key.WithHelp("^k", "switch")),
		Networks:    key.NewBinding(key.WithKeys("ctrl+o"), key.WithHelp("^o", "networks")),
		Browser:     key.NewBinding(key.WithKeys("ctrl+l"), key.WithHelp("^l", "list")),
		Plugins:     key.NewBinding(key.WithKeys("ctrl+g"), key.WithHelp("^g", "plugins")),
		CloseBuffer: key.NewBinding(key.WithKeys("ctrl+x"), key.WithHelp("^x", "close buf")),
		Help:        key.NewBinding(key.WithKeys("f1"), key.WithHelp("f1", "help")),
		Quit:        key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("^c", "quit")),
	}
}
