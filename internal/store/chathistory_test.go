package store

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/klippelism/stugan/internal/core"
)

func TestChatHistoryRanges(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 9; i++ {
		m := msg("n", "#c", "bob", fmt.Sprint(i), core.MsgPrivmsg, base.Add(time.Duration(i)*time.Second))
		m.ID = fmt.Sprint(i)
		s.Print(m)
	}
	s.Print(msg("n", "#c", "bob", "event", core.MsgJoin, base.Add(5*time.Second)))
	s.Print(msg("n", "#c", "bob", "", core.MsgPrivmsg, base.Add(6*time.Second)))
	stamp := func(i int) string {
		return "timestamp=" + base.Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano)
	}
	tests := []struct {
		cmd, a, b string
		want      []string
	}{
		{"LATEST", "*", "", []string{"7", "8", "9"}},
		{"LATEST", "msgid=3", "", []string{"7", "8", "9"}},
		{"LATEST", stamp(7), "", []string{"8", "9"}},
		{"BEFORE", "msgid=5", "", []string{"2", "3", "4"}},
		{"BEFORE", stamp(5), "", []string{"2", "3", "4"}},
		{"AFTER", "msgid=3", "", []string{"4", "5", "6"}},
		{"AFTER", stamp(3), "", []string{"4", "5", "6"}},
		{"AROUND", "msgid=5", "", []string{"4", "5", "6"}},
		{"AROUND", stamp(5), "", []string{"4", "5", "6"}},
		{"AROUND", "msgid=1", "", []string{"1", "2", "3"}},
		{"AROUND", "msgid=9", "", []string{"7", "8", "9"}},
		{"BETWEEN", "msgid=2", "msgid=8", []string{"3", "4", "5"}},
		{"BETWEEN", stamp(8), stamp(2), []string{"5", "6", "7"}},
		{"BETWEEN", "*", "msgid=5", []string{"1", "2", "3"}},
		{"BETWEEN", "msgid=5", "*", []string{"6", "7", "8"}},
		{"BETWEEN", "*", "*", []string{"1", "2", "3"}},
		{"AFTER", "msgid=9", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.cmd+tt.a+tt.b, func(t *testing.T) {
			got, err := s.ChatHistory(ctx, "n", "#C", tt.cmd, tt.a, tt.b, 3, true)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, m := range got {
				ids = append(ids, m.ID)
			}
			if !reflect.DeepEqual(ids, tt.want) {
				t.Fatalf("got %v want %v", ids, tt.want)
			}
		})
	}
	if _, err := s.ChatHistory(ctx, "n", "#c", "AFTER", "msgid=missing", "", 3, true); err == nil {
		t.Fatal("missing msgid accepted")
	}
}

func TestChatHistoryTargets(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, b := range []string{"#c", "bob", "#outside", "*status"} {
		s.Print(msg("n", b, "bob", "hello", core.MsgPrivmsg, base.Add(time.Duration(i+1)*time.Second)))
	}
	s.Print(msg("other", "#foreign", "bob", "hello", core.MsgPrivmsg, base))
	a := "timestamp=" + base.Add(time.Second).Format(time.RFC3339)
	b := "timestamp=" + base.Add(2*time.Second).Format(time.RFC3339)
	for _, refs := range [][2]string{{a, b}, {b, a}} {
		got, err := s.ChatHistoryTargets(ctx, "n", refs[0], refs[1], 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || !got["bob"].Equal(base.Add(2*time.Second)) {
			t.Fatalf("targets %v", got)
		}
	}
	got, err := s.ChatHistoryTargets(ctx, "n", "*", "*", 1)
	if err != nil || len(got) != 1 || !got["#outside"].Equal(base.Add(3*time.Second)) {
		t.Fatalf("limited targets %v %v", got, err)
	}
	// A conversation with an older line inside the window is excluded when its latest is outside.
	s.Print(msg("n", "#c", "bob", "new", core.MsgPrivmsg, base.Add(10*time.Second)))
	got, err = s.ChatHistoryTargets(ctx, "n", a, b, 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("latest window %v %v", got, err)
	}
}

func TestChatHistorySharedTimestampAndSelfFiltering(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	when := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 5; i++ {
		m := msg("n", "bob", "bob", fmt.Sprint(i), core.MsgPrivmsg, when)
		m.ID = fmt.Sprint(i)
		m.Self = i == 3
		s.Print(m)
	}
	got, err := s.ChatHistory(ctx, "n", "bob", "AFTER", "msgid=2", "", 2, false)
	if err != nil || len(got) != 2 || got[0].ID != "4" || got[1].ID != "5" {
		t.Fatalf("self filtering %v %v", got, err)
	}
	got, err = s.ChatHistory(ctx, "n", "bob", "BETWEEN", "msgid=5", "msgid=1", 2, true)
	if err != nil || len(got) != 2 || got[0].ID != "3" || got[1].ID != "4" {
		t.Fatalf("shared timestamp %v %v", got, err)
	}
	got, err = s.ChatHistory(ctx, "n", "bob", "AFTER", "timestamp="+when.Format(time.RFC3339Nano), "", 5, true)
	if err != nil || len(got) != 0 {
		t.Fatalf("strict timestamp %v %v", got, err)
	}
}
