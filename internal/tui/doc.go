// Package tui serves stugan's terminal UI over SSH. It is the only package
// that imports the SSH server (github.com/charmbracelet/wish) and the
// Bubble Tea/Lip Gloss TUI toolkit; like internal/irc with girc, those
// libraries never leak past this package. Callers depend on *core.Engine and
// the small History interface defined here, so the transport can be swapped
// without touching core/.
//
// One Sink is registered per user at startup (before the engine runs, like
// the web server's sink). Each live SSH session attaches to that user's Sink
// through a session registry, and committed engine lines fan out to every
// attached Bubble Tea program via Program.Send. Sessions come and go while
// the single per-user Sink stays put, so the engine's sink slice is never
// mutated after start.
package tui
