package irc

import (
	"encoding/base64"
	"testing"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// TestSASLScramRFC7677 replays the SCRAM-SHA-256 test vector from RFC 7677 §3.
func TestSASLScramRFC7677(t *testing.T) {
	m := &saslScram{user: "user", pass: "pencil", client: &captureSender{}}

	first := m.Encode([]string{"+"})
	raw, _ := base64.StdEncoding.DecodeString(first)
	if got := string(raw); len(got) < 8 || got[:8] != "n,,n=use" {
		t.Fatalf("client-first = %q", got)
	}

	// Pin the nonce to the RFC's so the proof is deterministic.
	m.cnonce = "rOprNGfwEbeRWgbNEkqO"
	m.clientFirst = "n=user,r=" + m.cnonce
	serverFirst := "r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096"
	final := m.Encode([]string{b64(serverFirst)})
	wantFinal := "c=biws,r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,p=dHzbZapWIk4jUhN+Ute9ytag9zjfMHgsqmmiz7AndVQ="
	if final != b64(wantFinal) {
		got, _ := base64.StdEncoding.DecodeString(final)
		t.Fatalf("client-final = %q, want %q", got, wantFinal)
	}

	if got := m.Encode([]string{b64("v=wrong")}); got != "" {
		t.Fatalf("bad server signature accepted: %q", got)
	}
	m.step = scramSentFinal
	if got := m.Encode([]string{b64("v=6rriTRBi23WpRR/wtup+mMhUZUn/dB5nLTJRsjl95G4=")}); got != "+" {
		t.Fatalf("valid server signature = %q, want +", got)
	}
}

func TestSASLScramRejects(t *testing.T) {
	start := func() *saslScram {
		m := &saslScram{user: "u", pass: "p", client: &captureSender{}}
		m.Encode([]string{"+"})
		return m
	}
	salt := b64("saltsalt")
	tests := map[string]func(m *saslScram) string{
		"nonce not extended": func(m *saslScram) string { return "r=" + m.cnonce + ",s=" + salt + ",i=4096" },
		"foreign nonce":      func(m *saslScram) string { return "r=otherserversnonce,s=" + salt + ",i=4096" },
		"zero iterations":    func(m *saslScram) string { return "r=" + m.cnonce + "x,s=" + salt + ",i=0" },
		"absurd iterations":  func(m *saslScram) string { return "r=" + m.cnonce + "x,s=" + salt + ",i=999999999" },
		"missing salt":       func(m *saslScram) string { return "r=" + m.cnonce + "x,i=4096" },
		"not an iteration":   func(m *saslScram) string { return "r=" + m.cnonce + "x,s=" + salt + ",i=abc" },
		"server error":       func(m *saslScram) string { return "e=invalid-proof" },
	}
	for name, msg := range tests {
		t.Run(name, func(t *testing.T) {
			m := start()
			if got := m.Encode([]string{b64(msg(m))}); got != "" {
				t.Errorf("accepted %q", msg(m))
			}
		})
	}
	t.Run("first message must be empty challenge", func(t *testing.T) {
		m := &saslScram{user: "u", pass: "p", client: &captureSender{}}
		if got := m.Encode([]string{b64("hello")}); got != "" {
			t.Errorf("answered %q", got)
		}
	})
	t.Run("not base64", func(t *testing.T) {
		m := start()
		if got := m.Encode([]string{"!!!"}); got != "" {
			t.Errorf("answered %q", got)
		}
	})
}

func TestSASLScramResetAndEscape(t *testing.T) {
	m := &saslScram{user: "a=b,c", pass: "p", client: &captureSender{}}
	raw, _ := base64.StdEncoding.DecodeString(m.Encode([]string{"+"}))
	if got := string(raw); got[:14] != "n,,n=a=3Db=2Cc" {
		t.Errorf("username not escaped: %q", got)
	}
	m.reset()
	if m.step != scramStart || m.user != "a=b,c" || m.client == nil {
		t.Errorf("reset: %+v", m)
	}
	if m.Encode([]string{"+"}) == "" {
		t.Error("cannot restart after reset")
	}
}
