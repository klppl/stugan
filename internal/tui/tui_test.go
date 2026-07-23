package tui

import "testing"

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
