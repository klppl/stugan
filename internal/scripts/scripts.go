// Package scripts ships built-in Lua plugins as embedded resources so the
// daemon can populate a fresh user's scripts directory on first run. The
// canonical copy of each bundled script lives next to this file; tests
// and the runtime read from here, never from docs/.
//
// Add to Builtins as more plugins ship. Each entry installs to the user's
// $STUGAN_HOME/scripts/<name> if (and only if) that file doesn't already
// exist, so a user customising or deleting a bundled script keeps their
// version across daemon restarts.
package scripts

import _ "embed"

//go:embed fish.lua
var Fish []byte

//go:embed ignore.lua
var Ignore []byte

// Builtins maps a script filename to its embedded contents. The hub uses
// this map to seed a user's scripts directory at startup.
var Builtins = map[string][]byte{
	"fish.lua":   Fish,
	"ignore.lua": Ignore,
}
