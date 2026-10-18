package irc

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"hash"
	"strconv"
	"strings"
	"sync"

	"github.com/lrstanley/girc"
	"golang.org/x/text/secure/precis"
)

// scramMaxIter bounds the PBKDF2 iteration count a server may demand, so a
// hostile or broken server can't pin a CPU for minutes. Real deployments use
// 4096–600000.
const scramMaxIter = 1 << 20

// scramHash is one SCRAM variant. scramHashes lists them strongest first;
// servers differ in what they offer (Libera: SCRAM-SHA-512 only; others
// SCRAM-SHA-256), so the variant is chosen from the server's advertised list.
type scramHash struct {
	name string
	New  func() hash.Hash
}

var scramHashes = []scramHash{
	{"SCRAM-SHA-512", sha512.New},
	{"SCRAM-SHA-256", sha256.New},
}

// saslScram implements girc.SASLMech for SCRAM-SHA-512 / SCRAM-SHA-256 (RFC 5802
// / RFC 7677, no channel binding). Unlike PLAIN the password never crosses the wire, and
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

	// mu guards mechs and hash: the CAP LS handler records the server's
	// mechanisms and girc then calls Method, both on girc's read goroutine,
	// but reset runs on the connecting one.
	mu    sync.Mutex
	mechs map[string]bool // SASL mechanisms the server advertised
	hash  scramHash       // the variant Method chose

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

// advertise records the mechanisms from a CAP LS/NEW line's sasl value
// ("sasl=PLAIN,SCRAM-SHA-512"). Multi-line LS replies accumulate.
func (s *saslScram) advertise(caps string) {
	for _, tok := range strings.Fields(caps) {
		val, ok := strings.CutPrefix(tok, "sasl=")
		if !ok {
			continue
		}
		s.mu.Lock()
		if s.mechs == nil {
			s.mechs = map[string]bool{}
		}
		for m := range strings.SplitSeq(val, ",") {
			s.mechs[strings.ToUpper(m)] = true
		}
		s.mu.Unlock()
	}
}

// Method identifies the mechanism to girc and the server: the strongest SCRAM
// variant the server offered, or SCRAM-SHA-256 when it listed none (an older
// server that doesn't announce mechanisms — it will refuse if it lacks it).
func (s *saslScram) Method() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hash = scramHashes[len(scramHashes)-1]
	for _, h := range scramHashes {
		if s.mechs[h.name] {
			s.hash = h
			break
		}
	}
	return s.hash.name
}

func (s *saslScram) variant() scramHash {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hash.New == nil {
		s.hash = scramHashes[len(scramHashes)-1]
	}
	return s.hash
}

// reset forgets the exchange and what the last server advertised; Connect
// calls it before dialing.
func (s *saslScram) reset() {
	s.mu.Lock()
	s.mechs, s.hash = nil, scramHash{}
	s.mu.Unlock()
	s.step, s.cnonce, s.clientFirst, s.serverSig = scramStart, "", "", nil
}

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

	v := s.variant()
	salted, err := pbkdf2.Key(v.New, saslPrep(s.pass), salt, iter, v.New().Size())
	if err != nil {
		return ""
	}
	clientKey := hmacSum(v.New, salted, "Client Key")
	stored := v.New()
	stored.Write(clientKey)
	noProof := "c=biws,r=" + nonce // biws = base64("n,,"), no channel binding
	authMsg := s.clientFirst + "," + serverFirst + "," + noProof
	clientSig := hmacSum(v.New, stored.Sum(nil), authMsg)
	proof := make([]byte, len(clientKey))
	for i := range proof {
		proof[i] = clientKey[i] ^ clientSig[i]
	}
	s.serverSig = hmacSum(v.New, hmacSum(v.New, salted, "Server Key"), authMsg)
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

func hmacSum(h func() hash.Hash, key []byte, msg string) []byte {
	m := hmac.New(h, key)
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
