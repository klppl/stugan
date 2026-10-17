package engine

import "strings"

// ircLine is the minimal parse the engine needs: it relays bytes and only
// looks at the source nick, command and params of a handful of commands.
type ircLine struct {
	nick   string // source nick ("" for server-sourced or unprefixed lines)
	cmd    string // upper-cased command or numeric
	params []string
}

// parseLine splits a raw IRC line (no CRLF). Tags are skipped. ok is false for
// a line the IRC client (girc) would refuse to parse: an empty or
// unterminated tag section or prefix, or no command.
func parseLine(raw string) (l ircLine, ok bool) {
	s := raw
	if strings.HasPrefix(s, "@") {
		i := strings.IndexByte(s, ' ')
		if i < 2 {
			return l, false
		}
		s = strings.TrimLeft(s[i+1:], " ")
	}
	if strings.HasPrefix(s, ":") {
		i := strings.IndexByte(s, ' ')
		if i < 2 {
			return l, false
		}
		src := s[1:i]
		if j := strings.IndexAny(src, "!@"); j >= 0 {
			src = src[:j]
		}
		l.nick = src
		s = strings.TrimLeft(s[i+1:], " ")
	}
	if s == "" {
		return l, false
	}
	cmd, rest, _ := strings.Cut(s, " ")
	l.cmd = strings.ToUpper(cmd)
	for rest != "" {
		rest = strings.TrimLeft(rest, " ")
		if rest == "" {
			break
		}
		if rest[0] == ':' {
			l.params = append(l.params, rest[1:])
			break
		}
		var p string
		p, rest, _ = strings.Cut(rest, " ")
		l.params = append(l.params, p)
	}
	return l, true
}

// param returns params[i] or "".
func (l ircLine) param(i int) string {
	if i < len(l.params) {
		return l.params[i]
	}
	return ""
}

// last returns the final param or "".
func (l ircLine) last() string {
	if len(l.params) == 0 {
		return ""
	}
	return l.params[len(l.params)-1]
}

// fold lower-cases s under RFC 1459 casemapping ([]\~ are the lower-case
// forms of {}|^), the IRC default and what girc uses on the stugan side.
func fold(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'A' && c <= 'Z':
			b[i] = c + ('a' - 'A')
		case c == '[':
			b[i] = '{'
		case c == ']':
			b[i] = '}'
		case c == '\\':
			b[i] = '|'
		case c == '~':
			b[i] = '^'
		}
	}
	return string(b)
}

// burstCommands are the registration-time lines a fresh IRC client needs to
// believe it registered: capability negotiation, SASL, the welcome numerics
// (001–005, with ISUPPORT), 042 (unique id) and the SASL results. MOTD text,
// notices and anything else is left out of the replay: it would be shown (and
// stored) again on every re-attach.
var burstCommands = map[string]bool{
	"CAP": true, "AUTHENTICATE": true,
	"001": true, "002": true, "003": true, "004": true, "005": true,
	"042": true,
	"900": true, "901": true, "903": true, "907": true, "908": true,
}

// endOfBurst reports whether cmd closes the registration burst: end of MOTD,
// or no MOTD.
func endOfBurst(cmd string) bool { return cmd == "376" || cmd == "422" }

// rewriteTarget returns raw with its first param (after the command) replaced
// by nick. Used on the recorded 001 so a replay registers the client under the
// nick it has now, not the one it registered with: a replayed NICK after 001
// would race girc's welcome handler, which sets the nick on another goroutine.
func rewriteTarget(raw, nick string) string {
	prefix := ""
	s := raw
	if strings.HasPrefix(s, "@") {
		i := strings.IndexByte(s, ' ')
		if i < 0 {
			return raw
		}
		prefix, s = s[:i+1], s[i+1:]
	}
	if strings.HasPrefix(s, ":") {
		i := strings.IndexByte(s, ' ')
		if i < 0 {
			return raw
		}
		prefix, s = prefix+s[:i+1], s[i+1:]
	}
	cmd, rest, ok := strings.Cut(s, " ")
	if !ok || rest == "" || rest[0] == ':' {
		return raw
	}
	_, after, _ := strings.Cut(rest, " ")
	out := prefix + cmd + " " + nick
	if after != "" {
		out += " " + after
	}
	return out
}
