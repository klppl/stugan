package ircserver

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/klippelism/stugan/internal/core"
)

func (s *Session) handleCommand(msg *Message) {
	switch msg.Command {
	case "PING":
		token := serverName
		if len(msg.Params) > 0 && msg.Params[0] != "" {
			token = msg.Params[0]
		}
		_ = s.write(FormatLine("", serverName, "PONG", serverName, token))
		return

	case "PONG":
		return

	case "QUIT":
		s.destroy("Client disconnected")
		return

	case "CAP":
		s.handleCap(msg)
		return

	case "USER":
		s.numeric("462", ":You may not reregister")
		return

	case "AUTHENTICATE":
		_ = s.write(FormatLine("", serverName, "904", s.currentNickOr("*"), ":Already authenticated"))
		return

	case "BOUNCER":
		s.handleBouncer(msg)
		return

	case "CHATHISTORY":
		s.handleChatHistory(msg)
		return

	case "MARKREAD":
		s.handleMarkRead(msg)
		return
	}

	if s.isControl {
		s.notice("Cannot interact with channels and users on a bouncer control connection — bind a network.")
		return
	}

	if s.engine == nil {
		s.notice("Engine is unavailable.")
		return
	}

	switch msg.Command {
	case "PRIVMSG", "NOTICE":
		s.handleClientMessage(msg)

	case "JOIN":
		if len(msg.Params) == 0 {
			s.numeric("461", "JOIN :Not enough parameters")
			return
		}
		if msg.Params[0] == "0" {
			_ = s.engine.SendInput(s.networkID, "*status", "/quote JOIN 0")
			return
		}
		chans := strings.Split(msg.Params[0], ",")
		var keys []string
		if len(msg.Params) > 1 {
			keys = strings.Split(msg.Params[1], ",")
		}
		for i, ch := range chans {
			ch = strings.TrimSpace(ch)
			if ch == "" {
				continue
			}
			key := ""
			if i < len(keys) {
				key = strings.TrimSpace(keys[i])
			}
			cmd := "/join " + ch
			if key != "" {
				cmd += " " + key
			}
			_ = s.engine.SendInput(s.networkID, "*status", cmd)
		}

	case "PART":
		if len(msg.Params) == 0 {
			s.numeric("461", "PART :Not enough parameters")
			return
		}
		chans := strings.Split(msg.Params[0], ",")
		reason := ""
		if len(msg.Params) > 1 {
			reason = msg.Params[1]
		}
		for _, ch := range chans {
			ch = strings.TrimSpace(ch)
			if ch == "" {
				continue
			}
			cmd := "/part " + ch
			if reason != "" {
				cmd += " " + reason
			}
			_ = s.engine.SendInput(s.networkID, "*status", cmd)
		}

	case "TOPIC":
		if len(msg.Params) == 0 {
			s.numeric("461", "TOPIC :Not enough parameters")
			return
		}
		target := msg.Params[0]
		if len(msg.Params) > 1 {
			// Setting topic
			_ = s.engine.SendInput(s.networkID, target, "/topic "+msg.Params[1])
		} else {
			// Querying topic
			if netSnapshot := s.engine.SnapshotNetwork(s.networkID); netSnapshot != nil {
				for _, ch := range netSnapshot.Channels {
					if core.EqualIRC(ch.Name, target) {
						nick := s.currentNickOr("*")
						if ch.Topic != "" {
							_ = s.write(FormatLine("", serverName, "332", nick, ch.Name, ch.Topic))
							if ch.TopicSetter != "" {
								setterTime := strconv.FormatInt(ch.TopicTime.Unix(), 10)
								_ = s.write(FormatLine("", serverName, "333", nick, ch.Name, ch.TopicSetter, setterTime))
							}
						} else {
							_ = s.write(FormatLine("", serverName, "331", nick, ch.Name, ":No topic is set"))
						}
						break
					}
				}
			}
		}

	case "AWAY":
		if len(msg.Params) == 0 || strings.TrimSpace(msg.Params[0]) == "" {
			_ = s.engine.SendInput(s.networkID, "*status", "/away")
		} else {
			_ = s.engine.SendInput(s.networkID, "*status", "/away "+msg.Params[0])
		}

	case "NICK":
		if len(msg.Params) == 0 || msg.Params[0] == "" {
			s.numeric("431", ":No nickname given")
			return
		}
		_ = s.engine.SendInput(s.networkID, "*status", "/nick "+msg.Params[0])

	case "LIST":
		query := ""
		if len(msg.Params) > 0 {
			query = msg.Params[0]
		}
		_ = s.engine.ListChannels(s.networkID, query)

	case "WHOIS", "WHO", "MODE", "KICK", "INVITE":
		// Format command as quote passthrough
		var sb strings.Builder
		sb.WriteString("/quote ")
		sb.WriteString(msg.Command)
		for _, p := range msg.Params {
			sb.WriteString(" ")
			if strings.Contains(p, " ") || strings.HasPrefix(p, ":") {
				sb.WriteString(":")
			}
			sb.WriteString(p)
		}
		target := "*status"
		if len(msg.Params) > 0 && (strings.HasPrefix(msg.Params[0], "#") || strings.HasPrefix(msg.Params[0], "&")) {
			target = msg.Params[0]
		}
		_ = s.engine.SendInput(s.networkID, target, sb.String())

	default:
		// Unknown or generic raw command
		var sb strings.Builder
		sb.WriteString("/quote ")
		sb.WriteString(msg.Command)
		for _, p := range msg.Params {
			sb.WriteString(" ")
			if strings.Contains(p, " ") || strings.HasPrefix(p, ":") {
				sb.WriteString(":")
			}
			sb.WriteString(p)
		}
		_ = s.engine.SendInput(s.networkID, "*status", sb.String())
	}
}

