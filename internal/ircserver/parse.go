package ircserver

import (
	"strings"
	"unicode"
)

// Message is a parsed IRC message line according to RFC 1459 and IRCv3.
type Message struct {
	// RawTags is the raw string of client-only tags (e.g. "+typing=active;+draft/react=👍").
	// Server-authoritative tags sent by clients are discarded for security.
	RawTags string
	// Tags holds decoded tag key-value pairs.
	Tags map[string]string
	// Prefix is the optional message source (e.g. "nick!user@host").
	Prefix string
	// Command is the IRC command in uppercase (e.g. "PRIVMSG", "JOIN", "001").
	Command string
	// Params is the list of command parameters. The last parameter is the trailing argument.
	Params []string
}

const (
	maxInputBuffer     = 64 * 1024
	maxClientTagBytes  = 4094
	serverName         = "stugan.bouncer"
	defaultMaxPlayback = 50
)

// ParseLine parses a raw incoming IRC line from a client.
// It strips CR/LF, extracts client-only message tags (the '+' prefixed tags),
// strips prefixes, and parses the command and parameters.
func ParseLine(raw string) *Message {
	line := strings.TrimRight(raw, "\r\n\x00")
	if line == "" {
		return nil
	}

	var rawTags string
	tags := make(map[string]string)

	// IRCv3 Message Tags: starts with '@'
	if strings.HasPrefix(line, "@") {
		sp := strings.IndexByte(line, ' ')
		if sp == -1 {
			return nil
		}
		tagPart := line[1:sp]
		line = strings.TrimLeft(line[sp+1:], " ")

		// Only retain client-only tags (+prefixed) from clients.
		var kept []string
		for _, t := range strings.Split(tagPart, ";") {
			if t == "" {
				continue
			}
			eq := strings.IndexByte(t, '=')
			var key, val string
			if eq != -1 {
				key = t[:eq]
				val = UnescapeTagValue(t[eq+1:])
			} else {
				key = t
			}
			if strings.HasPrefix(key, "+") && len(key) > 1 {
				tags[key] = val
				kept = append(kept, t)
			}
		}
		joined := strings.Join(kept, ";")
		if len(kept) > 0 && len(joined) <= maxClientTagBytes {
			rawTags = joined
		}
	}

	// Strip optional leading prefix if sent by client
	if strings.HasPrefix(line, ":") {
		sp := strings.IndexByte(line, ' ')
		if sp == -1 {
			return nil
		}
		line = strings.TrimLeft(line[sp+1:], " ")
	}

	if line == "" {
		return nil
	}

	var trailing *string
	head := line
	if ti := strings.Index(line, " :"); ti != -1 {
		t := line[ti+2:]
		trailing = &t
		head = line[:ti]
	}

	parts := strings.Fields(head)
	if len(parts) == 0 {
		return nil
	}

	cmd := strings.ToUpper(parts[0])
	params := parts[1:]
	if trailing != nil {
		params = append(params, *trailing)
	}

	return &Message{
		RawTags: rawTags,
		Tags:    tags,
		Command: cmd,
		Params:  params,
	}
}

// FormatLine constructs an outbound IRC line with the specified tags, prefix, command, and params.
func FormatLine(tags, prefix, command string, params ...string) string {
	var sb strings.Builder
	if tags != "" {
		sb.WriteString("@")
		sb.WriteString(tags)
		sb.WriteString(" ")
	}
	if prefix != "" {
		sb.WriteString(":")
		sb.WriteString(prefix)
		sb.WriteString(" ")
	}
	sb.WriteString(command)

	if len(params) == 0 {
		return sb.String()
	}

	head := params[:len(params)-1]
	last := params[len(params)-1]

	for _, p := range head {
		sb.WriteString(" ")
		sb.WriteString(p)
	}

	sb.WriteString(" ")
	if strings.HasPrefix(last, ":") {
		sb.WriteString(last)
	} else if last == "" || strings.Contains(last, " ") {
		sb.WriteString(":")
		sb.WriteString(last)
	} else {
		sb.WriteString(last)
	}

	return sb.String()
}

