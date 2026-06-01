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

func TestPluginKVRoundTrip(t *testing.T) {
	s := openTest(t)

	// Empty script returns an empty (not nil) map so callers can range freely.
	if got := s.PluginKVGetAll("fish"); len(got) != 0 {
		t.Fatalf("PluginKVGetAll on empty script = %v, want empty", got)
	}

	if err := s.PluginKVSet("fish", "libera\t#go", "cbc\thunter2"); err != nil {
		t.Fatalf("PluginKVSet: %v", err)
	}
	if err := s.PluginKVSet("fish", "libera\t#old", "ecb\tweak"); err != nil {
		t.Fatalf("PluginKVSet: %v", err)
	}
	// A second script's entries must not leak.
	if err := s.PluginKVSet("greet", "last", "now"); err != nil {
		t.Fatalf("PluginKVSet: %v", err)
	}

	got := s.PluginKVGetAll("fish")
	if got["libera\t#go"] != "cbc\thunter2" || got["libera\t#old"] != "ecb\tweak" || len(got) != 2 {
		t.Fatalf("PluginKVGetAll(fish) = %v", got)
	}
	if got := s.PluginKVGetAll("greet"); got["last"] != "now" || len(got) != 1 {
		t.Fatalf("PluginKVGetAll(greet) = %v", got)
	}

	// Upsert overwrites.
	if err := s.PluginKVSet("fish", "libera\t#go", "cbc\tnew"); err != nil {
		t.Fatalf("PluginKVSet upsert: %v", err)
	}
	if got := s.PluginKVGetAll("fish")["libera\t#go"]; got != "cbc\tnew" {
		t.Errorf("upsert: got %q, want cbc\\tnew", got)
	}

	// Delete is idempotent.
	if err := s.PluginKVDelete("fish", "libera\t#old"); err != nil {
		t.Fatalf("PluginKVDelete: %v", err)
	}
	if err := s.PluginKVDelete("fish", "libera\t#old"); err != nil {
		t.Errorf("PluginKVDelete (repeat): %v", err)
	}
	if got := s.PluginKVGetAll("fish"); len(got) != 1 {
		t.Errorf("after delete: got %v, want 1 entry", got)
	}
}

