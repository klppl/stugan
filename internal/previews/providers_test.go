package previews

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func plainFetcher() *Fetcher { return &Fetcher{Client: http.DefaultClient} }

func TestGitHubParse(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/klppl/stugan":                 "repo",
		"https://github.com/klppl/stugan/":                "repo",
		"https://github.com/klppl/stugan/issues/12":       "issues",
		"https://github.com/klppl/stugan/pull/3":          "pull",
		"https://github.com/klppl/stugan/pull/3/files":    "pull",
		"https://github.com/klppl/stugan/commit/abcdef12": "commit",
		"https://github.com/klppl/stugan/blob/main/x.go":  "",
		"https://github.com/klppl":                        "",
		"https://github.com/settings/profile":             "",
		"https://github.com/features/actions":             "",
		"https://notgithub.com/a/b":                       "",
	} {
		tg, ok := ghParse(mustURL(t, in))
		got := ""
		if ok {
			got = tg.kind
		}
		if got != want {
			t.Errorf("ghParse(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGitHubFetch(t *testing.T) {
	var auth string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r", func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Write([]byte(`{"full_name":"o/r","description":"A repo","language":"Go","stargazers_count":12345,"forks_count":7,"pushed_at":"2026-01-02T03:04:05Z","html_url":"https://github.com/o/r","license":{"spdx_id":"MIT"}}`))
	})
	mux.HandleFunc("/repos/o/r/pulls/5", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"number":5,"title":"Fix <b>it</b>","body":"<pre>bad</pre> thing","state":"closed","merged":true,"additions":10,"deletions":2,"changed_files":3,"created_at":"2026-02-03T00:00:00Z","html_url":"https://github.com/o/r/pull/5","user":{"login":"me"}}`))
	})
	mux.HandleFunc("/repos/o/r/commits/abcdef123", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"sha":"abcdef1234567","html_url":"https://github.com/o/r/commit/abcdef1234567","commit":{"message":"subject line\n\nbody here","author":{"name":"Me","date":"2026-03-04T00:00:00Z"}},"stats":{"additions":4,"deletions":1}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	g := newGitHub(func(k string) string {
		if k == "STUGAN_PREVIEWS_GITHUB_TOKEN" {
			return "tok"
		}
		return ""
	})
	g.api = srv.URL
	ctx := context.Background()

	c, err := g.Fetch(ctx, plainFetcher(), mustURL(t, "https://github.com/o/r"))
	if err != nil || c.Title != "o/r" || strings.Join(c.Details, "|") != "Go|12.3K stars|7 forks|MIT|2026-01-02" {
		t.Fatalf("repo = %+v, %v", c, err)
	}
	if auth != "Bearer tok" {
		t.Errorf("token not sent, auth = %q", auth)
	}
	c, err = g.Fetch(ctx, plainFetcher(), mustURL(t, "https://github.com/o/r/pull/5"))
	if err != nil || c.Title != "#5 Fix <b>it</b>" || c.Description != "bad thing" ||
		strings.Join(c.Details, "|") != "merged|@me|+10 −2 in 3 files|2026-02-03" {
		t.Fatalf("pull = %+v, %v", c, err)
	}
	c, err = g.Fetch(ctx, plainFetcher(), mustURL(t, "https://github.com/o/r/commit/abcdef123"))
	if err != nil || c.Title != "subject line" || c.Description != "body here" || c.Details[0] != "abcdef1" {
		t.Fatalf("commit = %+v, %v", c, err)
	}
}

func TestHackerNews(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/item/1.json":
			w.Write([]byte(`{"type":"story","title":"Show HN: thing","by":"pg","score":321,"descendants":45,"url":"https://www.example.com/x","time":1700000000}`))
		case "/item/2.json":
			w.Write([]byte(`{"type":"comment","by":"bob","text":"Hello<p>world &amp; more"}`))
		default:
			w.Write([]byte(`{"deleted":true}`))
		}
	}))
	defer srv.Close()
	h := &hackerNews{api: srv.URL}
	if hnID(mustURL(t, "https://news.ycombinator.com/news")) != "" || hnID(mustURL(t, "https://news.ycombinator.com/item?id=1")) != "1" {
		t.Error("hnID")
	}
	c, err := h.Fetch(context.Background(), plainFetcher(), mustURL(t, "https://news.ycombinator.com/item?id=1"))
	if err != nil || strings.Join(c.Details, "|") != "321 points|by pg|45 comments|example.com|2023-11-14" {
		t.Fatalf("story = %+v, %v", c, err)
	}
	c, err = h.Fetch(context.Background(), plainFetcher(), mustURL(t, "https://news.ycombinator.com/item?id=2"))
	if err != nil || c.Title != "Comment by bob" || c.Description != "Hello world & more" {
		t.Fatalf("comment = %+v, %v", c, err)
	}
	if _, err := h.Fetch(context.Background(), plainFetcher(), mustURL(t, "https://news.ycombinator.com/item?id=3")); err == nil {
		t.Error("deleted item should error")
	}
}

func TestBluesky(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("uri"); got != "at://alice.bsky.social/app.bsky.feed.post/3abc" {
			http.Error(w, got, 400)
			return
		}
		w.Write([]byte(`{"thread":{"post":{"author":{"handle":"alice.bsky.social","displayName":"Alice"},"record":{"text":"hi there","createdAt":"2026-05-06T07:08:09Z"},"embed":{"images":[{"thumb":"https://cdn/x.jpg"}]},"likeCount":1500,"repostCount":3,"replyCount":0}}}`))
	}))
	defer srv.Close()
	b := &bluesky{api: srv.URL}
	u := mustURL(t, "https://bsky.app/profile/alice.bsky.social/post/3abc")
	if !b.Match(u) || b.Match(mustURL(t, "https://bsky.app/profile/alice.bsky.social")) {
		t.Error("Match")
	}
	c, err := b.Fetch(context.Background(), plainFetcher(), u)
	if err != nil || c.Title != "Alice (@alice.bsky.social)" || c.Image != "https://cdn/x.jpg" ||
		strings.Join(c.Details, "|") != "1500 likes|3 reposts|0 replies|2026-05-06" {
		t.Fatalf("card = %+v, %v", c, err)
	}
}

func TestWikipedia(t *testing.T) {
	if wikiTitle(mustURL(t, "https://en.wikipedia.org/wiki/Go_(programming_language)")) != "Go_%28programming_language%29" &&
		wikiTitle(mustURL(t, "https://en.wikipedia.org/wiki/Go_(programming_language)")) != "Go_(programming_language)" {
		t.Error("title")
	}
	for _, bad := range []string{"https://en.wikipedia.org/wiki/Special:Random", "https://en.wikipedia.org/", "https://www.wikipedia.org/wiki/X", "https://example.org/wiki/X"} {
		if wikiTitle(mustURL(t, bad)) != "" {
			t.Errorf("wikiTitle(%q) should be empty", bad)
		}
	}
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Write([]byte(`{"type":"standard","title":"Go","description":"Programming language","extract":"Go is a language.","thumbnail":{"source":"https://t/x.png"},"content_urls":{"desktop":{"page":"https://en.wikipedia.org/wiki/Go"}}}`))
	}))
	defer srv.Close()
	w := &wikipedia{apiFor: func(string) string { return srv.URL }}
	c, err := w.Fetch(context.Background(), plainFetcher(), mustURL(t, "https://en.wikipedia.org/wiki/Go"))
	if err != nil || c.Title != "Go" || c.Details[0] != "Programming language" || c.Image != "https://t/x.png" {
		t.Fatalf("card = %+v, %v", c, err)
	}
	if !strings.HasPrefix(gotUA, "stugan-previews/") {
		t.Errorf("Wikimedia needs a descriptive User-Agent, sent %q", gotUA)
	}
}

func TestOEmbedTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("url") != "https://vimeo.com/123" {
			http.Error(w, "bad", 400)
			return
		}
		w.Write([]byte(`{"title":"A video","author_name":"Someone","thumbnail_url":"https://t/y.jpg","duration":3725}`))
	}))
	defer srv.Close()
	o := &oEmbed{sites: []oEmbedSite{{"Vimeo", []string{"vimeo.com"}, srv.URL}}}
	u := mustURL(t, "https://vimeo.com/123")
	if !o.Match(u) || o.Match(mustURL(t, "https://example.com/123")) || !o.Match(mustURL(t, "https://player.vimeo.com/x")) {
		t.Error("Match")
	}
	c, err := o.Fetch(context.Background(), plainFetcher(), u)
	if err != nil || c.Title != "A video" || c.Site != "Vimeo" || strings.Join(c.Details, "|") != "Someone|1:02:05" {
		t.Fatalf("card = %+v, %v", c, err)
	}
}

func TestFileCard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dl":
			w.Header().Set("Content-Type", "application/zip")
			w.Header().Set("Content-Disposition", `attachment; filename="backup.zip"`)
			w.Header().Set("Content-Length", "1572864")
			w.Write(make([]byte, 1572864))
		case "/pic":
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("x"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f := plainFetcher()
	c, err := openGraph{}.Fetch(context.Background(), f, mustURL(t, srv.URL+"/dl"))
	if err != nil || c.Title != "backup.zip" || c.Site != "File" || c.Details[0] != "application/zip" || c.Details[1] != "1.5 MB" {
		t.Fatalf("zip = %+v, %v", c, err)
	}
	c, err = openGraph{}.Fetch(context.Background(), f, mustURL(t, srv.URL+"/pic"))
	if err != nil || c.Title != "pic" || c.Image != srv.URL+"/pic" {
		t.Fatalf("image = %+v, %v", c, err)
	}
	if _, err := (openGraph{}).Fetch(context.Background(), f, mustURL(t, srv.URL+"/missing")); err == nil {
		t.Error("404 should not produce a card")
	}
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KB", 5 << 30: "5.0 GB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestExpandAndShorteners(t *testing.T) {
	if !isShortener("bit.ly") || !isShortener("www.t.co") || isShortener("github.com") || isShortener("notbit.ly") {
		t.Error("isShortener")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/redir", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>done</html>")) })
	mux.HandleFunc("/meta", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><noscript><META http-equiv="refresh" content="0;URL='https://example.com/dest'"></noscript></head></html>`))
	})
	mux.HandleFunc("/js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<script>window.location.replace("https://example.org/js-dest")</script>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	for path, want := range map[string]string{
		"/redir": srv.URL + "/final",
		"/meta":  "https://example.com/dest",
		"/js":    "https://example.org/js-dest",
	} {
		got, err := expand(context.Background(), plainFetcher(), mustURL(t, srv.URL+path))
		if err != nil || got.String() != want {
			t.Errorf("expand(%s) = %v, %v; want %s", path, got, err, want)
		}
	}
}
