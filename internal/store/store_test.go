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
	// First (newest) page: messages 3,4 oldest-first.
	page1, more, err := s.Backlog(ctx, "n", "#c", 0, 2)
	if err != nil || !more || len(page1) != 2 {
		t.Fatalf("page1: %v more=%v len=%d", err, more, len(page1))
	}
	if page1[0].Text != text(3) || page1[1].Text != text(4) {
		t.Fatalf("page1 = %q,%q", page1[0].Text, page1[1].Text)
	}

	// Page backward using the oldest loaded message's Seq as the cursor.
	page2, more, err := s.Backlog(ctx, "n", "#c", page1[0].Seq, 2)
	if err != nil || !more || len(page2) != 2 {
		t.Fatalf("page2: %v more=%v len=%d", err, more, len(page2))
	}
	if page2[0].Text != text(1) || page2[1].Text != text(2) {
		t.Fatalf("page2 = %q,%q", page2[0].Text, page2[1].Text)
	}

	// Final page: just message 0, no more.
	page3, more, err := s.Backlog(ctx, "n", "#c", page2[0].Seq, 2)
	if err != nil || more || len(page3) != 1 {
		t.Fatalf("page3: %v more=%v len=%d", err, more, len(page3))
	}
	if page3[0].Text != text(0) {
		t.Fatalf("page3 = %q", page3[0].Text)
	}
}

