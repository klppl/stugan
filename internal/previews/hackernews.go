package previews

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// hackerNews summarizes news.ycombinator.com/item?id=… pages from the keyless
// Firebase API: points, comment count and the linked domain.
type hackerNews struct {
	api string // overridable in tests
}

func newHackerNews() *hackerNews {
	return &hackerNews{api: "https://hacker-news.firebaseio.com/v0"}
}

func (*hackerNews) Name() string { return "hackernews" }

func hnID(u *url.URL) string {
	if !strings.EqualFold(u.Hostname(), "news.ycombinator.com") || u.Path != "/item" {
		return ""
	}
	if id := u.Query().Get("id"); isDigits(id) {
		return id
	}
	return ""
}

func (*hackerNews) Match(u *url.URL) bool { return hnID(u) != "" }

var (
	hnBreak = regexp.MustCompile(`(?i)<p>|<br\s*/?>`)
	hnTag   = regexp.MustCompile(`<[^>]+>`)
)

func (h *hackerNews) Fetch(ctx context.Context, f *Fetcher, u *url.URL) (*Card, error) {
	id := hnID(u)
	var it struct {
		Type        string `json:"type"`
		Title       string `json:"title"`
		By          string `json:"by"`
		Text        string `json:"text"`
		URL         string `json:"url"`
		Score       int64  `json:"score"`
		Descendants int64  `json:"descendants"`
		Time        int64  `json:"time"`
		Dead        bool   `json:"dead"`
		Deleted     bool   `json:"deleted"`
	}
	if err := f.JSON(ctx, h.api+"/item/"+id+".json", nil, &it); err != nil {
		return nil, err
	}
	if it.Deleted || it.Dead || (it.Title == "" && it.Text == "") {
		return nil, errors.New("item unavailable")
	}
	text := html.UnescapeString(hnTag.ReplaceAllString(hnBreak.ReplaceAllString(it.Text, " "), ""))
	c := &Card{URL: u.String(), Title: it.Title, Description: shorten(text, 240), Site: "Hacker News"}
	if it.Title == "" { // a comment
		c.Title = "Comment by " + it.By
		c.Description = shorten(text, 280)
	}
	add := detailAdder(c)
	if it.Type == "story" || it.Type == "poll" {
		add(compactCount(it.Score) + " points")
	}
	if it.By != "" && it.Title != "" {
		add("by " + it.By)
	}
	if it.Type == "story" {
		add(fmt.Sprintf("%s comments", compactCount(it.Descendants)))
	}
	if it.URL != "" {
		if lu, err := url.Parse(it.URL); err == nil {
			add(strings.TrimPrefix(lu.Hostname(), "www."))
		}
	}
	if it.Time > 0 {
		add(time.Unix(it.Time, 0).UTC().Format("2006-01-02"))
	}
	return c, nil
}
