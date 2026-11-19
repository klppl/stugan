package ircserver

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klippelism/stugan/internal/core"
	"github.com/klippelism/stugan/internal/store"
)

func TestChatHistoryProtocol(t *testing.T) {
	hist, err := store.Open(filepath.Join(t.TempDir(), "history.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer hist.Close()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 6; i++ {
		hist.Print(core.Message{Network: "n", Buffer: "#c", From: "bob", Text: fmt.Sprint(i), ID: fmt.Sprint(i), Kind: core.MsgPrivmsg, Time: base.Add(time.Duration(i) * time.Second)})
	}
	hist.Print(core.Message{Network: "n", Buffer: "bob", From: "bob", Text: "dm", Kind: core.MsgPrivmsg, Time: base.Add(4 * time.Second)})
	s := &Session{history: hist, networkID: "n", clientNick: "alice", out: make(chan string, 100), done: make(chan struct{}), caps: map[string]bool{CapBatch: true, CapServerTime: true, CapMessageTags: true}}
	stamp := "timestamp=" + base.Add(3*time.Second).Format(time.RFC3339Nano)
	tests := []struct {
		line  string
		texts []string
		fail  bool
	}{
		{"LATEST #c * 2", []string{"5", "6"}, false},
		{"LATEST #c msgid=5 2", []string{"6"}, false},
		{"LATEST #c " + stamp + " 2", []string{"5", "6"}, false},
		{"BEFORE #c msgid=4 2", []string{"2", "3"}, false},
		{"AFTER #c " + stamp + " 2", []string{"4", "5"}, false},
		{"AROUND #c msgid=3 3", []string{"2", "3", "4"}, false},
		{"BETWEEN #c msgid=1 msgid=6 2", []string{"2", "3"}, false},
		{"BETWEEN #c msgid=6 msgid=1 2", []string{"4", "5"}, false},
		{"BETWEEN #c * msgid=4 2", []string{"1", "2"}, false},
		{"BETWEEN #c msgid=4 * 2", []string{"5", "6"}, false},
		{"AFTER #c msgid=6 2", nil, false},
		{"LATEST #c *", nil, true}, {"BEFORE #c * 2", nil, true}, {"AFTER #c timestamp=bad 2", nil, true}, {"LATEST #c * 0", nil, true}, {"LATEST #c * -1", nil, true}, {"LATEST #c * wat", nil, true}, {"AFTER #c msgid=missing 2", nil, true}, {"TARGETS msgid=1 * 2", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			s.handleChatHistory(&Message{Params: strings.Fields(tt.line)})
			var lines []string
			for len(s.out) > 0 {
				lines = append(lines, <-s.out)
			}
			all := strings.Join(lines, "\n")
			if tt.fail {
				if !strings.Contains(all, "FAIL CHATHISTORY INVALID_PARAMS") {
					t.Fatal(all)
				}
				return
			}
			if len(lines) != len(tt.texts)+2 || !strings.Contains(lines[0], " chathistory #c") || !strings.Contains(lines[len(lines)-1], " BATCH -") {
				t.Fatal(all)
			}
			for i, text := range tt.texts {
				if !strings.HasSuffix(strings.TrimSpace(lines[i+1]), " "+text) && !strings.HasSuffix(strings.TrimSpace(lines[i+1]), " :"+text) {
					t.Fatal(all)
				}
			}
		})
	}
	s.handleChatHistory(&Message{Params: []string{"TARGETS", "*", "*", "2"}})
	var lines []string
	for len(s.out) > 0 {
		lines = append(lines, <-s.out)
	}
	all := strings.Join(lines, "\n")
	if len(lines) != 4 || !strings.Contains(lines[0], "draft/chathistory-targets") || !strings.Contains(lines[1], "TARGETS #c "+FormatIrcTime(base.Add(6*time.Second))) || !strings.Contains(lines[2], "TARGETS bob "+FormatIrcTime(base.Add(4*time.Second))) {
		t.Fatal(all)
	}
}
