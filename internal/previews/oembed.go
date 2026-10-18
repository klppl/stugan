package previews

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// oEmbedSite is one site with a keyless oEmbed endpoint.
type oEmbedSite struct {
	name     string
	domains  []string
	endpoint string // takes ?url=…&format=json
}

// oEmbedSites are services that publish an open oEmbed endpoint. Adding one is
// a line here; their cards are title, author, thumbnail and (where given) a
// duration. A site whose endpoint rejects a URL falls through to Open Graph.
var oEmbedSites = []oEmbedSite{
	{"Vimeo", []string{"vimeo.com"}, "https://vimeo.com/api/oembed.json"},
	{"SoundCloud", []string{"soundcloud.com"}, "https://soundcloud.com/oembed"},
	{"Spotify", []string{"open.spotify.com"}, "https://open.spotify.com/oembed"},
	{"TikTok", []string{"tiktok.com"}, "https://www.tiktok.com/oembed"},
	{"Dailymotion", []string{"dailymotion.com"}, "https://www.dailymotion.com/services/oembed"},
	{"Flickr", []string{"flickr.com", "flic.kr"}, "https://www.flickr.com/services/oembed"},
	{"Mixcloud", []string{"mixcloud.com"}, "https://app.mixcloud.com/oembed/"},
	{"Giphy", []string{"giphy.com"}, "https://giphy.com/services/oembed"},
}

type oEmbed struct {
	sites []oEmbedSite // overridable in tests
}

func newOEmbed() *oEmbed { return &oEmbed{sites: oEmbedSites} }

func (*oEmbed) Name() string { return "oembed" }

func (o *oEmbed) site(u *url.URL) *oEmbedSite {
	host := strings.ToLower(u.Hostname())
	for i := range o.sites {
		for _, d := range o.sites[i].domains {
			if hostIs(host, d) {
				return &o.sites[i]
			}
		}
	}
	return nil
}

func (o *oEmbed) Match(u *url.URL) bool { return o.site(u) != nil }

func (o *oEmbed) Fetch(ctx context.Context, f *Fetcher, u *url.URL) (*Card, error) {
	s := o.site(u)
	var r struct {
		Title        string  `json:"title"`
		AuthorName   string  `json:"author_name"`
		ProviderName string  `json:"provider_name"`
		Description  string  `json:"description"`
		ThumbnailURL string  `json:"thumbnail_url"`
		Duration     float64 `json:"duration"`
	}
	q := url.Values{"url": {u.String()}, "format": {"json"}}
	if err := f.JSON(ctx, s.endpoint+"?"+q.Encode(), nil, &r); err != nil {
		return nil, err
	}
	c := &Card{URL: u.String(), Title: r.Title, Description: shorten(r.Description, 240), Image: r.ThumbnailURL, Site: s.name}
	add := detailAdder(c)
	add(r.AuthorName)
	if r.Duration > 0 {
		secs := int(r.Duration)
		if secs >= 3600 {
			add(fmt.Sprintf("%d:%02d:%02d", secs/3600, secs%3600/60, secs%60))
		} else {
			add(fmt.Sprintf("%d:%02d", secs/60, secs%60))
		}
	}
	return c, nil
}
