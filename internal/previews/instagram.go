package previews

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html/charset"
)

// instagram builds a card from the Open Graph tags Instagram serves to link
// unfurlers (the same ones Facebook and Discord see), so there is no login,
// token or official API involved. The tags pack the author, like and comment
// counts, date and caption into og:title / og:description; this unpacks them.
// Instagram can change that text, rate-limit or block server IPs at any time:
// a miss falls through to the generic Open Graph provider (a plain card).
type instagram struct{}

func newInstagram() instagram { return instagram{} }

func (instagram) Name() string { return "instagram" }

// igUA is the crawler identity Instagram serves metadata-rich pages to.
const igUA = "facebookexternalhit/1.1 (+http://www.facebook.com/externalhit_uatext.php)"

var igPath = regexp.MustCompile(`^/(?:[A-Za-z0-9._]+/)?(p|reels?|tv)/([A-Za-z0-9_-]+)`)

func igTarget(u *url.URL) (kind, code string, ok bool) {
	if !hostIs(u.Hostname(), "instagram.com") {
		return "", "", false
	}
	m := igPath.FindStringSubmatch(u.Path)
	if m == nil {
		return "", "", false
	}
	kind = m[1]
	if kind == "reels" {
		kind = "reel"
	}
	return kind, m[2], true
}

func (instagram) Match(u *url.URL) bool {
	_, _, ok := igTarget(u)
	return ok
}

func (instagram) Fetch(ctx context.Context, f *Fetcher, u *url.URL) (*Card, error) {
	kind, code, _ := igTarget(u)
	page := fmt.Sprintf("https://www.instagram.com/%s/%s/", kind, code)
	resp, err := f.Get(ctx, page, map[string]string{"User-Agent": igUA})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := charset.NewReader(io.LimitReader(resp.Body, maxHTMLBytes), resp.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	return cardFromInstagramOG(ParseOpenGraph(body, page), u.String())
}

var (
	igLikes    = regexp.MustCompile(`(?i)([\d][\d,.]*[km]?) likes?\b`)
	igComments = regexp.MustCompile(`(?i)([\d][\d,.]*[km]?) comments?\b`)
	igAuthor   = regexp.MustCompile(`- ([A-Za-z0-9._]+) on `)
	igDate     = regexp.MustCompile(`on ([A-Z][a-z]+ \d{1,2}, \d{4})`)
	igName     = regexp.MustCompile(`^(.*?) on Instagram:`)
)

// cardFromInstagramOG unpacks Instagram's OG text, e.g.
//
//	og:title:       Display Name on Instagram: "caption…"
//	og:description: 5,244 likes, 43 comments - user on August 24, 2026: "caption…"
func cardFromInstagramOG(og Card, pageURL string) (*Card, error) {
	desc := og.Description
	if !strings.Contains(desc, " - ") && !strings.Contains(og.Title, "on Instagram") {
		return nil, errors.New("no instagram metadata in page")
	}
	// Everything before the first `: "` is the stats header; the rest is the
	// caption, wrapped in quotes.
	head, caption := desc, ""
	if i := strings.Index(desc, `: "`); i >= 0 {
		head, caption = desc[:i], desc[i+3:]
		caption = strings.TrimRight(strings.TrimSpace(caption), `."`)
	}

	var user string
	if m := igAuthor.FindStringSubmatch(head); m != nil {
		user = m[1]
	}
	var name string
	if m := igName.FindStringSubmatch(og.Title); m != nil {
		name = strings.TrimSpace(m[1])
	}

	c := &Card{URL: pageURL, Image: og.Image, Site: "Instagram", Description: shorten(caption, 280)}
	switch {
	case name != "" && user != "" && !strings.EqualFold(name, user):
		c.Title = fmt.Sprintf("%s (@%s)", name, user)
	case user != "":
		c.Title = "@" + user
	case name != "":
		c.Title = name
	default:
		return nil, errors.New("no author in instagram metadata")
	}
	if m := igLikes.FindStringSubmatch(head); m != nil {
		c.Details = append(c.Details, m[1]+" likes")
	}
	if m := igComments.FindStringSubmatch(head); m != nil {
		c.Details = append(c.Details, m[1]+" comments")
	}
	if m := igDate.FindStringSubmatch(head); m != nil {
		if t, err := time.Parse("January 2, 2006", m[1]); err == nil {
			c.Details = append(c.Details, t.Format("2006-01-02"))
		}
	}
	return c, nil
}
