package store

import (
	"context"
	"testing"
	"time"

	"github.com/klippelism/stugan/internal/core"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:", nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func msg(network, buffer, from, text string, kind core.MsgKind, when time.Time) core.Message {
	return core.Message{
		Network: network, Buffer: buffer, From: from, Text: text,
		Kind: kind, Time: when,
	}
}

func TestPersistAndBacklog(t *testing.T) {
	s := openTest(t)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := range 5 {
		s.Print(msg("libera", "#go", "alice", text(i), core.MsgPrivmsg, base.Add(time.Duration(i)*time.Minute)))
	}
	// A line in another buffer must not bleed in.
	s.Print(msg("libera", "#other", "bob", "elsewhere", core.MsgPrivmsg, base))

	ctx := context.Background()
	got, more, err := s.Backlog(ctx, "libera", "#go", 0, 3)
	if err != nil {
		t.Fatalf("backlog: %v", err)
	}
	if more != true {
		t.Errorf("more = false, want true (5 messages, page of 3)")
	}
	if len(got) != 3 {
		t.Fatalf("got %d messages, want 3", len(got))
	}
	// Newest page, oldest-first: messages 2,3,4.
	if got[0].Text != text(2) || got[2].Text != text(4) {
		t.Errorf("page = %q..%q, want %q..%q", got[0].Text, got[2].Text, text(2), text(4))
	}
	// Round-trip fields.
	if got[0].From != "alice" || got[0].Network != "libera" || got[0].Buffer != "#go" {
		t.Errorf("round-trip fields wrong: %+v", got[0])
	}
	if !got[0].Time.Equal(base.Add(2 * time.Minute)) {
		t.Errorf("time round-trip = %v", got[0].Time)
	}
}

func TestBacklogPaging(t *testing.T) {
	s := openTest(t)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := range 5 {
		s.Print(msg("n", "#c", "u", text(i), core.MsgPrivmsg, base.Add(time.Duration(i)*time.Minute)))
	}
	ctx := context.Background()
	// First (newest) page.
	page1, more, err := s.Backlog(ctx, "n", "#c", 0, 2)
	if err != nil || !more || len(page1) != 2 {
		t.Fatalf("page1: %v more=%v len=%d", err, more, len(page1))
	}
	if page1[0].Text != text(3) || page1[1].Text != text(4) {
		t.Fatalf("page1 = %q,%q", page1[0].Text, page1[1].Text)
	}
}

func TestSearch(t *testing.T) {
	s := openTest(t)
	now := time.Now()
	s.Print(msg("n", "#c", "alice", "the quick brown fox", core.MsgPrivmsg, now))
	s.Print(msg("n", "#c", "bob", "lazy dog sleeps", core.MsgPrivmsg, now))
	s.Print(msg("n", "#d", "carol", "another quick note", core.MsgPrivmsg, now))

	ctx := context.Background()
	res, err := s.Search(ctx, "quick", "", "", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("search 'quick' = %d results, want 2", len(res))
	}

	// Scoped to a buffer.
	res, err = s.Search(ctx, "quick", "n", "#d", 10)
	if err != nil {
		t.Fatalf("scoped search: %v", err)
	}
	if len(res) != 1 || res[0].Message.Buffer != "#d" {
		t.Fatalf("scoped search = %+v", res)
	}
}

func text(i int) string { return string(rune('a'+i)) + "-line" }
