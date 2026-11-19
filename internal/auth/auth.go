// Package auth provides password verification (bcrypt) and optionally persistent
// sessions for the multi-user daemon. It has no dependency on core, the
// store, or the server, so it can be reused as the User abstraction grows.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
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
	User       string    `json:"user"`
	Expires    time.Time `json:"expires"`
	Credential string    `json:"credential,omitempty"`
}

// Sessions is a thread-safe store of opaque session tokens.
type Sessions struct {
	path        string
	scope       string
	credentials map[string]string
	ttl         time.Duration
	mu          sync.Mutex
	toks        map[string]session
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

// NewPersistentSessions loads a hash-only session file. Missing files are created;
// corrupt or unreadable files return an error. credentials, when non-nil, binds
// each user to its current password hash and drops removed or changed accounts.
func NewPersistentSessions(ttl time.Duration, path, scope string, credentials map[string]string) (*Sessions, error) {
	s := NewSessions(ttl)
	s.path, s.scope = path, scope
	if credentials != nil {
		s.credentials = map[string]string{}
		for user, credential := range credentials {
			s.credentials[user] = fingerprint(credential)
		}
	}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		var disk sessionFile
		if err := json.Unmarshal(data, &disk); err != nil {
			return nil, fmt.Errorf("sessions: %w", err)
		}
		if disk.Version != 1 {
			return nil, fmt.Errorf("unsupported session file version %d", disk.Version)
		}
		if disk.Scope == scope {
			for hash, sess := range disk.Sessions {
				decoded, err := hex.DecodeString(hash)
				if err != nil || len(decoded) != sha256.Size {
					return nil, fmt.Errorf("invalid session hash")
				}
				if s.credentials != nil {
					credential, ok := s.credentials[sess.User]
					if !ok || credential != sess.Credential {
						continue
					}
				}
				s.toks[hash] = sess
			}
		}
	}
	s.prune()
	if err := s.persist(); err != nil {
		return nil, err
	}
	return s, nil
}

type sessionFile struct {
	Version  int                `json:"version"`
	Scope    string             `json:"scope"`
	Sessions map[string]session `json:"sessions"`
}

func fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (s *Sessions) prune() bool {
	changed := false
	for hash, sess := range s.toks {
		if !time.Now().Before(sess.Expires) {
			delete(s.toks, hash)
			changed = true
		}
	}
	return changed
}

func (s *Sessions) persist() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(sessionFile{Version: 1, Scope: s.scope, Sessions: s.toks})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".sessions-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), s.path)
}

// Create issues a new token for a user. A persistence failure fails closed.
func (s *Sessions) Create(user string) string {
	tok := newToken()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	credential := s.credentials[user]
	if s.credentials != nil {
		if _, ok := s.credentials[user]; !ok {
			return ""
		}
	}
	hash := fingerprint(tok)
	s.toks[hash] = session{User: user, Expires: time.Now().Add(s.ttl), Credential: credential}
	if err := s.persist(); err != nil {
		delete(s.toks, hash)
		slog.Error("persist session", "err", err)
		return ""
	}
	return tok
}

// Lookup returns the user for a valid, unexpired token.
func (s *Sessions) Lookup(tok string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.prune() {
		if err := s.persist(); err != nil {
			slog.Error("prune sessions", "err", err)
		}
	}
	sess, ok := s.toks[fingerprint(tok)]
	return sess.User, ok
}

// Delete invalidates a token (logout).
func (s *Sessions) Delete(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	delete(s.toks, fingerprint(tok))
	if err := s.persist(); err != nil {
		slog.Error("delete session", "err", err)
	}
}

func newToken() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}
