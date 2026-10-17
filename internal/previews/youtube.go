package previews

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// youTube resolves video and playlist links. With
// STUGAN_PREVIEWS_YOUTUBE_API_KEY set it uses the YouTube Data API v3 (title,
// channel, duration, views, likes, date); without a key it falls back to the
// keyless oEmbed endpoint (title, channel, thumbnail).
type youTube struct {
	apiKey string
}

func newYouTube(env Env) *youTube {
	return &youTube{apiKey: Setting(env, "youtube", "api_key")}
}

func (*youTube) Name() string { return "youtube" }

var (
	ytVideoID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	// System playlists (Watch Later, Liked, Favourites, Mix) aren't public.
	ytSystemLists = map[string]bool{"WL": true, "LL": true, "FL": true, "LM": true}
)

// ytTarget extracts a video or playlist id from a YouTube URL.
func ytTarget(u *url.URL) (video, playlist string) {
	host := strings.ToLower(u.Hostname())
	q := u.Query()
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch {
	case host == "youtu.be":
		if len(parts) > 0 && ytVideoID.MatchString(parts[0]) {
			video = parts[0]
		}
	case hostIs(host, "youtube.com"), hostIs(host, "youtube-nocookie.com"):
		if id := q.Get("v"); ytVideoID.MatchString(id) {
			video = id
		} else if len(parts) >= 2 {
			switch parts[0] {
			case "shorts", "live", "embed", "v":
				if ytVideoID.MatchString(parts[1]) {
					video = parts[1]
				}
			}
		}
		if video == "" {
			if l := q.Get("list"); l != "" && !ytSystemLists[l] && (len(parts) == 0 || parts[0] == "playlist") {
				playlist = l
			}
		}
	}
	return video, playlist
}

func (y *youTube) Match(u *url.URL) bool {
	v, p := ytTarget(u)
	return v != "" || p != ""
}

func (y *youTube) Fetch(ctx context.Context, f *Fetcher, u *url.URL) (*Card, error) {
	video, playlist := ytTarget(u)
	if y.apiKey != "" {
		if video != "" {
			return y.apiVideo(ctx, f, video)
		}
		return y.apiPlaylist(ctx, f, playlist)
	}
	return y.oEmbed(ctx, f, u, video, playlist)
}

type ytThumbs map[string]struct {
	URL string `json:"url"`
}

func (t ytThumbs) best() string {
	for _, k := range []string{"maxres", "standard", "high", "medium", "default"} {
		if v, ok := t[k]; ok && v.URL != "" {
			return v.URL
		}
	}
	return ""
}

