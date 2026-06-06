package server

import "github.com/klippelism/stugan/internal/safehttp"

// safeClient is an HTTP client for fetching user-supplied URLs (link previews,
// the image proxy). The SSRF guard lives in internal/safehttp, shared with the
// Lua plugin http binding.
var safeClient = safehttp.New()
