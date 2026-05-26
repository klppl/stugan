// Package irc owns all IRC protocol concerns and is the only package that
// imports the underlying IRC library (github.com/lrstanley/girc).
//
// Callers depend on the IRCConn interface, never on a concrete library
// type, so the implementation can be swapped for a custom IRCv3 core later
// without touching core/, server/, or plugin/. The interface and the girc
// implementation land in Phase 1.
package irc
