package ircserver

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/klippelism/stugan/internal/core"
)

// MemberPrefixSymbol returns the highest prefix symbol for a member's mode string.
func MemberPrefixSymbol(modes string) string {
	if len(modes) > 0 {
		return string(modes[0])
	}
	return ""
}

// BuildNamesLines chunks member names into standard 353 RPL_NAMREPLY lines under 480 bytes,
// terminated by a 366 RPL_ENDOFNAMES line.
func BuildNamesLines(nick, channel string, names []string) []string {
	base := fmt.Sprintf(":%s 353 %s = %s :", serverName, nick, channel)
	budget := 480 - len(base)
	if budget < 64 {
		budget = 64
	}

	var lines []string
	var chunk []string
	currLen := 0

	for _, name := range names {
		add := len(name)
		if len(chunk) > 0 {
			add++ // space separator
		}
		if currLen+add > budget && len(chunk) > 0 {
			lines = append(lines, base+strings.Join(chunk, " "))
			chunk = chunk[:0]
			currLen = 0
			add = len(name)
		}
		chunk = append(chunk, name)
		currLen += add
	}

	if len(chunk) > 0 {
		lines = append(lines, base+strings.Join(chunk, " "))
	}
	lines = append(lines, fmt.Sprintf(":%s 366 %s %s :End of /NAMES list.", serverName, nick, channel))
	return lines
}

// FormatIrcTime returns an ISO8601 UTC timestamp string with millisecond precision.
func FormatIrcTime(t time.Time) string {
	if t.IsZero() {
		t = time.Now().UTC()
	} else {
		t = t.UTC()
	}
	return t.Format("2006-01-02T15:04:05.000Z")
}

var servicesNickRegex = regexp.MustCompile(`^[a-zA-Z]+serv$`)

// IsServicesNick checks if a nickname is likely an IRC network service bot.
func IsServicesNick(nick string) bool {
	lower := strings.ToLower(nick)
	if servicesNickRegex.MatchString(lower) {
		return true
	}
	switch lower {
	case "global", "services", "q", "x", "w", "chanserv", "nickserv", "memoserv", "operserv", "authserv":
		return true
	}
	return false
}

// FormatTags constructs the leading IRCv3 message tags block based on the session's negotiated caps.
func (s *Session) formatTags(msgTime time.Time, msgID string, batchRef string) string {
	var tags []string
	if batchRef != "" {
		tags = append(tags, fmt.Sprintf("batch=%s", batchRef))
	}
	if s.caps[CapServerTime] && !msgTime.IsZero() {
		tags = append(tags, fmt.Sprintf("time=%s", FormatIrcTime(msgTime)))
	}
	if s.caps[CapMessageTags] && msgID != "" {
		tags = append(tags, fmt.Sprintf("msgid=%s", msgID))
	}
	if len(tags) == 0 {
		return ""
	}
	return strings.Join(tags, ";")
}

// formatPlaybackMessage formats a historical message for playback to an attached client.
func (s *Session) formatPlaybackMessage(m core.Message, isChannel bool, batchRef string) []string {
	if m.Kind != core.MsgPrivmsg && m.Kind != core.MsgAction && m.Kind != core.MsgNotice {
		return nil
	}
	if m.Text == "" {
		return nil
	}

	selfNick := s.currentNick()
	if selfNick == "" {
		selfNick = s.clientNick
	}
	if selfNick == "" {
		selfNick = "*"
	}

	// For DM buffers, don't replay self-messages unless the client opted in to self-messages
	if m.Self && !isChannel && !s.wantsSelfMessages() {
		return nil
	}

	// Never replay our own lines sent to services (e.g. passwords in IDENTIFY)
	if m.Self && !isChannel && IsServicesNick(m.Buffer) {
		return nil
	}

	fromNick := m.From
	if fromNick == "" {
		fromNick = "unknown"
	}
	prefix := fmt.Sprintf("%s!stugan@%s", fromNick, serverName)

	target := m.Buffer
	if !isChannel {
		if m.Self {
			target = m.Buffer
		} else {
			target = selfNick
		}
	}

	cmd := "PRIVMSG"
	if m.Kind == core.MsgNotice {
		cmd = "NOTICE"
	}

	var bodies []string
	if m.Kind == core.MsgAction {
		bodies = []string{fmt.Sprintf("\x01ACTION %s\x01", strings.ReplaceAll(m.Text, "\n", " "))}
	} else {
		bodies = strings.Split(m.Text, "\n")
	}

	var lines []string
	for _, body := range bodies {
		tags := s.formatTags(m.Time, m.ID, batchRef)
		lines = append(lines, FormatLine(tags, prefix, cmd, target, body))
	}

	return lines
}
