package previews

import (
	"io"
	"net/http"
	"strings"
)

// ServeMedia streams the image or video at target back to w, hiding the
// client's IP and avoiding mixed-content warnings. It is shared by the
// sidecar and by stugan's in-process fallback.
func ServeMedia(w http.ResponseWriter, client *http.Client, target string) {
	resp, err := client.Get(target)
	if err != nil {
		http.Error(w, "fetch failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") && !strings.HasPrefix(ct, "video/") {
		http.Error(w, "not an image", http.StatusUnsupportedMediaType)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	// The proxy reflects attacker-controlled bytes from a remote origin back
	// on *our* origin. A remote server can return Content-Type: image/svg+xml
	// (which passes the image/ prefix check) carrying inline <script>; opened
	// as a top-level document that would execute same-origin. <img>/<video>
	// embedding ignores these headers, so they cost nothing for the real use
	// while neutralising direct-navigation: nosniff pins the declared type and
	// the sandbox CSP blocks script execution in any document context.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	_, _ = io.Copy(w, io.LimitReader(resp.Body, maxImageBytes))
}
