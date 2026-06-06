package server

import (
	"encoding/json"
	"strings"

	"github.com/klippelism/stugan/internal/proto"
)

// Per-user preference keys stored via Tenant.Prefs. Values are opaque JSON the
// store never interprets (see store.Pref).
const (
	prefHighlight = "highlight" // proto.HighlightRules
	prefMuted     = "muted"     // []proto.MuteRef
	prefAliases   = "aliases"   // map[string]string (command name → expansion)
)

// sanitizeAliases normalizes a client-supplied alias table: it lowercases and
// trims names (dropping a leading slash a user might type), and discards any
// entry with an empty name, an empty expansion, or whitespace in the name (the
// name must be a single slash-command token). The result is a fresh map.
func sanitizeAliases(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		k = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(k), "/"))
		v = strings.TrimSpace(v)
		if k == "" || v == "" || strings.ContainsAny(k, " \t") {
			continue
		}
		out[k] = v
	}
	return out
}

// loadMuted reads a tenant's muted-buffer set, or nil when unset or on any
// error (mute is a best-effort convenience, never worth failing a request for).
func loadMuted(t *Tenant) []proto.MuteRef {
	if t == nil || t.Prefs == nil {
		return nil
	}
	v, err := t.Prefs.Pref(prefMuted)
	if err != nil || v == "" {
		return nil
	}
	var refs []proto.MuteRef
	if json.Unmarshal([]byte(v), &refs) != nil {
		return nil
	}
	return refs
}

// setMuted returns refs with (network, buffer) added when muted, or removed
// when not. Buffer matching is case-insensitive, matching how the client keys
// mutes and how isMuted compares. The result is a fresh slice.
func setMuted(refs []proto.MuteRef, network, buffer string, muted bool) []proto.MuteRef {
	out := make([]proto.MuteRef, 0, len(refs)+1)
	for _, r := range refs {
		if r.Network == network && strings.EqualFold(r.Buffer, buffer) {
			continue // drop any existing entry; re-added below if muting
		}
		out = append(out, r)
	}
	if muted {
		out = append(out, proto.MuteRef{Network: network, Buffer: buffer})
	}
	return out
}

// isMuted reports whether (network, buffer) is in the muted set.
func isMuted(refs []proto.MuteRef, network, buffer string) bool {
	for _, r := range refs {
		if r.Network == network && strings.EqualFold(r.Buffer, buffer) {
			return true
		}
	}
	return false
}
