package tui

import "time"

const (
	// shutdownGrace bounds how long ListenAndServe waits for in-flight SSH
	// sessions to drain on shutdown.
	shutdownGrace = 5 * time.Second

	// backlogPage is how many history lines a buffer loads per fetch.
	backlogPage = 200

	// sidebarWidth / membersWidth are the fixed side-column widths; the
	// message pane takes the rest.
	sidebarWidth = 22
	membersWidth = 18

	// minWidth / minHeight is the smallest terminal the layout targets.
	minWidth  = 40
	minHeight = 10
)