// EscapeTagValue escapes tag values according to IRCv3 message tag escaping rules.
func EscapeTagValue(v string) string {
	var sb strings.Builder
	for _, r := range v {
		switch r {
		case ';':
			sb.WriteString(`\:`)
		case ' ':
			sb.WriteString(`\s`)
		case '\\':
			sb.WriteString(`\\`)
		case '\r':
			sb.WriteString(`\r`)
		case '\n':
			sb.WriteString(`\n`)
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// UnescapeTagValue decodes an escaped IRCv3 tag value.
func UnescapeTagValue(v string) string {
	var sb strings.Builder
	escaped := false
	for _, r := range v {
		if escaped {
			switch r {
			case ':':
				sb.WriteByte(';')
			case 's':
				sb.WriteByte(' ')
			case '\\':
				sb.WriteByte('\\')
			case 'r':
				sb.WriteByte('\r')
			case 'n':
				sb.WriteByte('\n')
			default:
				sb.WriteRune(r)
			}
			escaped = false
		} else if r == '\\' {
			escaped = true
		} else {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// ParsedLogin contains the components extracted from a login string.
type ParsedLogin struct {
	Username string
	Network  string
	Client   string
}

// UnmarshalLogin parses a bouncer login string of the format `username[/network][@client]`
// or `username[@client][/network]`.
func UnmarshalLogin(raw string) ParsedLogin {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ParsedLogin{}
	}

	username := raw
	var network, client string

	i := strings.IndexAny(raw, "/@")
	lastSlash := strings.LastIndexByte(raw, '/')
	lastAt := strings.LastIndexByte(raw, '@')
	j := lastSlash
	if lastAt > j {
		j = lastAt
	}

	if i >= 0 {
		username = raw[:i]
	}
	if j >= 0 {
		if raw[j] == '@' {
			client = raw[j+1:]
		} else {
			network = raw[j+1:]
		}
	}
	if i >= 0 && j >= 0 && i < j {
		if raw[i] == '@' {
			client = raw[i+1 : j]
		} else {
			network = raw[i+1 : j]
		}
	}

	return ParsedLogin{
		Username: username,
		Network:  network,
		Client:   client,
	}
}

// ParsedCredentials holds the username, password/secret, and optional network target.
type ParsedCredentials struct {
	Username string
	Secret   string
	Network  string
}

// ParseCredentials extracts credentials from PASS and USER command inputs.
// Handles ZNC-style `PASS user[/network]:secret` as well as standard PASS + USER.
func ParseCredentials(passRaw, userField string) (*ParsedCredentials, bool) {
	loginPart := ""
	secret := passRaw

	if colon := strings.IndexByte(passRaw, ':'); colon != -1 {
		loginPart = passRaw[:colon]
		secret = passRaw[colon+1:]
	}

	if loginPart == "" {
		loginPart = userField
	}

	parsed := UnmarshalLogin(loginPart)
	network := parsed.Network

	// If network was not in PASS, check if USER has a network selector
	if network == "" && userField != "" {
		network = UnmarshalLogin(userField).Network
	}

	username := parsed.Username
	if username == "" && secret == "" {
		return nil, false
	}

	return &ParsedCredentials{
		Username: username,
		Secret:   secret,
		Network:  network,
	}, true
}

// CleanNick sanitizes a nickname to comply with IRC rules.
func CleanNick(nick string) string {
	nick = strings.TrimSpace(nick)
	if nick == "" {
		return "user"
	}
	var sb strings.Builder
	for i, r := range nick {
		if i == 0 && (unicode.IsDigit(r) || r == '-') {
			sb.WriteString("u")
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(`-[]\`+"`"+`^{}_|`, r) {
			sb.WriteRune(r)
		}
	}
	res := sb.String()
	if res == "" {
		return "user"
	}
	if len(res) > 32 {
		res = res[:32]
	}
	return res
}
