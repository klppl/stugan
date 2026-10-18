package previews

import (
	"context"
	"io"
	"net/url"
	"regexp"
	"strings"
)

// shorteners are link-shortening domains whose cards should describe the
// destination, not the redirector. Resolve follows the redirect chain (every
// hop through the SSRF guard) and runs the provider chain on the final URL.
var shorteners = []string{
	"bit.ly", "t.co", "tinyurl.com", "is.gd", "v.gd", "ow.ly", "buff.ly", "lnkd.in",
	"amzn.to", "amzn.eu", "a.co", "rb.gy", "cutt.ly", "shorturl.at", "tiny.cc", "t.ly",
	"s.id", "trib.al", "dlvr.it", "fb.me", "wp.me", "j.mp", "bl.ink", "rebrand.ly",
	"apple.co", "spoti.fi", "git.io", "pin.it", "lnk.to", "mzl.la", "goo.gl",
}

func isShortener(host string) bool {
	for _, d := range shorteners {
		if hostIs(host, d) {
			return true
		}
	}
	return false
}

var (
	metaRefresh = regexp.MustCompile(`(?is)<meta[^>]+http-equiv=["']?refresh["']?[^>]*?content=["'][^>]*?url=['"]?([^"'>\s]+)`)
	jsRedirect  = regexp.MustCompile(`(?is)location\.replace\(["']([^"']+)["']\)`)
)

// expand follows u's redirects and returns the final URL. t.co and a few
// others answer a browser with an HTML page carrying a meta refresh or a
// location.replace() instead of a 3xx, so a short, unredirected HTML reply is
// checked for those.
func expand(ctx context.Context, f *Fetcher, u *url.URL) (*url.URL, error) {
	resp, err := f.Get(ctx, u.String(), map[string]string{"Accept": "text/html,*/*"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	final := resp.Request.URL
	if final.Host != u.Host || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		return final, nil
	}
	head, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	for _, re := range []*regexp.Regexp{metaRefresh, jsRedirect} {
		if m := re.FindSubmatch(head); m != nil {
			if next, err := final.Parse(strings.TrimSpace(string(m[1]))); err == nil &&
				(next.Scheme == "http" || next.Scheme == "https") {
				return next, nil
			}
		}
	}
	return final, nil
}
