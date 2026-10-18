package irc

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strconv"
	"strings"

	"github.com/lrstanley/girc"
	"golang.org/x/text/secure/precis"
)

// scramMaxIter bounds the PBKDF2 iteration count a server may demand, so a
// hostile or broken server can't pin a CPU for minutes. Real deployments use
// 4096–600000.
const scramMaxIter = 1 << 20

// saslScram implements girc.SASLMech for SCRAM-SHA-256 (RFC 5802 / RFC 7677,
// no channel binding). Unlike PLAIN the password never crosses the wire, and
// the client verifies the server's signature, so a man in the middle cannot
// pose as the network.
//
// The exchange, driven by the server's AUTHENTICATE lines (each Encode call
// is one server message):
//
//	S: +                       → C: n,,n=user,r=cnonce          (client-first)
//	S: r=…,s=salt,i=iters      → C: c=biws,r=…,p=proof          (client-final)
//	S: v=signature             → C: +    (after verifying it)
//	S: 903
//
// Encode returning "" aborts the exchange (girc closes the connection), which
// is what every failure here does.
type saslScram struct {
	user, pass string
	client     eventSender

	// per-exchange state, cleared by reset on every (re)connect.
	step        int
	cnonce      string
	clientFirst string // client-first-message-bare
	serverSig   []byte
}

var _ girc.SASLMech = (*saslScram)(nil)

const (
	scramStart = iota
	scramSentFirst
	scramSentFinal
	scramDone
)

// Method identifies the mechanism to girc and the server.
func (s *saslScram) Method() string { return "SCRAM-SHA-256" }

// reset forgets any half-finished exchange; Connect calls it before dialing.
func (s *saslScram) reset() { *s = saslScram{user: s.user, pass: s.pass, client: s.client} }

// Encode answers one server AUTHENTICATE message.
func (s *saslScram) Encode(params []string) string {
	if len(params) != 1 {
		return ""
	}
	switch s.step {
	case scramStart:
		if params[0] != "+" {
			return ""
		}
		return s.first()
	case scramSentFirst:
		return s.final(params[0])
	case scramSentFinal:
		return s.verify(params[0])
	}
	return ""
}

func (s *saslScram) first() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	s.cnonce = base64.StdEncoding.EncodeToString(b[:])
	s.clientFirst = "n=" + scramEscape(saslPrep(s.user)) + ",r=" + s.cnonce
	s.step = scramSentFirst
	return sendChunked(s.client, base64.StdEncoding.EncodeToString([]byte("n,,"+s.clientFirst)))
}

func (s *saslScram) final(arg string) string {
	raw, err := base64.StdEncoding.DecodeString(arg)
	if err != nil {
		return ""
	}
	serverFirst := string(raw)
	attrs := scramAttrs(serverFirst)
	nonce, saltB64, iterStr := attrs["r"], attrs["s"], attrs["i"]
	// The server must extend our nonce, or it could be replaying an old one.
	if !strings.HasPrefix(nonce, s.cnonce) || len(nonce) == len(s.cnonce) {
		return ""
	}
	salt, err := base64.StdEncoding.DecodeString(saltB64)
	if err != nil || len(salt) == 0 {
		return ""
	}
	iter, err := strconv.Atoi(iterStr)
	if err != nil || iter < 1 || iter > scramMaxIter {
		return ""
	}

	salted, err := pbkdf2.Key(sha256.New, saslPrep(s.pass), salt, iter, sha256.Size)
	if err != nil {
		return ""
	}
	clientKey := hmacSHA256(salted, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	noProof := "c=biws,r=" + nonce // biws = base64("n,,"), no channel binding
	authMsg := s.clientFirst + "," + serverFirst + "," + noProof
	clientSig := hmacSHA256(storedKey[:], authMsg)
	proof := make([]byte, len(clientKey))
	for i := range proof {
		proof[i] = clientKey[i] ^ clientSig[i]
	}
	s.serverSig = hmacSHA256(hmacSHA256(salted, "Server Key"), authMsg)
	s.step = scramSentFinal
	return sendChunked(s.client, base64.StdEncoding.EncodeToString(
		[]byte(noProof+",p="+base64.StdEncoding.EncodeToString(proof))))
}

// verify checks the server's signature; only a server that knows the stored
// verifier for our password can produce it.
func (s *saslScram) verify(arg string) string {
	raw, err := base64.StdEncoding.DecodeString(arg)
	if err != nil {
		return ""
	}
	sig, err := base64.StdEncoding.DecodeString(scramAttrs(string(raw))["v"])
	if err != nil || subtle.ConstantTimeCompare(sig, s.serverSig) != 1 {
		return ""
	}
	s.step = scramDone
	return "+" // empty final response; the server answers 903
}

func hmacSHA256(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}

// scramAttrs splits "k=v,k=v" into a map (values may themselves contain '=').
func scramAttrs(msg string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(msg, ",") {
		if k, v, ok := strings.Cut(part, "="); ok && len(k) == 1 {
			out[k] = v
		}
	}
	return out
}

// scramEscape escapes the two characters reserved in a SCRAM username.
func scramEscape(u string) string {
	return strings.NewReplacer("=", "=3D", ",", "=2C").Replace(u)
}

// saslPrep normalizes a credential per RFC 7677 (SASLprep). Input the profile
// rejects is sent as-is: the server then fails the proof, which is no worse
// than aborting here.
func saslPrep(s string) string {
	if p, err := precis.OpaqueString.String(s); err == nil {
		return p
	}
	return s
}
