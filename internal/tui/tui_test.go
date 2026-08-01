package tui

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/klippelism/stugan/internal/core"
)

type historyStub struct{ before int64 }

func (h *historyStub) Backlog(_ context.Context, _, _ string, before int64, _ int) ([]core.Message, bool, error) {
	h.before = before
	return []core.Message{{ID: "older", Seq: before - 1}}, false, nil
}
func (*historyStub) UnreadCounts(context.Context) ([]core.UnreadCount, error)  { return nil, nil }
func (*historyStub) MarkRead(context.Context, string, string, time.Time) error { return nil }

func TestRegistryAddRemove(t *testing.T) {
	r := newRegistry()
	a := &session{user: "alice"}
	b := &session{user: "alice"}
	c := &session{user: "bob"}
	r.add(a)
	r.add(b)
	r.add(c)

	if got := len(r.sessions["alice"]); got != 2 {
		t.Fatalf("alice sessions = %d, want 2", got)
	}
	r.remove(a)
	if got := len(r.sessions["alice"]); got != 1 {
		t.Fatalf("after remove, alice sessions = %d, want 1", got)
	}
	r.remove(b)
	if _, ok := r.sessions["alice"]; ok {
		t.Fatal("alice bucket should be gone once empty")
	}
	if got := len(r.sessions["bob"]); got != 1 {
		t.Fatalf("bob sessions = %d, want 1", got)
	}
}

func TestBufRef(t *testing.T) {
	a := bufRef{net: "libera", name: "#go"}
	b := bufRef{net: "libera", name: "#go"}
	c := bufRef{net: "libera", name: "#rust"}
	if !a.eq(b) {
		t.Fatal("equal refs should compare equal")
	}
	if a.eq(c) {
		t.Fatal("different names should not be equal")
	}
	if a.key() == c.key() {
		t.Fatal("keys must differ by buffer name")
	}
	if !(bufRef{}).zero() || a.zero() {
		t.Fatal("zero() wrong")
	}
}

func TestLiveMessageDoesNotSuppressInitialBacklog(t *testing.T) {
	m := &model{
		bufs: map[string]*buf{}, unread: map[string]int{}, highlite: map[string]int{},
	}
	m.appendMessage(core.Message{Network: "libera", Buffer: "#go", ID: "live"})
	if got := m.bufs[(bufRef{net: "libera", name: "#go"}).key()]; got == nil || got.loaded {
		t.Fatalf("live-only buffer = %+v, want loaded=false", got)
	}
}

func TestLoadOlderUsesOldestPersistedSequence(t *testing.T) {
	hist := &historyStub{}
	active := bufRef{net: "libera", name: "#go"}
	m := &model{
		hist: hist, active: active,
		bufs: map[string]*buf{active.key(): {
			loaded: true, more: true,
			msgs: []core.Message{{ID: "oldest", Seq: 42}, {ID: "live", Seq: 0}},
		}},
	}
	cmd := m.loadOlder()
	if cmd == nil {
		t.Fatal("loadOlder returned no command")
	}
	if !m.bufs[active.key()].loading {
		t.Fatal("loadOlder did not guard the in-flight request")
	}
	msg, ok := cmd().(backlogMsg)
	if !ok {
		t.Fatalf("command returned %T, want backlogMsg", cmd())
	}
	if hist.before != 42 || msg.beforeSeq != 42 {
		t.Fatalf("before cursor = history:%d message:%d, want 42", hist.before, msg.beforeSeq)
	}
}

func TestMergeBacklogDeduplicatesLiveOverlap(t *testing.T) {
	history := []core.Message{{ID: "old", Seq: 1}, {ID: "same", Seq: 2}}
	live := []core.Message{{ID: "same"}, {ID: "new"}}
	got := mergeBacklog(history, live)
	if len(got) != 3 || got[0].ID != "old" || got[1].ID != "same" || got[2].ID != "new" {
		t.Fatalf("merged backlog = %+v", got)
	}
}

func TestRankOf(t *testing.T) {
	// op/admin/owner rank above halfop above voice above plain.
	if rankOf("@") >= rankOf("%") || rankOf("%") >= rankOf("+") || rankOf("+") >= rankOf("") {
		t.Fatalf("prefix ranking out of order: @=%d %%=%d +=%d ''=%d",
			rankOf("@"), rankOf("%"), rankOf("+"), rankOf(""))
	}
}

func TestSplitComma(t *testing.T) {
	got := splitComma(" #one, #two ,, #three ")
	want := []string{"#one", "#two", "#three"}
	if len(got) != len(want) {
		t.Fatalf("splitComma = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("splitComma[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if splitComma("   ") != nil {
		t.Fatal("all-blank should yield nil")
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Fatalf("short unchanged: %q", got)
	}
	if got := truncate("hello world", 5); got != "hell…" {
		t.Fatalf("truncate = %q, want %q", got, "hell…")
	}
	if got := truncate("x", 0); got != "" {
		t.Fatalf("zero width = %q", got)
	}
}

func TestPluginsOverlayTabs(t *testing.T) {
	p := &pluginsOverlay{}
	if p.tab != 0 {
		t.Fatalf("initial tab = %d, want 0", p.tab)
	}
	p.Update(nil, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("tab")})
	if p.tab != 1 {
		t.Fatalf("after tab key, tab = %d, want 1", p.tab)
	}
	p.Update(nil, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("tab")})
	if p.tab != 0 {
		t.Fatalf("after second tab key, tab = %d, want 0", p.tab)
	}
}
