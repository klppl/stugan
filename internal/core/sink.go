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

// toLowerASCII lowercases a string for case-insensitive map keys. IRC
// casemapping is server-defined; rfc1459 mapping arrives with ISUPPORT.
func toLowerASCII(s string) string { return strings.ToLower(s) }
