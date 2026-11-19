package ircserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
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
	if !s.hasCap(CapBouncerNetworks) {
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

// chatHistoryStore is optional so basic History providers remain compatible.
type chatHistoryStore interface {
	ChatHistory(context.Context, string, string, string, string, string, int, bool) ([]core.Message, error)
	ChatHistoryTargets(context.Context, string, string, string, int) (map[string]time.Time, error)
}

func validHistoryRef(ref string, wildcard, msgid bool) bool {
	if ref == "*" {
		return wildcard
	}
	if strings.HasPrefix(ref, "msgid=") {
		return msgid && len(ref) > 6 && !strings.ContainsAny(ref[6:], " \r\n\t")
	}
	if !strings.HasPrefix(ref, "timestamp=") {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, ref[10:])
	return err == nil
}

func (s *Session) handleChatHistory(msg *Message) {
	sub := "*"
	if len(msg.Params) > 0 {
		sub = strings.ToUpper(msg.Params[0])
	}
	fail := func(code, reason string) {
		_ = s.write(FormatLine("", serverName, "FAIL", "CHATHISTORY", code, sub, reason))
	}
	if s.isControl || s.networkID == "" || s.history == nil {
		fail("INVALID_TARGET", "Cannot fetch history on this connection")
		return
	}
	targets := sub == "TARGETS"
	count := 4
	if sub == "BETWEEN" {
		count = 5
	}
	if sub != "LATEST" && sub != "BEFORE" && sub != "AFTER" && sub != "AROUND" && sub != "BETWEEN" && !targets {
		fail("INVALID_PARAMS", "Unknown subcommand")
		return
	}
	if len(msg.Params) != count {
		fail("INVALID_PARAMS", "Incorrect parameter count")
		return
	}
	limit, err := strconv.Atoi(msg.Params[count-1])
	if err != nil || limit <= 0 || limit > maxChatHistory {
		fail("INVALID_PARAMS", "Invalid limit")
		return
	}
	first := 2
	if targets {
		first = 1
	}
	for _, r := range msg.Params[first : count-1] {
		if !validHistoryRef(r, targets || sub == "LATEST" || sub == "BETWEEN", !targets) {
			fail("INVALID_PARAMS", "Invalid message reference")
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	provider, ok := s.history.(chatHistoryStore)
	if targets {
		entries := map[string]time.Time{}
		if ok {
			entries, err = provider.ChatHistoryTargets(ctx, s.networkID, msg.Params[1], msg.Params[2], limit)
		}
		if err != nil {
			fail("MESSAGE_ERROR", "Failed to query history")
			return
		}
		names := make([]string, 0, len(entries))
		for name := range entries {
			names = append(names, name)
		}
		sort.Slice(names, func(i, j int) bool {
			if entries[names[i]].Equal(entries[names[j]]) {
				return names[i] < names[j]
			}
			return entries[names[i]].After(entries[names[j]])
		})
		s.withBatch("draft/chathistory-targets", nil, func(ref string) {
			for _, name := range names {
				tag := ""
				if ref != "" {
					tag = "batch=" + ref
				}
				_ = s.write(FormatLine(tag, serverName, "CHATHISTORY", "TARGETS", name, FormatIrcTime(entries[name])))
			}
		})
		return
	}
	target := msg.Params[1]
	if target == "" || strings.HasPrefix(target, "*") || strings.ContainsAny(target, " ,\r\n\t") {
		fail("INVALID_TARGET", "Invalid target")
		return
	}
	isChannel := strings.HasPrefix(target, "#") || strings.HasPrefix(target, "&")
	var msgs []core.Message
	if ok {
		ref2 := ""
		if sub == "BETWEEN" {
			ref2 = msg.Params[3]
		}
		includeSelf := isChannel || (s.wantsSelfMessages() && !IsServicesNick(target))
		msgs, err = provider.ChatHistory(ctx, s.networkID, target, sub, msg.Params[2], ref2, limit, includeSelf)
	} else {
		msgs, _, err = s.history.Backlog(ctx, s.networkID, target, 0, limit)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fail("INVALID_PARAMS", "Unknown message reference")
		} else {
			fail("MESSAGE_ERROR", "Failed to query history")
		}
		return
	}
	s.withBatch("chathistory", []string{target}, func(ref string) {
		for _, m := range msgs {
			for _, line := range s.formatPlaybackMessage(m, isChannel, ref) {
				_ = s.write(line)
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
