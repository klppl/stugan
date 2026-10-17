package previews

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/klippelism/stugan/internal/safehttp"
)

const (
	maxHTMLBytes  = 512 << 10 // HTML we'll parse for metadata
	maxJSONBytes  = 1 << 20   // provider API responses
	maxImageBytes = 10 << 20  // media-proxy size cap

	// browserUA is sent to sites that serve a login wall or an empty shell to
	// anything that doesn't look like a browser.
	browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
)

// Fetcher is the SSRF-guarded HTTP client every provider fetches through. The
// guard (internal/safehttp) refuses non-public addresses on every dial,
// including redirect hops.
type Fetcher struct {
	Client *http.Client
}

// NewFetcher returns a Fetcher over a fresh safehttp client.
func NewFetcher() *Fetcher { return &Fetcher{Client: safehttp.New()} }

// Get issues a GET with the given headers (a browser User-Agent is the
// default). The caller closes the body.
func (f *Fetcher) Get(ctx context.Context, rawURL string, header map[string]string) (*http.Response, error) {
	return f.Do(ctx, http.MethodGet, rawURL, nil, header)
}

// Do is Get with a method and body, for APIs that need a token exchange.
func (f *Fetcher) Do(ctx context.Context, method, rawURL string, body io.Reader, header map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	return f.Client.Do(req)
}

// GetJSON fetches an API URL and returns its (size-capped) body, failing on a
// non-2xx status.
func (f *Fetcher) GetJSON(ctx context.Context, rawURL string) ([]byte, error) {
	resp, err := f.Get(ctx, rawURL, map[string]string{"Accept": "application/json"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxJSONBytes))
}

// ValidTarget accepts only absolute http(s) URLs.
func ValidTarget(raw string) (string, bool) {
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", false
	}
	return u.String(), true
}

// hostIs reports whether host equals domain or is a subdomain of it.
func hostIs(host, domain string) bool {
	host = strings.ToLower(host)
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// limitBody caps an API response body.
func limitBody(r io.Reader) io.Reader { return io.LimitReader(r, maxJSONBytes) }

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}
