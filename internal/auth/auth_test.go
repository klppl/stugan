package auth

import (
	"os"
	"path/filepath"
	"strings"
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

func TestPersistentSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "sessions.json")
	open := func(credentials map[string]string) *Sessions {
		t.Helper()
		s, err := NewPersistentSessions(time.Hour, path, "test", credentials)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	creds := map[string]string{"alice": "hash-a", "bob": "hash-b", "carol": "hash-c"}
	s := open(creds)
	alice, bob, carol := s.Create("alice"), s.Create("bob"), s.Create("carol")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{alice, bob, carol} {
		if strings.Contains(string(data), token) || !strings.Contains(string(data), fingerprint(token)) {
			t.Fatal("disk must contain only token hashes")
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	s = open(creds)
	if u, ok := s.Lookup(alice); !ok || u != "alice" {
		t.Fatal("session lost on restart")
	}
	s.Delete(alice)
	s = open(creds)
	if _, ok := s.Lookup(alice); ok {
		t.Fatal("logout lost on restart")
	}
	s = open(map[string]string{"bob": "changed", "alice": "hash-a"})
	for _, token := range []string{bob, carol} {
		if _, ok := s.Lookup(token); ok {
			t.Fatal("changed/removed account survived")
		}
	}
	if len(s.toks) != 0 {
		t.Fatal("invalid sessions not pruned")
	}
}

func TestPersistentSessionExpiry(t *testing.T) {
	for _, operation := range []string{"reload", "create", "delete", "lookup"} {
		t.Run(operation, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sessions.json")
			s, err := NewPersistentSessions(time.Hour, path, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			token := s.Create("alice")
			sess := s.toks[fingerprint(token)]
			sess.Expires = time.Now().Add(-time.Second)
			s.toks[fingerprint(token)] = sess
			if err := s.persist(); err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "reload":
				s, err = NewPersistentSessions(time.Hour, path, "", nil)
				if err != nil {
					t.Fatal(err)
				}
			case "create":
				s.Create("bob")
			case "delete":
				s.Delete("unknown")
			case "lookup":
				s.Lookup(token)
			}
			if _, ok := s.Lookup(token); ok {
				t.Fatal("expired session accepted")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), fingerprint(token)) {
				t.Fatal("expired session retained on disk")
			}
		})
	}
}

func TestPersistentSessionScopeAndCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	s, err := NewPersistentSessions(time.Hour, path, "one", nil)
	if err != nil {
		t.Fatal(err)
	}
	token := s.Create("gate")
	s, err = NewPersistentSessions(time.Hour, path, "two", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Lookup(token); ok {
		t.Fatal("changed scope accepted")
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPersistentSessions(time.Hour, path, "two", nil); err == nil {
		t.Fatal("corrupt file accepted")
	}
	if _, err := NewPersistentSessions(time.Hour, filepath.Dir(path), "", nil); err == nil {
		t.Fatal("unreadable file accepted")
	}
}
