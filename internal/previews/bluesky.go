package previews

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// bluesky summarizes bsky.app post links from the public AppView API (no key):
// the text, author, like/repost/reply counts and the first attached image.
type bluesky struct {
	api string // overridable in tests
}

func newBluesky() *bluesky { return &bluesky{api: "https://public.api.bsky.app"} }

func (*bluesky) Name() string { return "bluesky" }

var bskyPath = regexp.MustCompile(`^/profile/([^/]+)/post/([A-Za-z0-9]+)/?$`)

func bskyTarget(u *url.URL) (actor, rkey string, ok bool) {
	if !strings.EqualFold(u.Hostname(), "bsky.app") {
		return "", "", false
	}
	m := bskyPath.FindStringSubmatch(u.Path)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

func (*bluesky) Match(u *url.URL) bool { _, _, ok := bskyTarget(u); return ok }

func (b *bluesky) Fetch(ctx context.Context, f *Fetcher, u *url.URL) (*Card, error) {
	actor, rkey, _ := bskyTarget(u)
	q := url.Values{
		"uri":          {"at://" + actor + "/app.bsky.feed.post/" + rkey},
		"depth":        {"0"},
		"parentHeight": {"0"},
	}
	var r struct {
		Thread struct {
			Post struct {
				Author struct {
					Handle      string `json:"handle"`
					DisplayName string `json:"displayName"`
				} `json:"author"`
				Record struct {
					Text      string `json:"text"`
					CreatedAt string `json:"createdAt"`
				} `json:"record"`
				Embed struct {
					Images []struct {
						Thumb    string `json:"thumb"`
						Fullsize string `json:"fullsize"`
					} `json:"images"`
					External struct {
						Thumb string `json:"thumb"`
					} `json:"external"`
					Media struct { // record-with-media quotes
						Images []struct {
							Thumb string `json:"thumb"`
						} `json:"images"`
					} `json:"media"`
				} `json:"embed"`
				LikeCount   int64 `json:"likeCount"`
				RepostCount int64 `json:"repostCount"`
				ReplyCount  int64 `json:"replyCount"`
			} `json:"post"`
		} `json:"thread"`
	}
	if err := f.JSON(ctx, b.api+"/xrpc/app.bsky.feed.getPostThread?"+q.Encode(), nil, &r); err != nil {
		return nil, err
	}
	p := r.Thread.Post
	if p.Author.Handle == "" {
		return nil, errors.New("post not found")
	}
	title := "@" + p.Author.Handle
	if p.Author.DisplayName != "" {
		title = p.Author.DisplayName + " (@" + p.Author.Handle + ")"
	}
	c := &Card{URL: u.String(), Title: title, Description: shorten(p.Record.Text, 300), Site: "Bluesky"}
	switch {
	case len(p.Embed.Images) > 0:
		c.Image = p.Embed.Images[0].Thumb
	case p.Embed.External.Thumb != "":
		c.Image = p.Embed.External.Thumb
	case len(p.Embed.Media.Images) > 0:
		c.Image = p.Embed.Media.Images[0].Thumb
	}
	add := detailAdder(c)
	add(compactCount(p.LikeCount) + " likes")
	add(compactCount(p.RepostCount) + " reposts")
	add(compactCount(p.ReplyCount) + " replies")
	add(isoDate(p.Record.CreatedAt))
	return c, nil
}
