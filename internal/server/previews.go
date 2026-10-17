package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/klippelism/stugan/internal/previews"
)

const maxPreviewBytes = 512 << 10 // HTML we'll parse for OG tags

// preview is the link-preview card returned to the client.
type preview = previews.Card

// handlePreview returns link-preview metadata for a URL, from the
// stugan-previews sidecar when configured, otherwise by fetching in-process.
// GET /api/preview?url=<encoded>
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	target, ok := validTarget(r.URL.Query().Get("url"))
	if !ok {
		http.Error(w, "bad url", http.StatusBadRequest)
		return
	}
	if s.previews != nil {
		s.previews.preview(w, r, target)
		return
	}
	resp, err := safeClient.Get(target)
	if err != nil {
		http.Error(w, "fetch failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		http.Error(w, "not html", http.StatusUnsupportedMediaType)
		return
	}

	p := parseOpenGraph(io.LimitReader(resp.Body, maxPreviewBytes), target)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_ = json.NewEncoder(w).Encode(p)
}

// handleProxy streams a remote image back to the browser, avoiding
// mixed-content and hiding the client IP. GET /api/proxy?url=<encoded>
func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	target, ok := validTarget(r.URL.Query().Get("url"))
	if !ok {
		http.Error(w, "bad url", http.StatusBadRequest)
		return
	}
	if s.previews != nil {
		s.previews.proxy(w, r, target)
		return
	}
	previews.ServeMedia(w, safeClient, target)
}

// validTarget accepts only absolute http(s) URLs.
func validTarget(raw string) (string, bool) { return previews.ValidTarget(raw) }

// parseOpenGraph extracts og:* metadata (falling back to <title>) from an
// HTML document.
func parseOpenGraph(r io.Reader, pageURL string) preview {
	return previews.ParseOpenGraph(r, pageURL)
}
