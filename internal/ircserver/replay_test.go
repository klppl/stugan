package ircserver

import (
	"strings"
	"testing"
)

func TestMemberPrefixSymbol(t *testing.T) {
	if s := MemberPrefixSymbol("@+"); s != "@" {
		t.Errorf("MemberPrefixSymbol(@+) = %q; want @", s)
	}
	if s := MemberPrefixSymbol("+"); s != "+" {
		t.Errorf("MemberPrefixSymbol(+) = %q; want +", s)
	}
	if s := MemberPrefixSymbol("~"); s != "~" {
		t.Errorf("MemberPrefixSymbol(~) = %q; want ~", s)
	}
	if s := MemberPrefixSymbol(""); s != "" {
		t.Errorf("MemberPrefixSymbol(\"\") = %q; want empty", s)
	}
}

func TestBuildNamesLines(t *testing.T) {
	var names []string
	for i := 0; i < 50; i++ {
		names = append(names, "@alice", "+bob", "charlie")
	}

	lines := BuildNamesLines("tester", "#mychan", names)
	if len(lines) < 2 {
		t.Fatalf("BuildNamesLines returned %d lines, want >= 2", len(lines))
	}

	lastLine := lines[len(lines)-1]
	if !strings.Contains(lastLine, "366 tester #mychan :End of /NAMES list.") {
		t.Errorf("Last line = %q; want 366 ENDOFNAMES", lastLine)
	}

	for _, l := range lines {
		if len(l) > 512 {
			t.Errorf("Line exceeds 512 bytes: len=%d line=%q", len(l), l)
		}
	}
}

func TestIsServicesNick(t *testing.T) {
	if !IsServicesNick("NickServ") {
		t.Errorf("IsServicesNick(NickServ) = false, want true")
	}
	if !IsServicesNick("ChanServ") {
		t.Errorf("IsServicesNick(ChanServ) = false, want true")
	}
	if !IsServicesNick("Q") {
		t.Errorf("IsServicesNick(Q) = false, want true")
	}
	if IsServicesNick("alex") {
		t.Errorf("IsServicesNick(alex) = true, want false")
	}
}
