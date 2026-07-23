package irc

import (
	"encoding/base64"

	"github.com/lrstanley/girc"
)

// saslChunk is the maximum length of one AUTHENTICATE payload (IRCv3): a
// response longer than this is split, and a chunk of exactly saslChunk bytes
// tells the server another chunk follows.
const saslChunk = 400

// eventSender is the part of *girc.Client saslPlain needs, so the chunking can
// be tested without a server.
type eventSender interface {
	Send(*girc.Event)
}

// saslPlain implements girc.SASLMech for SASL PLAIN, replacing
// girc.SASLPlain for two reasons:
//
//  1. girc's chunker (cap_sasl.go handleSASL) writes auth[0:saslChunkSize-1]
//     — 399 bytes — but then advances the cursor by 400, so every response
//     longer than 400 base64 bytes goes out both truncated and with a byte
//     missing. A server that ends a multi-line response at any chunk shorter
//     than 400 (soju, ergo) decodes the short chunk immediately and answers
//     "904 Invalid base64-encoded response". Long credentials — a bouncer
//     login of the form user/network@client plus a generated password — are
//     enough to cross that line. We emit every chunk but the last ourselves
//     and hand girc a final one it writes correctly; girc appends the
//     trailing "AUTHENTICATE +" when that last chunk is exactly 400 bytes.
//
//  2. girc sends the authcid as the authzid too; every mainstream client
//     leaves the authzid empty ("\x00user\x00pass"), which is also
//     shorter on the wire.
//
// client is nil until New has built the girc client (the config holding this
// mech has to exist first); Encode only runs once a connection is dialing, so
// it is always set by then.
type saslPlain struct {
	user, pass string
	client     eventSender
}

var _ girc.SASLMech = (*saslPlain)(nil)

// Method identifies the mechanism to girc and the server.
func (s *saslPlain) Method() string { return "PLAIN" }

// Encode answers the server's "AUTHENTICATE +" with the PLAIN response,
// returning the final chunk for girc to write.
func (s *saslPlain) Encode(params []string) string {
	if len(params) != 1 || params[0] != "+" {
		return ""
	}
	payload := base64.StdEncoding.EncodeToString(
		[]byte("\x00" + s.user + "\x00" + s.pass))

	for len(payload) > saslChunk {
		s.client.Send(&girc.Event{
			Command:   girc.AUTHENTICATE,
			Params:    []string{payload[:saslChunk]},
			Sensitive: true,
		})
		payload = payload[saslChunk:]
	}
	return payload
}
