package core

import "testing"

func TestHighlighter(t *testing.T) {
	h, err := NewHighlighter([]string{`\bdeploy\b`, "urgent"}, []string{"not urgent"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		text string
		nick string
		want bool
	}{
		{"hey alice are you there", "alice", true}, // nick mention
		{"ALICE!!", "alice", true},                 // case-insensitive
		{"malice in wonderland", "alice", false},   // word boundary
		{"time to deploy the build", "bob", true},  // pattern
		{"this is urgent", "bob", true},            // pattern
		{"this is not urgent", "bob", false},       // exception wins
		{"nothing to see", "bob", false},           // no match
	}
	for _, c := range cases {
		if got := h.Match(c.text, c.nick); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.text, c.nick, got, c.want)
		}
	}
}

func TestHighlighterRulesRoundTrip(t *testing.T) {
	// Blank entries are dropped (a trailing empty line in the form must not
	// compile to a match-everything regex), and the kept sources round-trip.
	h, err := NewHighlighter([]string{"release", "", `\bship\b`}, []string{"", "draft"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := h.Patterns(), []string{"release", `\bship\b`}; !equalStrings(got, want) {
		t.Errorf("Patterns() = %v, want %v", got, want)
	}
	if got, want := h.Exceptions(), []string{"draft"}; !equalStrings(got, want) {
		t.Errorf("Exceptions() = %v, want %v", got, want)
	}
	// A blank line never matches.
	if h.Match("", "") {
		t.Error("empty text highlighted")
	}

	// A bad regex is rejected.
	if _, err := NewHighlighter([]string{"("}, nil); err == nil {
		t.Error("expected error for invalid regex")
	}

	// The nil highlighter (default) reports no rules and matches nothing.
	var nilH *Highlighter
	if nilH.Patterns() != nil || nilH.Exceptions() != nil {
		t.Error("nil highlighter should report nil rules")
	}
}

func TestEngineSetHighlighter(t *testing.T) {
	e := New(Options{}) // default: nick mentions only
	if p, ex := e.HighlightRules(); len(p) != 0 || len(ex) != 0 {
		t.Fatalf("default rules = %v/%v, want empty", p, ex)
	}
	hl, err := NewHighlighter([]string{"ping"}, []string{"ouch"})
	if err != nil {
		t.Fatal(err)
	}
	e.SetHighlighter(hl)
	p, ex := e.HighlightRules()
	if !equalStrings(p, []string{"ping"}) || !equalStrings(ex, []string{"ouch"}) {
		t.Fatalf("after swap rules = %v/%v", p, ex)
	}
	// A nil swap restores the default (nick-mentions-only) without panicking.
	e.SetHighlighter(nil)
	if p, ex := e.HighlightRules(); len(p) != 0 || len(ex) != 0 {
		t.Fatalf("after nil swap rules = %v/%v, want empty", p, ex)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestExpandAlias(t *testing.T) {
	cases := []struct {
		tmpl string
		args []string
		want string
	}{
		{"/join #$1", []string{"go"}, "/join #go"},
		{"/msg $1 $2-", []string{"bob", "hello", "there"}, "/msg bob hello there"},
		{"say $*", []string{"a", "b", "c"}, "say a b c"},
		{"x$1y", nil, "xy"},                       // missing arg expands to empty
		{"cost $5 dollars", nil, "cost  dollars"}, // out-of-range arg → empty
	}
	for _, c := range cases {
		if got := expandAlias(c.tmpl, c.args); got != c.want {
			t.Errorf("expandAlias(%q, %v) = %q, want %q", c.tmpl, c.args, got, c.want)
		}
	}
}
