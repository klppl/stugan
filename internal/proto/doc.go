// Package proto defines the typed JSON wire protocol shared between the
// daemon and the browser: every client→server and server→client event as
// a Go struct. These structs are the single source of truth; the
// TypeScript mirror in client/src/proto is generated/kept in sync from
// them. See docs/protocol.md.
//
// The concrete event set is a Phase 0 deliverable awaiting sign-off and is
// added in Phase 2.
package proto
