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
)

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