func (s *Session) handleClientMessage(msg *Message) {
	if len(msg.Params) < 1 || msg.Params[0] == "" {
		s.numeric("411", fmt.Sprintf(":No recipient given (%s)", msg.Command))
		return
	}
	if len(msg.Params) < 2 || msg.Params[1] == "" {
		s.numeric("412", ":No text to send")
		return
	}

	targets := strings.Split(msg.Params[0], ",")
	text := msg.Params[1]

	for _, target := range targets {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}

		isAction := strings.HasPrefix(text, "\x01ACTION ") && strings.HasSuffix(text, "\x01")
		if isAction {
			body := strings.TrimPrefix(text, "\x01ACTION ")
			body = strings.TrimSuffix(body, "\x01")
			s.registerEcho(core.MsgAction, target, body)
			_ = s.engine.SendInput(s.networkID, target, "/me "+body)
		} else if msg.Command == "NOTICE" {
			s.registerEcho(core.MsgNotice, target, text)
			_ = s.engine.SendInput(s.networkID, target, "/notice "+target+" "+text)
		} else {
			s.registerEcho(core.MsgPrivmsg, target, text)
			_ = s.engine.SendInput(s.networkID, target, text)
		}
	}
}

func (s *Session) handleBouncer(msg *Message) {
	if !s.caps[CapBouncerNetworks] {
		_ = s.write(FormatLine("", serverName, "FAIL", "BOUNCER", "UNKNOWN_COMMAND", ":Negotiate soju.im/bouncer-networks capability first"))
		return
	}

	sub := ""
	if len(msg.Params) > 0 {
		sub = strings.ToUpper(msg.Params[0])
	}

	switch sub {
	case "LISTNETWORKS":
		s.sendNetworkList()

	case "BIND":
		_ = s.write(FormatLine("", serverName, "FAIL", "BOUNCER", "REGISTRATION_IS_COMPLETED", "BIND", ":Cannot bind to a network after registration"))

	case "ADDNETWORK", "CHANGENETWORK", "DELNETWORK":
		_ = s.write(FormatLine("", serverName, "FAIL", "BOUNCER", "UNKNOWN_COMMAND", sub, ":Manage networks in the stugan web UI"))

	default:
		_ = s.write(FormatLine("", serverName, "FAIL", "BOUNCER", "UNKNOWN_COMMAND", sub, ":Unknown subcommand"))
	}
}

