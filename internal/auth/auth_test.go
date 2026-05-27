package auth

import (
	"testing"
	"time"
)

func TestVerify(t *testing.T) {
	hash, err := HashPassword("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	u := NewUsers(map[string]string{"alice": hash})

	if !u.Verify("alice", "s3cret") {
		t.Error("correct password rejected")
	}
	if u.Verify("alice", "wrong") {
		t.Error("wrong password accepted")
	}
	if u.Verify("bob", "s3cret") {
		t.Error("unknown user accepted")
	}
}

func TestSessions(t *testing.T) {
	s := NewSessions(time.Hour)
	tok := s.Create("alice")
	if tok == "" {
		t.Fatal("empty token")
	}
	if u, ok := s.Lookup(tok); !ok || u != "alice" {
		t.Fatalf("lookup = %q,%v", u, ok)
	}
	s.Delete(tok)
	if _, ok := s.Lookup(tok); ok {
		t.Error("token valid after delete")
	}
}

func TestSessionExpiry(t *testing.T) {
	s := NewSessions(time.Millisecond)
	tok := s.Create("alice")
	time.Sleep(5 * time.Millisecond)
	if _, ok := s.Lookup(tok); ok {
		t.Error("expired token still valid")
	}
}

func TestTokensUnique(t *testing.T) {
	s := NewSessions(time.Hour)
	seen := map[string]bool{}
	for range 100 {
		tok := s.Create("u")
		if seen[tok] {
			t.Fatal("duplicate token")
		}
		seen[tok] = true
	}
}
