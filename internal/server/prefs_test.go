package server

import (
	"testing"

	"github.com/klippelism/stugan/internal/proto"
)

// memPrefs is an in-memory Prefs for exercising the mute helpers.
type memPrefs map[string]string

func (m memPrefs) Pref(key string) (string, error) { return m[key], nil }
func (m memPrefs) SetPref(key, value string) error { m[key] = value; return nil }

func TestSetMuted(t *testing.T) {
	var refs []proto.MuteRef

	// Mute two buffers.
	refs = setMuted(refs, "libera", "#go", true)
	refs = setMuted(refs, "libera", "#rust", true)
	if len(refs) != 2 {
		t.Fatalf("after two mutes: %v", refs)
	}
	if !isMuted(refs, "libera", "#go") || !isMuted(refs, "libera", "#rust") {
		t.Fatalf("expected both muted: %v", refs)
	}

	// Buffer match is case-insensitive; network match is exact.
	if !isMuted(refs, "libera", "#GO") {
		t.Error("buffer match should be case-insensitive")
	}
	if isMuted(refs, "oftc", "#go") {
		t.Error("network match should be exact")
	}

	// Re-muting the same buffer (different case) doesn't duplicate.
	refs = setMuted(refs, "libera", "#GO", true)
	if len(refs) != 2 {
		t.Fatalf("re-mute duplicated entry: %v", refs)
	}

	// Unmute removes it.
	refs = setMuted(refs, "libera", "#go", false)
	if isMuted(refs, "libera", "#go") || len(refs) != 1 {
		t.Fatalf("after unmute: %v", refs)
	}

	// Unmuting a buffer that isn't muted is a no-op.
	refs = setMuted(refs, "libera", "#never", false)
	if len(refs) != 1 {
		t.Fatalf("spurious unmute changed set: %v", refs)
	}
}

func TestLoadMutedNilSafe(t *testing.T) {
	// No tenant / no prefs / no stored value all yield nil, never panic.
	if loadMuted(nil) != nil {
		t.Error("loadMuted(nil) should be nil")
	}
	if loadMuted(&Tenant{}) != nil {
		t.Error("loadMuted with nil Prefs should be nil")
	}
	if loadMuted(&Tenant{Prefs: memPrefs{}}) != nil {
		t.Error("loadMuted with empty store should be nil")
	}

	// A round-tripped value loads back.
	p := memPrefs{}
	p.SetPref(prefMuted, `[{"network":"libera","buffer":"#go"}]`)
	got := loadMuted(&Tenant{Prefs: p})
	if len(got) != 1 || got[0].Network != "libera" || got[0].Buffer != "#go" {
		t.Fatalf("loadMuted = %v", got)
	}

	// Corrupt JSON loads as nil rather than erroring.
	bad := memPrefs{prefMuted: "not json"}
	if loadMuted(&Tenant{Prefs: bad}) != nil {
		t.Error("corrupt muted blob should load as nil")
	}
}
