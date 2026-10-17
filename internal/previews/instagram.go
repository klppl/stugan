package previews

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// instagram scrapes the public embed page of a post, reel or IGTV video:
// no login, token or official API. Instagram can change that markup, rate-
// limit or block server IPs at any time, so every field is best-effort and a
// miss falls through to the generic Open Graph provider (usually a login
// wall, i.e. a plain link).
type instagram struct{}

func newInstagram() instagram { return instagram{} }

func (instagram) Name() string { return "instagram" }

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

var (
	igLikes    = regexp.MustCompile(`(?i)>\s*(\d[\d,.]*)\s*([km])?\s*likes?\s*<`)
	igUser     = regexp.MustCompile(`(?s)<a[^>]*class="[^"]*CaptionUsername[^"]*"[^>]*>(.*?)</a>`)
	igUserJSON = regexp.MustCompile(`\\?"username\\?":\s*\\?"([A-Za-z0-9._]+)\\?"`)
	igCaption  = regexp.MustCompile(`(?s)<div class="Caption"[^>]*>(.*?)</div>`)
	igImage    = regexp.MustCompile(`(?s)<img[^>]*class="[^"]*EmbeddedMediaImage[^"]*"[^>]*src="([^"]+)"`)
	igBR       = regexp.MustCompile(`(?i)<br\s*/?>`)
	igTag      = regexp.MustCompile(`<[^>]+>`)
	igMoreCmts = regexp.MustCompile(`(?s)View all [\d,.]+ comments.*$`)
)

func (instagram) Fetch(ctx context.Context, f *Fetcher, u *url.URL) (*Card, error) {
	kind, code, _ := igTarget(u)
	embed := fmt.Sprintf("https://www.instagram.com/%s/%s/embed/captioned/", kind, code)
	resp, err := f.Get(ctx, embed, map[string]string{"Referer": "https://www.instagram.com/"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	buf := make([]byte, 0, 64<<10)
	tmp := make([]byte, 32<<10)
	for len(buf) < maxHTMLBytes {
		n, rerr := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if rerr != nil {
			break
		}
	}
	return parseInstagramEmbed(string(buf), u.String())
}

func parseInstagramEmbed(doc, pageURL string) (*Card, error) {
	var user string
	if m := igUser.FindStringSubmatch(doc); m != nil {
		user = cleanHTML(m[1])
	} else if m := igUserJSON.FindStringSubmatch(doc); m != nil {
		user = m[1]
	}
	var caption string
	if m := igCaption.FindStringSubmatch(doc); m != nil {
		caption = igMoreCmts.ReplaceAllString(m[1], "")
		caption = igBR.ReplaceAllString(caption, "\n")
		caption = cleanHTML(caption)
		// The caption div starts with the username link; drop the repeat.
		caption = strings.TrimSpace(strings.TrimPrefix(caption, user))
	}
	var image string
	if m := igImage.FindStringSubmatch(doc); m != nil {
		image = html.UnescapeString(m[1])
	}
	if user == "" && caption == "" && image == "" {
		return nil, errors.New("embed page had no recognizable fields")
	}
	c := &Card{URL: pageURL, Description: shorten(caption, 280), Image: image, Site: "Instagram"}
	if user != "" {
		c.Title = "@" + strings.TrimPrefix(user, "@")
	} else {
		c.Title = "Instagram post"
	}
	if m := igLikes.FindStringSubmatch(doc); m != nil {
		if n, ok := parseLikes(m[1], m[2]); ok {
			c.Details = append(c.Details, compactCount(n)+" likes")
		}
	}
	return c, nil
}

func cleanHTML(s string) string {
	s = igTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.Join(strings.Fields(l), " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// parseLikes turns "1,234" or "1.2" with a k/m suffix into a count.
func parseLikes(num, suffix string) (int64, bool) {
	num = strings.ReplaceAll(num, ",", "")
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, false
	}
	switch strings.ToLower(suffix) {
	case "k":
		f *= 1e3
	case "m":
		f *= 1e6
	}
	return int64(f), true
}
