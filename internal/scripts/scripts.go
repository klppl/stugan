// Package scripts ships built-in Lua plugins as embedded resources so the
// daemon can populate a fresh user's scripts directory on first run. The
// canonical copy of each bundled script lives next to this file; tests
// and the runtime read from here, never from docs/.
//
// Builtins are copied into the user's $STUGAN_HOME/scripts/ only on first
// run, when that directory doesn't exist yet. After that the directory is
// the user's: edits and uninstalls stick across daemon restarts.
package scripts

import _ "embed"

//go:embed fish.lua
var Fish []byte

//go:embed ignore.lua
var Ignore []byte

//go:embed ai.lua
var AI []byte

//go:embed webhooks.lua
var Webhooks []byte

// Builtins maps a script filename to its embedded contents. The hub uses
// this map to seed a new user's scripts directory.
var Builtins = map[string][]byte{
	"fish.lua":     Fish,
	"ignore.lua":   Ignore,
	"ai.lua":       AI,
	"webhooks.lua": Webhooks,
}
