package irc

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"encoding/base64"
	"slices"
	"strconv"
	"strings"
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

// TestSASLScramRoundTripAllVariants plays an independent server side of
// SCRAM, written from RFC 5802 §3 rather than sharing the client's helpers, so
// a round trip proves the two agree for every variant.
func TestSASLScramRoundTripAllVariants(t *testing.T) {
	for _, v := range scramHashes {
		t.Run(v.name, func(t *testing.T) {
			const pass, user = "pässwörd", "alice"
			salt := []byte("0123456789abcdef")
			iter := 4096

			m := &saslScram{user: user, pass: pass, client: &captureSender{}}
			m.advertise("sasl=PLAIN," + v.name)
			if got := m.Method(); got != v.name {
				t.Fatalf("Method = %s", got)
			}

			raw, _ := base64.StdEncoding.DecodeString(m.Encode([]string{"+"}))
			clientFirst := string(raw)
			bare := clientFirst[len("n,,"):]
			attrs := scramAttrs(bare)
			if attrs["n"] != user {
				t.Fatalf("client-first = %q", clientFirst)
			}
			snonce := attrs["r"] + "srvnonce"
			serverFirst := "r=" + snonce + ",s=" + base64.StdEncoding.EncodeToString(salt) + ",i=" + strconv.Itoa(iter)

			// Server side, derived from the stored password only.
			salted, _ := pbkdf2.Key(v.New, saslPrep(pass), salt, iter, v.New().Size())
			mac := func(key []byte, msg string) []byte {
				h := hmac.New(v.New, key)
				h.Write([]byte(msg))
				return h.Sum(nil)
			}
			storedKey := v.New()
			storedKey.Write(mac(salted, "Client Key"))
			serverKey := mac(salted, "Server Key")

			finalRaw, _ := base64.StdEncoding.DecodeString(m.Encode([]string{b64(serverFirst)}))
			final := string(finalRaw)
			proofB64 := scramAttrs(final)["p"]
			noProof := strings.TrimSuffix(final, ",p="+proofB64)
			authMsg := bare + "," + serverFirst + "," + noProof
			clientSig := mac(storedKey.Sum(nil), authMsg)
			proof, _ := base64.StdEncoding.DecodeString(proofB64)
			if len(proof) != len(clientSig) {
				t.Fatalf("proof length %d, want %d", len(proof), len(clientSig))
			}
			recovered := make([]byte, len(proof))
			for i := range proof {
				recovered[i] = proof[i] ^ clientSig[i]
			}
			check := v.New()
			check.Write(recovered)
			if !hmac.Equal(check.Sum(nil), storedKey.Sum(nil)) {
				t.Fatal("server could not verify the client proof")
			}

			sig := "v=" + base64.StdEncoding.EncodeToString(mac(serverKey, authMsg))
			if got := m.Encode([]string{b64(sig)}); got != "+" {
				t.Fatalf("valid server signature = %q", got)
			}
		})
	}
}

func TestSASLScramMethodSelection(t *testing.T) {
	tests := []struct {
		caps string
		want string
	}{
		{"", "SCRAM-SHA-256"}, // nothing advertised: the default
		{"sasl", "SCRAM-SHA-256"},
		{"sasl=PLAIN,EXTERNAL", "SCRAM-SHA-256"},
		{"sasl=PLAIN,SCRAM-SHA-256", "SCRAM-SHA-256"},
		{"away-notify sasl=ECDSA-NIST256P-CHALLENGE,EXTERNAL,PLAIN,SCRAM-SHA-512 batch", "SCRAM-SHA-512"},
		{"sasl=scram-sha-256,scram-sha-512", "SCRAM-SHA-512"}, // strongest wins, case-insensitive
	}
	for _, tt := range tests {
		m := &saslScram{}
		m.advertise(tt.caps)
		if got := m.Method(); got != tt.want {
			t.Errorf("advertised %q: Method = %s, want %s", tt.caps, got, tt.want)
		}
	}
	// A reconnect to a different server must not reuse the old list.
	m := &saslScram{}
	m.advertise("sasl=SCRAM-SHA-512")
	m.reset()
	if got := m.Method(); got != "SCRAM-SHA-256" {
		t.Errorf("after reset Method = %s", got)
	}
}

// Conn.Caps only reports capabilities on knownCaps; one missing from it is
// silently invisible to the engine and the UI.
func TestKnownCapsCoverFeatureCaps(t *testing.T) {
	for _, c := range []string{"soju.im/bouncer-networks", "soju.im/bouncer-networks-notify", "chghost", "setname", "draft/chathistory", "chathistory"} {
		if !slices.Contains(knownCaps, c) {
			t.Errorf("%s missing from knownCaps", c)
		}
	}
}