func (s *Session) handleChatHistory(msg *Message) {
	if s.isControl || s.networkID == "" || s.history == nil {
		_ = s.write(FormatLine("", serverName, "FAIL", "CHATHISTORY", "INVALID_TARGET", "*", ":Cannot fetch chat history on this connection"))
		return
	}

	if len(msg.Params) < 1 {
		s.numeric("461", "CHATHISTORY :Not enough parameters")
		return
	}

	sub := strings.ToUpper(msg.Params[0])
	if sub == "TARGETS" {
		s.handleChatHistoryTargets(msg)
		return
	}

	if sub != "LATEST" && sub != "BEFORE" && sub != "AFTER" && sub != "AROUND" && sub != "BETWEEN" {
		_ = s.write(FormatLine("", serverName, "FAIL", "CHATHISTORY", "INVALID_PARAMS", sub, ":Unknown subcommand"))
		return
	}

	if len(msg.Params) < 2 || msg.Params[1] == "" {
		s.numeric("461", "CHATHISTORY :Not enough parameters")
		return
	}

	target := msg.Params[1]
	isBetween := sub == "BETWEEN"

	limitIdx := 3
	if isBetween {
		limitIdx = 4
	}

	limit := defaultMaxPlayback
	if len(msg.Params) > limitIdx && msg.Params[limitIdx] != "" {
		n, err := strconv.Atoi(msg.Params[limitIdx])
		if err != nil || n < 0 || n > maxChatHistory {
			_ = s.write(FormatLine("", serverName, "FAIL", "CHATHISTORY", "INVALID_PARAMS", sub, msg.Params[limitIdx], ":Invalid limit"))
			return
		}
		limit = n
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	msgs, _, err := s.history.Backlog(ctx, s.networkID, target, 0, limit)
	if err != nil {
		_ = s.write(FormatLine("", serverName, "FAIL", "CHATHISTORY", "INTERNAL_ERROR", sub, ":Failed to query history"))
		return
	}

	isChannel := strings.HasPrefix(target, "#") || strings.HasPrefix(target, "&")

	s.withBatch("chathistory", []string{target}, func(ref string) {
		for _, m := range msgs {
			lines := s.formatPlaybackMessage(m, isChannel, ref)
			for _, line := range lines {
				_ = s.write(line)
			}
		}
	})
}

func (s *Session) handleChatHistoryTargets(msg *Message) {
	// Simple targets dump in batch
	s.withBatch("draft/chathistory-targets", nil, func(ref string) {
		if netSnapshot := s.engine.SnapshotNetwork(s.networkID); netSnapshot != nil {
			for _, ch := range netSnapshot.Channels {
				tag := ""
				if ref != "" {
					tag = fmt.Sprintf("batch=%s", ref)
				}
				_ = s.write(FormatLine(tag, serverName, "CHATHISTORY", "TARGETS", ch.Name, FormatIrcTime(time.Now())))
			}
		}
	})
}

func (s *Session) handleMarkRead(msg *Message) {
	if s.isControl || s.networkID == "" || s.engine == nil {
		return
	}
	if len(msg.Params) < 1 || msg.Params[0] == "" {
		s.numeric("461", "MARKREAD :Not enough parameters")
		return
	}

	target := msg.Params[0]
	ts := time.Now()

	if len(msg.Params) > 1 {
		param := msg.Params[1]
		if strings.HasPrefix(strings.ToLower(param), "timestamp=") {
			rawTs := param[len("timestamp="):]
			if parsed, err := time.Parse(time.RFC3339Nano, rawTs); err == nil {
				ts = parsed
			} else if parsed, err := time.Parse("2006-01-02T15:04:05.000Z", rawTs); err == nil {
				ts = parsed
			} else if parsed, err := time.Parse(time.RFC3339, rawTs); err == nil {
				ts = parsed
			}
		} else if param != "*" {
			if parsed, err := time.Parse(time.RFC3339Nano, param); err == nil {
				ts = parsed
			} else if parsed, err := time.Parse("2006-01-02T15:04:05.000Z", param); err == nil {
				ts = parsed
			} else if parsed, err := time.Parse(time.RFC3339, param); err == nil {
				ts = parsed
			}
		}
	}

	s.engine.MarkRead(s.networkID, target, ts)
}
