package previews

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

// wikipedia summarizes article links from Wikipedia's keyless REST summary
// endpoint: the lead paragraph, short description and thumbnail.
type wikipedia struct {
	apiFor func(host string) string // overridable in tests
}

func newWikipedia() *wikipedia {
	return &wikipedia{apiFor: func(host string) string { return "https://" + host + "/api/rest_v1" }}
}

func (*wikipedia) Name() string { return "wikipedia" }

// wikiTitle returns the escaped article title of a /wiki/<Title> URL, or "".
func wikiTitle(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	if !strings.HasSuffix(host, ".wikipedia.org") || host == "www.wikipedia.org" {
		return ""
	}
	p, ok := strings.CutPrefix(u.EscapedPath(), "/wiki/")
	if !ok || p == "" {
		return ""
	}
	// Special:, Talk:, Category: and friends have no useful summary.
	if raw, _ := url.PathUnescape(p); strings.HasPrefix(raw, "Special:") || strings.HasPrefix(raw, "Talk:") {
		return ""
	}
	return p
}

func (*wikipedia) Match(u *url.URL) bool { return wikiTitle(u) != "" }

func (w *wikipedia) Fetch(ctx context.Context, f *Fetcher, u *url.URL) (*Card, error) {
	var s struct {
		Type        string `json:"type"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Extract     string `json:"extract"`
		Thumbnail   struct {
			Source string `json:"source"`
		} `json:"thumbnail"`
		ContentURLs struct {
			Desktop struct {
				Page string `json:"page"`
			} `json:"desktop"`
		} `json:"content_urls"`
	}
	host := strings.ToLower(u.Hostname())
	if err := f.JSON(ctx, w.apiFor(host)+"/page/summary/"+wikiTitle(u), map[string]string{"User-Agent": apiUA}, &s); err != nil {
		return nil, err
	}
	if s.Title == "" || s.Extract == "" {
		return nil, errors.New("no summary")
	}
	c := &Card{
		URL: s.ContentURLs.Desktop.Page, Title: s.Title, Description: shorten(s.Extract, 320),
		Image: s.Thumbnail.Source, Site: "Wikipedia",
	}
	if c.URL == "" {
		c.URL = u.String()
	}
	if s.Type == "disambiguation" {
		c.Details = []string{"disambiguation page"}
	} else if s.Description != "" {
		c.Details = []string{s.Description}
	}
	return c, nil
}
