package previews

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// reddit resolves post links. With your own Reddit app credentials
// (STUGAN_PREVIEWS_REDDIT_CLIENT_ID, and _CLIENT_SECRET for script/web apps)
// it uses the OAuth API for score, comments, author and image. Without them it
// falls back to Reddit's keyless oEmbed endpoint: title and subreddit only,
// because anonymous .json access is blocked.
type reddit struct {
	clientID, secret, userAgent string

	// Endpoints, overridable in tests.
	tokenURL, apiBase, oembedURL string

	mu      sync.Mutex
	token   string
	expires time.Time
}

func newReddit(env Env) *reddit {
	ua := Setting(env, "reddit", "user_agent")
	if ua == "" {
		ua = "linux:stugan-previews:1.0 (link preview bot)"
	}
	return &reddit{
		clientID:  Setting(env, "reddit", "client_id"),
		secret:    Setting(env, "reddit", "client_secret"),
		userAgent: ua,
		tokenURL:  "https://www.reddit.com/api/v1/access_token",
		apiBase:   "https://oauth.reddit.com",
		oembedURL: "https://www.reddit.com/oembed",
	}
}

func (*reddit) Name() string { return "reddit" }

var redditPost = regexp.MustCompile(`/comments/([a-z0-9]+)`)

// redditPostID extracts the base36 post id from a reddit.com permalink or a
// redd.it short link.
func redditPostID(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "redd.it":
		id := strings.Trim(u.Path, "/")
		if id != "" && !strings.Contains(id, "/") {
			return id
		}
	case hostIs(host, "reddit.com"):
		if m := redditPost.FindStringSubmatch(u.Path); m != nil {
			return m[1]
		}
	}
	return ""
}

func (*reddit) Match(u *url.URL) bool { return redditPostID(u) != "" }

func (r *reddit) Fetch(ctx context.Context, f *Fetcher, u *url.URL) (*Card, error) {
	id := redditPostID(u)
	if r.clientID != "" {
		c, err := r.api(ctx, f, id)
		if err == nil {
			return c, nil
		}
		// A bad credential or a rate limit still yields a title via oEmbed.
		if c2, oerr := r.oEmbed(ctx, f, id, u); oerr == nil {
			return c2, nil
		}
		return nil, err
	}
	return r.oEmbed(ctx, f, id, u)
}

