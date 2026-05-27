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
