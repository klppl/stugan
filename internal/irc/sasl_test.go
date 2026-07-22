package irc

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/lrstanley/girc"
)

// captureSender records the AUTHENTICATE chunks the mech writes itself.
type captureSender struct{ chunks []string }

func (c *captureSender) Send(e *girc.Event) {
	if e.Command == girc.AUTHENTICATE {
		c.chunks = append(c.chunks, e.Params[0])
	}
}

func TestSASLPlainEncode(t *testing.T) {
	tests := []struct {
		name string
		user string
		pass string
	}{
		{"short", "user", "hunter2"},
		// Payload lengths straddling the 400-byte chunk boundary. A PLAIN
		// response is base64("\x00user\x00pass"), so the raw length is
		// len(user)+len(pass)+2 and the encoded length 4*ceil(raw/3).
		{"one byte under a chunk", "user", strings.Repeat("p", 293)},
		{"exactly one chunk", "user", strings.Repeat("p", 294)},
		{"one byte over a chunk", "user", strings.Repeat("p", 295)},
		{"two chunks", "user", strings.Repeat("p", 594)},
		{"bouncer login", "anders/libera@stugan", strings.Repeat("p", 512)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			send := &captureSender{}
			mech := &saslPlain{user: tt.user, pass: tt.pass, client: send}

			last := mech.Encode([]string{"+"})

			// Every chunk the mech writes must be exactly saslChunk bytes:
			// a shorter one ends the response, so the server would decode a
			// truncated payload.
			for i, c := range send.chunks {
				if len(c) != saslChunk {
					t.Errorf("chunk %d: len = %d, want %d", i, len(c), saslChunk)
				}
			}
			if len(last) == 0 || len(last) > saslChunk {
				t.Errorf("final chunk: len = %d, want 1..%d", len(last), saslChunk)
			}

			// Reassembled, the chunks must decode to the PLAIN response with
			// an empty authzid.
			got, err := base64.StdEncoding.DecodeString(strings.Join(send.chunks, "") + last)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if want := "\x00" + tt.user + "\x00" + tt.pass; string(got) != want {
				t.Errorf("payload = %q, want %q", got, want)
			}
		})
	}
}

// TestSASLPlainEncodeIgnoresNonChallenge checks the mech only answers the
// server's empty challenge; anything else makes girc abort the exchange.
func TestSASLPlainEncodeIgnoresNonChallenge(t *testing.T) {
	for _, params := range [][]string{{}, {"*"}, {"+", "+"}} {
		mech := &saslPlain{user: "user", pass: "pass", client: &captureSender{}}
		if got := mech.Encode(params); got != "" {
			t.Errorf("Encode(%q) = %q, want empty", params, got)
		}
	}
}
