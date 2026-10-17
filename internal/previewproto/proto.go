// Package previewproto is the wire contract between stugan and the optional
// stugan-previews sidecar (docs/previews.md). It is deliberately tiny: two
// authenticated GET endpoints and one JSON type. Both sides import it; it
// imports only the standard library.
package previewproto

import "strings"

const (
	// PathPreview returns a Card for ?url=<absolute http(s) URL>.
	PathPreview = "/v1/preview"
	// PathProxy streams the image or video at ?url=<absolute http(s) URL>.
	PathProxy = "/v1/proxy"
	// PathHealth answers 200 without authentication (container healthchecks).
	PathHealth = "/healthz"

	// MinSecretLen is the shortest accepted shared secret.
	MinSecretLen = 16

	// DefaultListen is the sidecar's default bind address.
	DefaultListen = "127.0.0.1:8017"
)

// Card is the link-preview summary the web client renders. The first four
// fields are the original Open Graph shape; Site and Details are optional
// extras a site-specific provider can fill in.
type Card struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Image       string `json:"image"`
	// Site is a display name for the source ("YouTube"). Empty means the
	// client falls back to the URL's host.
	Site string `json:"site,omitempty"`
	// Details are short facts shown on one line under the title:
	// channel, duration, view count and so on.
	Details []string `json:"details,omitempty"`
}

// Empty reports whether the card carries nothing worth showing.
func (c Card) Empty() bool {
	return c.Title == "" && c.Description == "" && c.Image == ""
}

// NormalizeURL turns a configured sidecar address into a base URL: a bare
// host:port gets http://, and trailing slashes are dropped. It does not
// validate; callers parse the result.
func NormalizeURL(s string) string {
	s = strings.TrimSpace(s)
	if s != "" && !strings.Contains(s, "://") {
		s = "http://" + s
	}
	return strings.TrimRight(s, "/")
}