func TestPrefRoundTrip(t *testing.T) {
	s := openTest(t)

	// An unset key reads as empty without error.
	if v, err := s.Pref("highlight"); err != nil || v != "" {
		t.Fatalf("Pref(unset) = %q, %v; want \"\", nil", v, err)
	}

	if err := s.SetPref("highlight", `{"patterns":["x"]}`); err != nil {
		t.Fatalf("SetPref: %v", err)
	}
	if v, _ := s.Pref("highlight"); v != `{"patterns":["x"]}` {
		t.Fatalf("Pref after set = %q", v)
	}

	// Upsert overwrites; other keys are independent.
	if err := s.SetPref("highlight", `{"patterns":["y"]}`); err != nil {
		t.Fatalf("SetPref upsert: %v", err)
	}
	if err := s.SetPref("muted", `[{"network":"libera","buffer":"#go"}]`); err != nil {
		t.Fatalf("SetPref muted: %v", err)
	}
	if v, _ := s.Pref("highlight"); v != `{"patterns":["y"]}` {
		t.Errorf("Pref(highlight) after upsert = %q", v)
	}
	if v, _ := s.Pref("muted"); v != `[{"network":"libera","buffer":"#go"}]` {
		t.Errorf("Pref(muted) = %q", v)
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
	got, more, err := s.Backlog(ctx, "libera", "#go", time.Time{}, 3)
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
	// First (newest) page: messages 3,4 oldest-first.
	page1, more, err := s.Backlog(ctx, "n", "#c", time.Time{}, 2)
	if err != nil || !more || len(page1) != 2 {
		t.Fatalf("page1: %v more=%v len=%d", err, more, len(page1))
	}
	if page1[0].Text != text(3) || page1[1].Text != text(4) {
		t.Fatalf("page1 = %q,%q", page1[0].Text, page1[1].Text)
	}

	// Page backward using the oldest loaded message's time as the cursor.
	page2, more, err := s.Backlog(ctx, "n", "#c", page1[0].Time, 2)
	if err != nil || !more || len(page2) != 2 {
		t.Fatalf("page2: %v more=%v len=%d", err, more, len(page2))
	}
	if page2[0].Text != text(1) || page2[1].Text != text(2) {
		t.Fatalf("page2 = %q,%q", page2[0].Text, page2[1].Text)
	}

	// Final page: just message 0, no more.
	page3, more, err := s.Backlog(ctx, "n", "#c", page2[0].Time, 2)
	if err != nil || more || len(page3) != 1 {
		t.Fatalf("page3: %v more=%v len=%d", err, more, len(page3))
	}
	if page3[0].Text != text(0) {
		t.Fatalf("page3 = %q", page3[0].Text)
	}
}

func TestBacklogAround(t *testing.T) {
	s := openTest(t)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	// 10 messages a,b,c…j a minute apart.
	for i := range 10 {
		s.Print(msg("n", "#c", "u", text(i), core.MsgPrivmsg, base.Add(time.Duration(i)*time.Minute)))
	}
	ctx := context.Background()

	// Window of 6 centered on message index 5 (text "f"): expects
	// roughly 3 ≤ around (indices 3,4,5) and 3 strictly newer (6,7,8).
	got, more, err := s.BacklogAround(ctx, "n", "#c", base.Add(5*time.Minute), 6)
	if err != nil {
		t.Fatalf("around: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("got %d, want 6", len(got))
	}
	if got[0].Text != text(3) || got[5].Text != text(8) {
		t.Fatalf("window = %q..%q, want %q..%q", got[0].Text, got[5].Text, text(3), text(8))
	}
	if !more {
		t.Errorf("more = false, want true (indices 0..2 still older than window)")
	}

	// Anchor at the very first message: nothing older, after-half fills out.
	got, more, err = s.BacklogAround(ctx, "n", "#c", base, 6)
	if err != nil {
		t.Fatalf("around-first: %v", err)
	}
	if len(got) != 4 {
		// before-half=3 wants ts ≤ base → only the anchor, after-half=3 → 1,2,3.
		t.Fatalf("got %d, want 4 (1 anchor + 3 newer)", len(got))
	}
	if got[0].Text != text(0) || got[3].Text != text(3) {
		t.Fatalf("first-window = %q..%q", got[0].Text, got[3].Text)
	}
	if more {
		t.Errorf("more = true at oldest anchor, want false")
	}

	// Anchor at the very last message: nothing newer, before-half fills out.
	got, more, err = s.BacklogAround(ctx, "n", "#c", base.Add(9*time.Minute), 6)
	if err != nil {
		t.Fatalf("around-last: %v", err)
	}
	if len(got) != 3 || got[2].Text != text(9) {
		t.Fatalf("last-window = %+v", got)
	}
	if !more {
		t.Errorf("more = false at newest anchor, want true (older history exists)")
	}

	// Zero around → falls back to most-recent page semantics.
	got, _, err = s.BacklogAround(ctx, "n", "#c", time.Time{}, 4)
	if err != nil {
		t.Fatalf("around-zero: %v", err)
	}
	if len(got) != 4 || got[3].Text != text(9) {
		t.Fatalf("zero-around = %+v", got)
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
	if len(res) != 1 || res[0].Buffer != "#d" {
		t.Fatalf("scoped search = %+v", res)
	}
}

func TestNetworkPersistence(t *testing.T) {
	s := openTest(t)
	if err := s.SaveNetwork(core.NetworkParams{ID: "libera", Name: "libera", Addr: "irc.libera.chat:6697", TLS: true, Nick: "me", Channels: []string{"#go"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveNetwork(core.NetworkParams{ID: "oftc", Name: "oftc", Addr: "irc.oftc.net:6697", TLS: true, Nick: "me"}); err != nil {
		t.Fatal(err)
	}
	nets, err := s.Networks()
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 2 {
		t.Fatalf("got %d networks, want 2", len(nets))
	}
	// Upsert (same id) replaces, not duplicates.
	if err := s.SaveNetwork(core.NetworkParams{ID: "libera", Name: "libera", Addr: "newaddr:6697", Nick: "me2"}); err != nil {
		t.Fatal(err)
	}
	nets, _ = s.Networks()
	if len(nets) != 2 {
		t.Fatalf("upsert duplicated; got %d", len(nets))
	}

	if err := s.DeleteNetwork("oftc"); err != nil {
		t.Fatal(err)
	}
	nets, _ = s.Networks()
	if len(nets) != 1 || nets[0].ID != "libera" || nets[0].Addr != "newaddr:6697" {
		t.Fatalf("after delete = %+v", nets)
	}
	if len(nets[0].Channels) != 0 {
		t.Errorf("upserted network kept stale channels: %+v", nets[0].Channels)
	}
}

func text(i int) string { return string(rune('a'+i)) + "-line" }

func TestReadMarkersUnreadCounts(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	at := func(secs int) time.Time { return base.Add(time.Duration(secs) * time.Second) }

	// A buffer with no read marker contributes nothing, even with history —
	// there's no baseline, so old history is never retroactively "unread".
	s.Print(msg("libera", "#go", "alice", "hi", core.MsgPrivmsg, at(0)))
	got, err := s.UnreadCounts(ctx)
	if err != nil {
		t.Fatalf("UnreadCounts: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("no marker should yield no counts, got %+v", got)
	}

	// Read up to t=10. Messages at/before it are read; later ones are unread.
	if err := s.MarkRead(ctx, "libera", "#go", at(10)); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	s.Print(msg("libera", "#go", "bob", "before", core.MsgPrivmsg, at(5))) // read
	s.Print(msg("libera", "#go", "carol", "new1", core.MsgPrivmsg, at(20)))
	s.Print(msg("libera", "#go", "dave", "new2", core.MsgNotice, at(30)))
	// A highlight (counts toward both unread and highlight).
	hl := msg("libera", "#go", "erin", "ping you", core.MsgPrivmsg, at(40))
	hl.Highlight = true
	s.Print(hl)
	// Excluded: our own echo, and a non-conversational join.
	self := msg("libera", "#go", "me", "echo", core.MsgPrivmsg, at(50))
	self.Self = true
	s.Print(self)
	s.Print(msg("libera", "#go", "frank", "joined", core.MsgJoin, at(60)))

	got, err = s.UnreadCounts(ctx)
	if err != nil {
		t.Fatalf("UnreadCounts: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 buffer, got %+v", got)
	}
	u := got[0]
	if u.Network != "libera" || u.Buffer != "#go" {
		t.Errorf("wrong buffer: %+v", u)
	}
	if u.Unread != 3 { // new1, new2, ping (before/self/join excluded)
		t.Errorf("Unread = %d, want 3", u.Unread)
	}
	if u.Highlight != 1 {
		t.Errorf("Highlight = %d, want 1", u.Highlight)
	}

	// Marker is monotonic: a stale, earlier MarkRead must not move it back.
	if err := s.MarkRead(ctx, "libera", "#go", at(5)); err != nil {
		t.Fatalf("MarkRead stale: %v", err)
	}
	got, _ = s.UnreadCounts(ctx)
	if len(got) != 1 || got[0].Unread != 3 {
		t.Errorf("stale MarkRead moved marker back: %+v", got)
	}

	// Reading up to the end clears the buffer entirely.
	if err := s.MarkRead(ctx, "libera", "#go", at(100)); err != nil {
		t.Fatalf("MarkRead end: %v", err)
	}
	got, _ = s.UnreadCounts(ctx)
	if len(got) != 0 {
		t.Errorf("after reading to end, want no counts, got %+v", got)
	}
}
