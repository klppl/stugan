package previews

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRedditPostID(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.reddit.com/r/golang/comments/1b1qlr2/some_title/": "1b1qlr2",
		"https://old.reddit.com/r/golang/comments/abc123/x/def456/":    "abc123",
		"https://reddit.com/comments/abc123":                           "abc123",
		"https://redd.it/abc123":                                       "abc123",
		"https://www.reddit.com/r/golang/":                             "",
		"https://www.reddit.com/user/someone/":                         "",
		"https://notreddit.com/r/golang/comments/abc123/":              "",
	} {
		u, _ := url.Parse(in)
		if got := redditPostID(u); got != want {
			t.Errorf("redditPostID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRedditCardNSFWHidesContent(t *testing.T) {
	p := redditPostData{Title: "t", Over18: true, Selftext: "secret", Thumbnail: "https://x/y.jpg", Subreddit: "r/x", Score: 5}
	c := redditCard(p)
	if c.Description != "" || c.Image != "" {
		t.Errorf("NSFW card leaked content: %+v", c)
	}
	if c.Details[0] != "NSFW" {
		t.Errorf("details = %v", c.Details)
	}
}

func TestRedditAPIAndOEmbed(t *testing.T) {
	var tokenCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		tokenCalls.Add(1)
		user, pass, _ := r.BasicAuth()
		r.ParseForm()
		if user != "cid" || pass != "sec" || r.PostForm.Get("grant_type") != "client_credentials" {
			http.Error(w, "bad", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"access_token":"tok","expires_in":86400}`))
	})
	mux.HandleFunc("/api/info", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Query().Get("id") != "t3_abc123" {
			http.Error(w, "bad", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":{"children":[{"data":{"title":"A post","selftext":"body text","subreddit_name_prefixed":"r/golang","author":"gopher","score":12345,"upvote_ratio":0.97,"num_comments":321,"created_utc":1700000000,"permalink":"/r/golang/comments/abc123/a_post/","preview":{"images":[{"source":{"url":"https://i.redd.it/x.jpg?a=1&b=2"}}]}}}]}}`))
	})
	mux.HandleFunc("/oembed", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"title":"Keyless &amp; title ","html":"<a href=\"https://www.reddit.com/r/hyderabad/\">hyderabad</a>"}`))
	})
	mux.HandleFunc("/rss", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><entry>
<author><name>/u/gopher</name></author><category term="golang" label="r/golang"/>
<content type="html">&lt;!-- SC_OFF --&gt;&lt;div class=&quot;md&quot;&gt;&lt;p&gt;First para &amp;amp; more.&lt;/p&gt; &lt;p&gt;Second&lt;/p&gt; &lt;/div&gt;&lt;!-- SC_ON --&gt; &lt;br/&gt; &lt;span&gt;[link]&lt;/span&gt;</content>
<id>t3_abc123</id><published>2024-02-27T23:27:15+00:00</published><title>Feed title</title></entry>
<entry><author><name>/u/other</name></author><id>t1_zzz</id><title>comment</title></entry></feed>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f := &Fetcher{Client: http.DefaultClient}
	u, _ := url.Parse("https://www.reddit.com/r/golang/comments/abc123/a_post/")
	mk := func(id, secret string) *reddit {
		r := newReddit(func(k string) string {
			switch k {
			case "STUGAN_PREVIEWS_REDDIT_CLIENT_ID":
				return id
			case "STUGAN_PREVIEWS_REDDIT_CLIENT_SECRET":
				return secret
			}
			return ""
		})
		r.tokenURL, r.apiBase, r.oembedURL = srv.URL+"/token", srv.URL, srv.URL+"/oembed"
		r.rssURL = func(string) string { return srv.URL + "/rss" }
		return r
	}

	r := mk("cid", "sec")
	for i := 0; i < 2; i++ {
		c, err := r.Fetch(context.Background(), f, u)
		if err != nil {
			t.Fatal(err)
		}
		if c.Title != "A post" || c.Description != "body text" || c.Image != "https://i.redd.it/x.jpg?a=1&b=2" {
			t.Errorf("card = %+v", c)
		}
		want := "r/golang|u/gopher|12.3K points|97% upvoted|321 comments|2023-11-14"
		if got := strings.Join(c.Details, "|"); got != want {
			t.Errorf("details = %q, want %q", got, want)
		}
	}
	if n := tokenCalls.Load(); n != 1 {
		t.Errorf("token requested %d times, want 1 (cached)", n)
	}

	// Bad credentials degrade to the keyless oEmbed card instead of failing.
	c, err := mk("cid", "wrong").Fetch(context.Background(), f, u)
	wantKeyless := "r/hyderabad|u/gopher|2024-02-27"
	if err != nil || c.Title != "Keyless & title" || strings.Join(c.Details, "|") != wantKeyless || c.Description != "First para & more. Second" {
		t.Errorf("fallback = %+v, %v", c, err)
	}
	// No credentials at all: oEmbed directly.
	c, err = mk("", "").Fetch(context.Background(), f, u)
	if err != nil || c.Title != "Keyless & title" {
		t.Errorf("keyless = %+v, %v", c, err)
	}
}

func TestRedditKeylessSurvivesOneSourceFailing(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oembed", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "blocked", 403) })
	mux.HandleFunc("/rss", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>t3_x</id><title>From feed</title>
<category label="r/go"/><content type="html">[deleted]</content></entry></feed>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	r := newReddit(func(string) string { return "" })
	r.oembedURL, r.rssURL = srv.URL+"/oembed", func(string) string { return srv.URL + "/rss" }
	u, _ := url.Parse("https://www.reddit.com/r/go/comments/x/t/")
	c, err := r.Fetch(context.Background(), &Fetcher{Client: http.DefaultClient}, u)
	if err != nil || c.Title != "From feed" || c.Description != "" || strings.Join(c.Details, "|") != "r/go" {
		t.Errorf("feed-only = %+v, %v", c, err)
	}

	// Both sources down: an error, so the chain falls through to a plain link.
	mux2 := http.NewServeMux()
	srv2 := httptest.NewServer(mux2)
	defer srv2.Close()
	r.oembedURL, r.rssURL = srv2.URL+"/oembed", func(string) string { return srv2.URL + "/rss" }
	if _, err := r.Fetch(context.Background(), &Fetcher{Client: http.DefaultClient}, u); err == nil {
		t.Error("expected error when both keyless sources fail")
	}

	// RSS=off skips the feed entirely.
	off := newReddit(func(k string) string {
		if k == "STUGAN_PREVIEWS_REDDIT_RSS" {
			return "off"
		}
		return ""
	})
	if !off.rssOff {
		t.Error("STUGAN_PREVIEWS_REDDIT_RSS=off not honoured")
	}
}