func (y *youTube) apiVideo(ctx context.Context, f *Fetcher, id string) (*Card, error) {
	q := url.Values{"part": {"snippet,contentDetails,statistics"}, "id": {id}, "key": {y.apiKey}}
	body, err := f.GetJSON(ctx, "https://www.googleapis.com/youtube/v3/videos?"+q.Encode())
	if err != nil {
		return nil, redactKey(err)
	}
	var r struct {
		Items []struct {
			Snippet struct {
				Title        string   `json:"title"`
				Description  string   `json:"description"`
				ChannelTitle string   `json:"channelTitle"`
				PublishedAt  string   `json:"publishedAt"`
				Thumbnails   ytThumbs `json:"thumbnails"`
			} `json:"snippet"`
			ContentDetails struct {
				Duration string `json:"duration"`
			} `json:"contentDetails"`
			Statistics struct {
				ViewCount string `json:"viewCount"`
				LikeCount string `json:"likeCount"`
			} `json:"statistics"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if len(r.Items) == 0 {
		return nil, errors.New("video not found")
	}
	it := r.Items[0]
	c := &Card{
		URL:         "https://www.youtube.com/watch?v=" + id,
		Title:       it.Snippet.Title,
		Description: shorten(it.Snippet.Description, 200),
		Image:       it.Snippet.Thumbnails.best(),
		Site:        "YouTube",
	}
	add := func(s string) {
		if s != "" {
			c.Details = append(c.Details, s)
		}
	}
	add(it.Snippet.ChannelTitle)
	add(formatISODuration(it.ContentDetails.Duration))
	if n, ok := atoi(it.Statistics.ViewCount); ok {
		add(compactCount(n) + " views")
	}
	if n, ok := atoi(it.Statistics.LikeCount); ok {
		add(compactCount(n) + " likes")
	}
	if t, err := time.Parse(time.RFC3339, it.Snippet.PublishedAt); err == nil {
		add(t.Format("2006-01-02"))
	}
	return c, nil
}

func (y *youTube) apiPlaylist(ctx context.Context, f *Fetcher, id string) (*Card, error) {
	q := url.Values{"part": {"snippet,contentDetails"}, "id": {id}, "key": {y.apiKey}}
	body, err := f.GetJSON(ctx, "https://www.googleapis.com/youtube/v3/playlists?"+q.Encode())
	if err != nil {
		return nil, redactKey(err)
	}
	var r struct {
		Items []struct {
			Snippet struct {
				Title        string   `json:"title"`
				Description  string   `json:"description"`
				ChannelTitle string   `json:"channelTitle"`
				Thumbnails   ytThumbs `json:"thumbnails"`
			} `json:"snippet"`
			ContentDetails struct {
				ItemCount int `json:"itemCount"`
			} `json:"contentDetails"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if len(r.Items) == 0 {
		return nil, errors.New("playlist not found")
	}
	it := r.Items[0]
	c := &Card{
		URL:         "https://www.youtube.com/playlist?list=" + id,
		Title:       it.Snippet.Title,
		Description: shorten(it.Snippet.Description, 200),
		Image:       it.Snippet.Thumbnails.best(),
		Site:        "YouTube playlist",
	}
	if it.Snippet.ChannelTitle != "" {
		c.Details = append(c.Details, it.Snippet.ChannelTitle)
	}
	if it.ContentDetails.ItemCount > 0 {
		c.Details = append(c.Details, fmt.Sprintf("%d videos", it.ContentDetails.ItemCount))
	}
	return c, nil
}

func (y *youTube) oEmbed(ctx context.Context, f *Fetcher, u *url.URL, video, playlist string) (*Card, error) {
	canon := "https://www.youtube.com/watch?v=" + video
	if video == "" {
		canon = "https://www.youtube.com/playlist?list=" + playlist
	}
	body, err := f.GetJSON(ctx, "https://www.youtube.com/oembed?format=json&url="+url.QueryEscape(canon))
	if err != nil {
		return nil, err
	}
	var r struct {
		Title        string `json:"title"`
		AuthorName   string `json:"author_name"`
		ThumbnailURL string `json:"thumbnail_url"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	c := &Card{URL: canon, Title: r.Title, Image: r.ThumbnailURL, Site: "YouTube"}
	if r.AuthorName != "" {
		c.Details = []string{r.AuthorName}
	}
	return c, nil
}

// redactKey keeps the API key out of logs: net/http errors embed the URL.
func redactKey(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

var isoDuration = regexp.MustCompile(`^P(?:(\d+)D)?T?(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?$`)

// formatISODuration renders "PT1H2M3S" as "1:02:03" and "PT45S" as "0:45".
func formatISODuration(s string) string {
	m := isoDuration.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	n := func(i int) int { v, _ := strconv.Atoi(m[i]); return v }
	h := n(1)*24 + n(2)
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, n(3), n(4))
	}
	return fmt.Sprintf("%d:%02d", n(3), n(4))
}

func atoi(s string) (int64, bool) {
	v, err := strconv.ParseInt(s, 10, 64)
	return v, err == nil
}

// compactCount renders 1234 as "1.2K", 3400000 as "3.4M".
func compactCount(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return trimZero(float64(n)/1e9) + "B"
	case n >= 1_000_000:
		return trimZero(float64(n)/1e6) + "M"
	case n >= 10_000:
		return trimZero(float64(n)/1e3) + "K"
	}
	return strconv.FormatInt(n, 10)
}

func trimZero(f float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(f, 'f', 1, 64), ".0")
}

// shorten collapses whitespace and cuts s to max runes with an ellipsis.
func shorten(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max-1])) + "…"
}