// TestBacklogPagingSameTimestamp guards the keyset cursor: when every message
// shares one millisecond timestamp (pastes, bot bursts), paging by Seq must
// still walk all of them without skipping. A ts-based cursor would drop every
// message sharing the boundary millisecond and stall.
func TestBacklogPagingSameTimestamp(t *testing.T) {
	s := openTest(t)
	ts := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	const total = 10
	for i := range total {
		s.Print(msg("n", "#c", "u", text(i), core.MsgPrivmsg, ts)) // all identical ts
	}
	ctx := context.Background()

	// Page backward in pages of 3, accumulating oldest-first results.
	var seen []string
	cursor := int64(0)
	for range total { // bounded loop; should converge well before this
		page, more, err := s.Backlog(ctx, "n", "#c", cursor, 3)
		if err != nil {
			t.Fatalf("backlog: %v", err)
		}
		if len(page) == 0 {
			t.Fatalf("empty page with cursor=%d; %d/%d seen", cursor, len(seen), total)
		}
		texts := make([]string, len(page))
		for i, m := range page {
			texts[i] = m.Text
		}
		seen = append(texts, seen...) // prepend; pages arrive newest-block first
		cursor = page[0].Seq
		if !more {
			break
		}
	}
	if len(seen) != total {
		t.Fatalf("retrieved %d messages, want %d (same-ts paging skipped some)", len(seen), total)
	}
	for i := range total {
		if seen[i] != text(i) {
			t.Fatalf("message %d = %q, want %q (order/skip bug)", i, seen[i], text(i))
		}
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
	got, more, moreNewer, err := s.BacklogAround(ctx, "n", "#c", base.Add(5*time.Minute), 6)
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
	if !moreNewer {
		t.Errorf("moreNewer = false, want true (index 9 is beyond the window)")
	}

	// Anchor at the very first message: nothing older, after-half fills out.
	got, more, moreNewer, err = s.BacklogAround(ctx, "n", "#c", base, 6)
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
	if !moreNewer {
		t.Errorf("moreNewer = false, want true (newer history remains)")
	}

	// Anchor at the very last message: nothing newer, before-half fills out.
	got, more, moreNewer, err = s.BacklogAround(ctx, "n", "#c", base.Add(9*time.Minute), 6)
	if err != nil {
		t.Fatalf("around-last: %v", err)
	}
	if len(got) != 3 || got[2].Text != text(9) {
		t.Fatalf("last-window = %+v", got)
	}
	if !more {
		t.Errorf("more = false at newest anchor, want true (older history exists)")
	}
	if moreNewer {
		t.Errorf("moreNewer = true at the live tail, want false")
	}

	// Zero around → falls back to most-recent page semantics.
	got, _, moreNewer, err = s.BacklogAround(ctx, "n", "#c", time.Time{}, 4)
	if err != nil {
		t.Fatalf("around-zero: %v", err)
	}
	if len(got) != 4 || got[3].Text != text(9) {
		t.Fatalf("zero-around = %+v", got)
	}
	if moreNewer {
		t.Errorf("zero-around moreNewer = true, want false")
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

	// Multi-word stays an implicit AND across terms.
	res, err = s.Search(ctx, "quick fox", "", "", 10)
	if err != nil || len(res) != 1 {
		t.Fatalf("search 'quick fox' = %d results err=%v, want 1", len(res), err)
	}

	// FTS5-special input must not error — it's matched literally, not parsed.
	for _, q := range []string{`"`, "quick AND", "fox:", "c++", "(quick", "*", "  "} {
		if _, err := s.Search(ctx, q, "", "", 10); err != nil {
			t.Errorf("search %q errored: %v", q, err)
		}
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

func TestMissedHighlights(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	at := func(secs int) time.Time { return base.Add(time.Duration(secs) * time.Second) }

	hlAt := func(network, buffer, from, text string, ts time.Time) core.Message {
		m := msg(network, buffer, from, text, core.MsgPrivmsg, ts)
		m.Highlight = true
		return m
	}

	// A highlight with no read marker contributes nothing (no baseline).
	s.Print(hlAt("libera", "#go", "alice", "early ping", at(0)))
	if got, err := s.MissedHighlights(ctx, 50); err != nil || len(got) != 0 {
		t.Fatalf("no marker should yield no missed highlights, got %+v (err %v)", got, err)
	}

	// Mark two buffers read at t=10, then add a mix of later activity.
	if err := s.MarkRead(ctx, "libera", "#go", at(10)); err != nil {
		t.Fatalf("MarkRead #go: %v", err)
	}
	if err := s.MarkRead(ctx, "libera", "#vim", at(10)); err != nil {
		t.Fatalf("MarkRead #vim: %v", err)
	}
	// Inserted in arrival order — MissedHighlights orders by store sequence
	// (rowid), consistent with Backlog/Search, so arrival order is the result
	// order regardless of the wall-clock timestamps.
	s.Print(hlAt("libera", "#go", "bob", "before marker", at(5)))           // read — excluded
	s.Print(hlAt("libera", "#vim", "dave", "first", at(20)))                // missed (arrives first)
	s.Print(hlAt("libera", "#go", "carol", "second", at(30)))               // missed
	s.Print(msg("libera", "#go", "erin", "plain", core.MsgPrivmsg, at(40))) // not a highlight
	selfHL := hlAt("libera", "#go", "me", "self ping", at(50))
	selfHL.Self = true
	s.Print(selfHL) // self — excluded

	got, err := s.MissedHighlights(ctx, 50)
	if err != nil {
		t.Fatalf("MissedHighlights: %v", err)
	}
	// Oldest-first by arrival across buffers: #vim/first then #go/second.
	if len(got) != 2 {
		t.Fatalf("want 2 missed highlights, got %d: %+v", len(got), got)
	}
	if got[0].Buffer != "#vim" || got[0].Text != "first" {
		t.Errorf("first row = %+v, want #vim/first", got[0])
	}
	if got[1].Buffer != "#go" || got[1].Text != "second" {
		t.Errorf("second row = %+v, want #go/second", got[1])
	}

	// The limit keeps the newest matches but still returns them oldest-first.
	got, err = s.MissedHighlights(ctx, 1)
	if err != nil {
		t.Fatalf("MissedHighlights limit: %v", err)
	}
	if len(got) != 1 || got[0].Text != "second" {
		t.Errorf("limit=1 should keep newest (#go/second), got %+v", got)
	}
}

// TestMsgidDedup: replaying a message we already hold (chathistory playback,
// bouncer replay) must not duplicate the row.
func TestMsgidDedup(t *testing.T) {
	s := openTest(t)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := msg("n", "#c", "alice", "hello", core.MsgPrivmsg, base)
	m.ID = "msgid-1"
	s.Print(m)
	s.Print(m) // replay
	// Same msgid in another buffer is a different logical line (kept), and
	// id-less lines are never deduped against each other.
	other := m
	other.Buffer = "#other"
	s.Print(other)
	noID := msg("n", "#c", "serv", "motd", core.MsgSystem, base)
	s.Print(noID)
	s.Print(noID)

	got, _, err := s.Backlog(context.Background(), "n", "#c", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 { // hello once + motd twice
		t.Fatalf("got %d rows, want 3: %+v", len(got), got)
	}
}

// TestMsgidDedupMigration: a database written before the unique msgid index
// existed (with duplicate rows) must be deduplicated on open.
func TestMsgidDedupMigration(t *testing.T) {
	path := t.TempDir() + "/legacy.db"
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the legacy state: drop the unique index, insert duplicates.
	if _, err := s.db.Exec(`DROP INDEX idx_messages_msgid`); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := s.db.Exec(
			`INSERT INTO messages(msgid, network, buffer, ts, kind, text) VALUES ('dup', 'n', '#c', 1, 'privmsg', 'hi')`,
		); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	s2, err := Open(path, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	got, _, err := s2.Backlog(context.Background(), "n", "#c", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows after migration, want 1", len(got))
	}
	// FTS must have been kept in sync by the delete trigger.
	hits, err := s2.Search(context.Background(), "hi", "n", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("FTS returned %d hits after dedup, want 1", len(hits))
	}
}

func TestPrune(t *testing.T) {
	s := openTest(t)
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fresh := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	s.Print(msg("n", "#c", "alice", "ancient history", core.MsgPrivmsg, old))
	s.Print(msg("n", "#c", "alice", "recent enough", core.MsgPrivmsg, fresh))

	ctx := context.Background()
	n, err := s.Prune(ctx, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || n != 1 {
		t.Fatalf("Prune = (%d, %v), want (1, nil)", n, err)
	}
	got, _, err := s.Backlog(ctx, "n", "#c", 0, 10)
	if err != nil || len(got) != 1 || got[0].Text != "recent enough" {
		t.Fatalf("backlog after prune = %+v, %v", got, err)
	}
	// FTS stays in sync via the delete trigger.
	if hits, _ := s.Search(ctx, "ancient", "n", "", 10); len(hits) != 0 {
		t.Errorf("pruned row still searchable: %+v", hits)
	}
}

func TestRedactPersists(t *testing.T) {
	s := openTest(t)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := msg("n", "#c", "alice", "delete me", core.MsgPrivmsg, base)
	m.ID = "gone-1"
	s.Print(m)
	s.Print(msg("n", "#c", "bob", "keep me", core.MsgPrivmsg, base))

	s.Redact("n", "#c", "gone-1", "alice", "typo")
	got, _, err := s.Backlog(context.Background(), "n", "#c", 0, 10)
	if err != nil || len(got) != 1 || got[0].Text != "keep me" {
		t.Fatalf("backlog after redact = %+v, %v", got, err)
	}
	if hits, _ := s.Search(context.Background(), "delete", "n", "", 10); len(hits) != 0 {
		t.Errorf("redacted row still searchable: %+v", hits)
	}
	// Empty target is a no-op, not a wildcard delete.
	s.Redact("n", "#c", "", "x", "")
	if got, _, _ = s.Backlog(context.Background(), "n", "#c", 0, 10); len(got) != 1 {
		t.Errorf("empty-target redact deleted rows: %+v", got)
	}
}
