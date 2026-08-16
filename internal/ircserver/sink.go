package ircserver

import (
	"fmt"
	"strings"
	"time"

	"github.com/klippelism/stugan/internal/core"
)

// ircSink implements core.Sink for a specific user, fanning out committed events
// from the Engine to all currently attached bouncer sessions.
type ircSink struct {
	srv  *Server
	user string
}

func (s *ircSink) Print(m core.Message) {
	s.srv.broadcastPrint(s.user, m)
}

func (s *ircSink) NetworkChanged(n *core.Network) {
	s.srv.broadcastNetworkChanged(s.user, n)
}

func (s *ircSink) NetworkRemoved(networkID string) {
	s.srv.broadcastNetworkRemoved(s.user, networkID)
}

func (s *ircSink) NetworksReordered(networkIDs []string) {}

func (s *ircSink) ChannelList(network string, items []core.ChannelListItem) {
	s.srv.broadcastChannelList(s.user, network, items)
}

func (s *ircSink) Typing(network, buffer, nick, state string) {
	s.srv.broadcastTyping(s.user, network, buffer, nick, state)
}

func (s *ircSink) React(network, buffer, target, nick, reaction string) {
	s.srv.broadcastReact(s.user, network, buffer, target, nick, reaction)
}

func (s *ircSink) Redact(network, buffer, target, nick, reason string) {
	s.srv.broadcastRedact(s.user, network, buffer, target, nick, reason)
}

func (s *ircSink) ReadMarker(network, buffer string, ts time.Time) {
	s.srv.broadcastReadMarker(s.user, network, buffer, ts)
}

// formatSinkMessage formats a live committed core.Message into IRC wire format for a session.
func (sess *Session) formatSinkMessage(m core.Message) []string {
	if m.Network != sess.networkID {
		return nil
	}

	selfNick := sess.currentNick()
	if selfNick == "" {
		selfNick = sess.clientNick
	}
	if selfNick == "" {
		selfNick = "*"
	}

	fromNick := m.From
	if fromNick == "" {
		fromNick = "unknown"
	}
	prefix := fmt.Sprintf("%s!stugan@%s", fromNick, serverName)

	isChannel := strings.HasPrefix(m.Buffer, "#") || strings.HasPrefix(m.Buffer, "&")

	target := m.Buffer
	if !isChannel {
		if m.Self {
			target = m.Buffer
		} else {
			target = selfNick
		}
	}

	// For DMs, cross-client sync requires self-message or echo-message caps
	if m.Self && !isChannel && !sess.wantsSelfMessages() {
		return nil
	}

	cmd := "PRIVMSG"
	switch m.Kind {
	case core.MsgNotice:
		cmd = "NOTICE"
	case core.MsgAction:
		cmd = "PRIVMSG"
	case core.MsgSystem:
		// Render system lines as notices from server
		prefix = serverName
		cmd = "NOTICE"
		target = selfNick
	case core.MsgJoin:
		return []string{FormatLine("", prefix, "JOIN", m.Buffer)}
	case core.MsgPart:
		return []string{FormatLine("", prefix, "PART", m.Buffer, m.Text)}
	case core.MsgQuit:
		return []string{FormatLine("", prefix, "QUIT", m.Text)}
	case core.MsgNick:
		return []string{FormatLine("", prefix, "NICK", m.Text)}
	case core.MsgTopic:
		return []string{FormatLine("", prefix, "TOPIC", m.Buffer, m.Text)}
	}

	var bodies []string
	if m.Kind == core.MsgAction {
		bodies = []string{fmt.Sprintf("\x01ACTION %s\x01", strings.ReplaceAll(m.Text, "\n", " "))}
	} else {
		bodies = strings.Split(m.Text, "\n")
	}

	var lines []string
	for _, body := range bodies {
		tags := sess.formatTags(m.Time, m.ID, "")
		lines = append(lines, FormatLine(tags, prefix, cmd, target, body))
	}

	return lines
}
