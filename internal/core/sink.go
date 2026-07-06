package core

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// logSink is the default Sink: it renders each committed line to stdout in
// a compact, human-readable form. Used in Phase 1 before the server bridge
// and store sinks exist.
type logSink struct{ log *slog.Logger }

func (s logSink) Print(m Message) {
	ts := m.Time.Format("15:04:05")
	loc := fmt.Sprintf("[%s/%s]", m.Network, m.Buffer)
	var line string
	switch m.Kind {
	case MsgSystem:
		line = fmt.Sprintf("%s %s -!- %s", ts, loc, m.Text)
	case MsgAction:
		line = fmt.Sprintf("%s %s * %s %s", ts, loc, m.From, m.Text)
	case MsgNotice:
		line = fmt.Sprintf("%s %s -%s- %s", ts, loc, m.From, m.Text)
	default:
		line = fmt.Sprintf("%s %s <%s> %s", ts, loc, m.From, m.Text)
	}
	fmt.Fprintln(os.Stdout, line)
}

// NetworkChanged is a no-op for the terminal sink: structural changes are
// already visible via the system lines Print emits.
func (s logSink) NetworkChanged(*Network) {}

// NetworkRemoved is a no-op for the terminal sink.
func (s logSink) NetworkRemoved(string) {}

// NetworksReordered is a no-op for the terminal sink (it has no sidebar).
func (s logSink) NetworksReordered([]string) {}

// ChannelList is a no-op for the terminal sink.
func (s logSink) ChannelList(string, []ChannelListItem) {}

// Typing is a no-op for the terminal sink.
func (s logSink) Typing(string, string, string, string) {}

// React and Redact are no-ops for the terminal sink.
func (s logSink) React(string, string, string, string, string)  {}
func (s logSink) Redact(string, string, string, string, string) {}

// toLowerASCII folds a string for case-insensitive map keys using rfc1459
// casemapping: ASCII A–Z plus []\~ fold to {}|^ (RFC 1459 §2.2 — those
// bytes are "uppercase" in IRC because Scandinavian charsets mapped them to
// letters). Virtually every server uses rfc1459 or its ascii subset, for
// which this is also correct; honoring an explicit ISUPPORT CASEMAPPING
// would need per-network folding and hasn't been worth the plumbing.
func toLowerASCII(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		case r == '[':
			return '{'
		case r == ']':
			return '}'
		case r == '\\':
			return '|'
		case r == '~':
			return '^'
		}
		return r
	}, s)
}
