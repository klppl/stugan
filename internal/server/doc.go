// Package server hosts the HTTP and WebSocket endpoints and bridges core
// events to and from browser sockets.
//
// It owns session/auth, the typed event router (encoding/decoding
// proto structs), the file-upload endpoint, the link-preview fetcher and
// image proxy, and the web-push sender. It depends on core only through
// the event bus and public core types — never the reverse. The minimal
// WebSocket server and router land in Phase 2.
package server