// accessToken returns a cached app-only token, fetching one when needed.
func (r *reddit) accessToken(ctx context.Context, f *Fetcher) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.token != "" && time.Now().Before(r.expires) {
		return r.token, nil
	}
	form := url.Values{}
	if r.secret != "" {
		form.Set("grant_type", "client_credentials")
	} else { // "installed app" type: no secret, per-device id
		form.Set("grant_type", "https://oauth.reddit.com/grants/installed_client")
		form.Set("device_id", "DO_NOT_TRACK_THIS_DEVICE")
	}
	resp, err := f.Do(ctx, "POST", r.tokenURL, strings.NewReader(form.Encode()), map[string]string{
		"User-Agent":    r.userAgent,
		"Content-Type":  "application/x-www-form-urlencoded",
		"Authorization": "Basic " + basicAuth(r.clientID, r.secret),
	})
	if err != nil {
		return "", redactKey(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("token request: status %d", resp.StatusCode)
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(limitBody(resp.Body)).Decode(&t); err != nil {
		return "", err
	}
	if t.AccessToken == "" {
		return "", fmt.Errorf("token request: %s", t.Error)
	}
	r.token = t.AccessToken
	// Refresh a minute early.
	r.expires = time.Now().Add(time.Duration(t.ExpiresIn)*time.Second - time.Minute)
	return r.token, nil
}

func (r *reddit) invalidate() {
	r.mu.Lock()
	r.token = ""
	r.mu.Unlock()
}

type redditPostData struct {
	Title       string  `json:"title"`
	Selftext    string  `json:"selftext"`
	Subreddit   string  `json:"subreddit_name_prefixed"`
	Author      string  `json:"author"`
	Score       int64   `json:"score"`
	UpvoteRatio float64 `json:"upvote_ratio"`
	NumComments int64   `json:"num_comments"`
	CreatedUTC  float64 `json:"created_utc"`
	Over18      bool    `json:"over_18"`
	Spoiler     bool    `json:"spoiler"`
	Thumbnail   string  `json:"thumbnail"`
	Permalink   string  `json:"permalink"`
	Preview     struct {
		Images []struct {
			Source struct {
				URL string `json:"url"`
			} `json:"source"`
		} `json:"images"`
	} `json:"preview"`
}

func (r *reddit) api(ctx context.Context, f *Fetcher, id string) (*Card, error) {
	tok, err := r.accessToken(ctx, f)
	if err != nil {
		return nil, err
	}
	resp, err := f.Get(ctx, r.apiBase+"/api/info?raw_json=1&id=t3_"+url.QueryEscape(id), map[string]string{
		"User-Agent":    r.userAgent,
		"Authorization": "Bearer " + tok,
		"Accept":        "application/json",
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		r.invalidate()
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("api: status %d", resp.StatusCode)
	}
	var out struct {
		Data struct {
			Children []struct {
				Data redditPostData `json:"data"`
			} `json:"children"`
		} `json:"data"`
	}
	if err := json.NewDecoder(limitBody(resp.Body)).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Data.Children) == 0 {
		return nil, errors.New("post not found")
	}
	return redditCard(out.Data.Children[0].Data), nil
}

func redditCard(p redditPostData) *Card {
	c := &Card{Title: p.Title, Site: "Reddit"}
	add := func(s string) {
		if s != "" {
			c.Details = append(c.Details, s)
		}
	}
	if p.Over18 {
		add("NSFW")
	}
	add(p.Subreddit)
	if p.Author != "" && p.Author != "[deleted]" {
		add("u/" + p.Author)
	}
	add(compactCount(p.Score) + " points")
	if p.UpvoteRatio > 0 {
		add(fmt.Sprintf("%d%% upvoted", int(p.UpvoteRatio*100+0.5)))
	}
	add(compactCount(p.NumComments) + " comments")
	if p.CreatedUTC > 0 {
		add(time.Unix(int64(p.CreatedUTC), 0).UTC().Format("2006-01-02"))
	}
	if p.Permalink != "" {
		c.URL = "https://www.reddit.com" + p.Permalink
	}
	// Don't surface previews of adult or spoiler content in a chat.
	if p.Over18 || p.Spoiler {
		return c
	}
	c.Description = shorten(p.Selftext, 240)
	if len(p.Preview.Images) > 0 {
		c.Image = p.Preview.Images[0].Source.URL
	} else if strings.HasPrefix(p.Thumbnail, "http") {
		c.Image = p.Thumbnail
	}
	return c
}

var redditSub = regexp.MustCompile(`href="https?://(?:www\.)?reddit\.com/(r/[A-Za-z0-9_]+)/?"`)

// oEmbed is the keyless fallback: the title and subreddit, nothing numeric.
func (r *reddit) oEmbed(ctx context.Context, f *Fetcher, id string, u *url.URL) (*Card, error) {
	// oEmbed wants the full permalink on www.reddit.com (old./np./m. and bare
	// /comments/<id> are rejected); redd.it links go through as-is.
	canon := u.String()
	if strings.EqualFold(u.Hostname(), "redd.it") {
		// Resolve the short link to its permalink by following the redirect.
		resp, err := f.Get(ctx, canon, nil)
		if err != nil {
			return nil, err
		}
		resp.Body.Close()
		u = resp.Request.URL
	}
	if hostIs(u.Hostname(), "reddit.com") {
		canon = "https://www.reddit.com" + u.EscapedPath()
	}
	body, err := f.GetJSON(ctx, r.oembedURL+"?url="+url.QueryEscape(canon))
	if err != nil {
		return nil, err
	}
	var o struct {
		Title string `json:"title"`
		HTML  string `json:"html"`
	}
	if err := json.Unmarshal(body, &o); err != nil {
		return nil, err
	}
	title := strings.TrimSpace(html.UnescapeString(o.Title))
	if title == "" {
		return nil, errors.New("oembed: no title")
	}
	c := &Card{URL: u.String(), Title: title, Site: "Reddit"}
	if m := redditSub.FindStringSubmatch(o.HTML); m != nil {
		c.Details = []string{m[1]}
	}
	return c, nil
}
