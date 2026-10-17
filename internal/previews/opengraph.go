package previews

import (
	"context"
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

// openGraph is the catch-all provider: fetch the page and read og:* tags,
// falling back to <title> and the description meta tag.
type openGraph struct{}

func (openGraph) Name() string        { return "opengraph" }
func (openGraph) Match(*url.URL) bool { return true }

func (openGraph) Fetch(ctx context.Context, f *Fetcher, u *url.URL) (*Card, error) {
	resp, err := f.Get(ctx, u.String(), map[string]string{"Accept": "text/html,application/xhtml+xml"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") && !strings.HasPrefix(ct, "application/xhtml") {
		return nil, ErrNotHTML
	}
	// Decode per the Content-Type header or <meta charset> so non-UTF-8 pages
	// don't come out garbled.
	body, err := charset.NewReader(io.LimitReader(resp.Body, maxHTMLBytes), ct)
	if err != nil {
		return nil, err
	}
	c := ParseOpenGraph(body, resp.Request.URL.String())
	return &c, nil
}

// ParseOpenGraph extracts og:* metadata (falling back to <title>) from an
// HTML document.
func ParseOpenGraph(r io.Reader, pageURL string) Card {
	p := Card{URL: pageURL}
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
