// Package auth provides password verification (bcrypt) and in-memory
// sessions for the multi-user daemon. It has no dependency on core, the
// store, or the server, so it can be reused as the User abstraction grows.
package auth

import (
	"crypto/rand"
	"encoding/base64"
	"maps"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword returns a bcrypt hash suitable for a config password_hash.
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(h), err
}

// Users holds username → bcrypt hash and verifies credentials in constant
// time relative to a real entry (a dummy compare runs for unknown users to
// avoid revealing which usernames exist).
type Users struct {
	hashes map[string]string
}

// dummyHash is a valid bcrypt hash of a random value, compared against when
// a username is unknown so the response time does not leak account existence.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("stugan-timing-guard"), bcrypt.DefaultCost)

// NewUsers builds a verifier from username → bcrypt-hash pairs.
func NewUsers(hashes map[string]string) *Users {
	m := make(map[string]string, len(hashes))
	maps.Copy(m, hashes)
	return &Users{hashes: m}
}

// Verify reports whether the username/password pair is valid.
func (u *Users) Verify(username, password string) bool {
	hash, ok := u.hashes[username]
	if !ok {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// session is one logged-in session.
type session struct {
	user    string
	expires time.Time
}

// Sessions is a thread-safe store of opaque session tokens.
type Sessions struct {
	ttl  time.Duration
	mu   sync.Mutex
	toks map[string]session
}

// NewSessions builds a session store with the given lifetime.
func NewSessions(ttl time.Duration) *Sessions {
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	return &Sessions{ttl: ttl, toks: map[string]session{}}
}

// TTL returns the session lifetime.
func (s *Sessions) TTL() time.Duration { return s.ttl }

// Create issues a new token for a user.
func (s *Sessions) Create(user string) string {
	tok := newToken()
	s.mu.Lock()
	s.toks[tok] = session{user: user, expires: time.Now().Add(s.ttl)}
	s.mu.Unlock()
	return tok
}

// Lookup returns the user for a valid, unexpired token.
func (s *Sessions) Lookup(tok string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.toks[tok]
	if !ok {
		return "", false
	}
	if time.Now().After(sess.expires) {
		delete(s.toks, tok)
		return "", false
	}
	return sess.user, true
}

// Delete invalidates a token (logout).
func (s *Sessions) Delete(tok string) {
	s.mu.Lock()
	delete(s.toks, tok)
	s.mu.Unlock()
}

func newToken() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}
