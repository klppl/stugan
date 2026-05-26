// Package store persists message history, networks, and sessions in a
// single SQLite database (pure-Go modernc.org/sqlite), with FTS5 for
// full-text message search.
//
// core depends on a storage interface, not on SQLite types, so the
// backend stays swappable. Writes are atomic. The schema and interface
// land in Phase 3.
package store
