package tui

import "github.com/charmbracelet/lipgloss"

// styles holds every lipgloss style the views use, built from one session's
// renderer so colors match the connecting terminal's profile. Kept in a
// struct (not package globals) because each SSH session has its own renderer.
type styles struct {
	r *lipgloss.Renderer

	sidebar      lipgloss.Style
	netHeader    lipgloss.Style
	buffer       lipgloss.Style
	bufferActive lipgloss.Style
	bufferUnread lipgloss.Style
	badge        lipgloss.Style
	badgeHi      lipgloss.Style

	topic     lipgloss.Style
	statusBar lipgloss.Style

	members      lipgloss.Style
	memberOp     lipgloss.Style
	memberVoice  lipgloss.Style
	memberNormal lipgloss.Style

	ts        lipgloss.Style
	nick      lipgloss.Style
	self      lipgloss.Style
	system    lipgloss.Style
	highlight lipgloss.Style
	notice    lipgloss.Style
	action    lipgloss.Style

	input       lipgloss.Style
	inputPrompt lipgloss.Style

	modal      lipgloss.Style
	modalTitle lipgloss.Style
	label      lipgloss.Style
	fieldOn    lipgloss.Style
	help       lipgloss.Style
	errText    lipgloss.Style
}

func newStyles(r *lipgloss.Renderer) *styles {
	c := func(s string) lipgloss.Color { return lipgloss.Color(s) }
	base := r.NewStyle()
	return &styles{
		r:            r,
		sidebar:      base.Foreground(c("250")),
		netHeader:    base.Foreground(c("111")).Bold(true),
		buffer:       base.Foreground(c("245")),
		bufferActive: base.Foreground(c("231")).Background(c("24")).Bold(true),
		bufferUnread: base.Foreground(c("252")).Bold(true),
		badge:        base.Foreground(c("236")).Background(c("244")),
		badgeHi:      base.Foreground(c("231")).Background(c("197")).Bold(true),

		topic:     base.Foreground(c("252")).Background(c("236")).Bold(true),
		statusBar: base.Foreground(c("250")).Background(c("236")),

		members:      base.Foreground(c("250")),
		memberOp:     base.Foreground(c("197")).Bold(true),
		memberVoice:  base.Foreground(c("184")),
		memberNormal: base.Foreground(c("250")),

		ts:        base.Foreground(c("240")),
		nick:      base.Foreground(c("111")),
		self:      base.Foreground(c("150")),
		system:    base.Foreground(c("240")).Italic(true),
		highlight: base.Foreground(c("231")).Background(c("88")),
		notice:    base.Foreground(c("179")),
		action:    base.Foreground(c("176")).Italic(true),

		input:       base.Foreground(c("252")),
		inputPrompt: base.Foreground(c("111")).Bold(true),

		modal:      base.Border(lipgloss.RoundedBorder()).BorderForeground(c("111")).Padding(0, 1),
		modalTitle: base.Foreground(c("111")).Bold(true),
		label:      base.Foreground(c("245")),
		fieldOn:    base.Foreground(c("231")).Bold(true),
		help:       base.Foreground(c("240")),
		errText:    base.Foreground(c("197")).Bold(true),
	}
}

// nickColor derives a stable 256-color for a nick so the same speaker keeps
// one color across a session.
func (s *styles) nickColor(nick string) lipgloss.Style {
	var h uint32 = 2166136261
	for i := 0; i < len(nick); i++ {
		h = (h ^ uint32(nick[i])) * 16777619
	}
	// A readable subset of the 256-color cube (skip the dark/low range).
	palette := []string{"75", "111", "150", "180", "210", "114", "179", "141", "216", "108", "153", "174"}
	return s.r.NewStyle().Foreground(lipgloss.Color(palette[h%uint32(len(palette))]))
}
