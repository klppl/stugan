package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

const (
	maxPreviewBytes = 512 << 10 // HTML we'll parse for OG tags
	maxImageBytes   = 10 << 20  // image-proxy size cap
)

// preview is the Open Graph summary returned to the client.
type preview struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Image       string `json:"image"`
}

// handlePreview fetches a URL and extracts Open Graph / <title> metadata.
// GET /api/preview?url=<encoded>
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	target, ok := validTarget(r.URL.Query().Get("url"))
	if !ok {
		http.Error(w, "bad url", http.StatusBadRequest)
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
	resp, err := safeClient.Get(target)
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
	_, _ = io.Copy(w, io.LimitReader(resp.Body, maxImageBytes))
}

// validTarget accepts only absolute http(s) URLs.
func validTarget(raw string) (string, bool) {
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", false
	}
	return u.String(), true
}

// parseOpenGraph extracts og:* metadata (falling back to <title>) from an
// HTML document.
func parseOpenGraph(r io.Reader, pageURL string) preview {
	p := preview{URL: pageURL}
	z := html.NewTokenizer(r)
	inTitle := false
	for {
		switch z.Next() {
		case html.ErrorToken:
			return p
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			switch t.Data {
			case "meta":
				prop, content := "", ""
				for _, a := range t.Attr {
					switch a.Key {
					case "property", "name":
						prop = a.Val
					case "content":
						content = a.Val
					}
				}
				switch prop {
				case "og:title":
					p.Title = content
				case "og:description", "description":
					if p.Description == "" {
						p.Description = content
					}
				case "og:image", "og:image:url":
					p.Image = content
				}
			case "title":
				inTitle = true
			case "body":
				// OG/description live in <head>; stop once the body starts.
				if p.Title != "" || p.Description != "" {
					return p
				}
			}
		case html.TextToken:
			if inTitle && p.Title == "" {
				p.Title = strings.TrimSpace(z.Token().Data)
			}
		case html.EndTagToken:
			if z.Token().Data == "title" {
				inTitle = false
			}
		}
	}
}
