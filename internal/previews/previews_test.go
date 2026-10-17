package previews

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSetting(t *testing.T) {
	env := func(k string) string {
		if k == "STUGAN_PREVIEWS_YOUTUBE_API_KEY" {
			return " abc \n"
		}
		return ""
	}
	if got := Setting(env, "youtube", "api_key"); got != "abc" {
		t.Errorf("Setting = %q", got)
	}
	if got := Setting(env, "reddit", "client_id"); got != "" {
		t.Errorf("unset Setting = %q", got)
	}
}

func TestOSEnvFileVariant(t *testing.T) {
	f := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(f, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUGAN_TEST_KEY_FILE", f)
	if got := OSEnv("STUGAN_TEST_KEY"); got != "from-file" {
		t.Errorf("OSEnv via _FILE = %q", got)
	}
	t.Setenv("STUGAN_TEST_KEY", "direct")
	if got := OSEnv("STUGAN_TEST_KEY"); got != "direct" {
		t.Errorf("direct var should win, got %q", got)
	}
}

func TestYouTubeTarget(t *testing.T) {
	for _, c := range []struct{ in, video, playlist string }{
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", "dQw4w9WgXcQ", ""},
		{"https://youtu.be/dQw4w9WgXcQ?t=5", "dQw4w9WgXcQ", ""},
		{"https://m.youtube.com/shorts/dQw4w9WgXcQ", "dQw4w9WgXcQ", ""},
		{"https://www.youtube.com/live/dQw4w9WgXcQ", "dQw4w9WgXcQ", ""},
		{"https://www.youtube.com/playlist?list=PLabc123", "", "PLabc123"},
		{"https://www.youtube.com/playlist?list=WL", "", ""},
		{"https://www.youtube.com/@somechannel", "", ""},
		{"https://www.youtube.com/channel/UCxxxxxxxxxxxxxxxxxxxxxx", "", ""},
		{"https://notyoutube.com/watch?v=dQw4w9WgXcQ", "", ""},
	} {
		u, _ := url.Parse(c.in)
		v, p := ytTarget(u)
		if v != c.video || p != c.playlist {
			t.Errorf("ytTarget(%q) = (%q,%q), want (%q,%q)", c.in, v, p, c.video, c.playlist)
		}
	}
}

func TestFormatISODuration(t *testing.T) {
	for in, want := range map[string]string{
		"PT45S": "0:45", "PT3M5S": "3:05", "PT1H2M3S": "1:02:03", "P1DT1H": "25:00:00", "garbage": "",
	} {
		if got := formatISODuration(in); got != want {
			t.Errorf("formatISODuration(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCompactCount(t *testing.T) {
	for in, want := range map[int64]string{999: "999", 12345: "12.3K", 1500000: "1.5M", 2000000: "2M", 3200000000: "3.2B"} {
		if got := compactCount(in); got != want {
			t.Errorf("compactCount(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestInstagramTarget(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.instagram.com/p/Cabc_123/":         "p/Cabc_123",
		"https://instagram.com/reel/Cabc123/?igsh=x":    "reel/Cabc123",
		"https://www.instagram.com/reels/Cabc123/":      "reel/Cabc123",
		"https://www.instagram.com/someuser/p/Cabc123/": "p/Cabc123",
		"https://www.instagram.com/someuser/":           "",
		"https://evilinstagram.com/p/Cabc123/":          "",
	} {
		u, _ := url.Parse(in)
		k, c, ok := igTarget(u)
		got := ""
		if ok {
			got = k + "/" + c
		}
		if got != want {
			t.Errorf("igTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseInstagramEmbed(t *testing.T) {
	doc := `<div class="Embed"><img class="EmbeddedMediaImage" alt="x" src="https://cdn.example/p.jpg?a=1&amp;b=2">
	<div class="Likes">&nbsp;<a>x</a>></div><span>>1.2K likes<</span>
	<div class="Caption"><a class="CaptionUsername" href="/u">some_user</a> Hello<br/>world &amp; more
	<a>View all 12 comments</a></div></div>`
	c, err := parseInstagramEmbed(doc, "https://www.instagram.com/p/x/")
	if err != nil {
		t.Fatal(err)
	}
	if c.Title != "@some_user" {
		t.Errorf("title = %q", c.Title)
	}
	if c.Description != "Hello world & more" {
		t.Errorf("description = %q", c.Description)
	}
	if c.Image != "https://cdn.example/p.jpg?a=1&b=2" {
		t.Errorf("image = %q", c.Image)
	}
	if len(c.Details) != 1 || c.Details[0] != "1200 likes" {
		t.Errorf("details = %v", c.Details)
	}
	if _, err := parseInstagramEmbed("<html>login</html>", "u"); err == nil {
		t.Error("expected error for unrecognizable page")
	}
}

type fakeProvider struct {
	name  string
	match bool
	card  *Card
	err   error
	calls atomic.Int32
}

func (f *fakeProvider) Name() string        { return f.name }
func (f *fakeProvider) Match(*url.URL) bool { return f.match }
func (f *fakeProvider) Fetch(context.Context, *Fetcher, *url.URL) (*Card, error) {
	f.calls.Add(1)
	return f.card, f.err
}

func TestRegistryFallsThrough(t *testing.T) {
	bad := &fakeProvider{name: "site", match: true, err: errors.New("blocked")}
	skip := &fakeProvider{name: "other", match: false}
	good := &fakeProvider{name: "og", match: true, card: &Card{Title: "ok"}}
	reg := NewRegistry([]Provider{bad, skip, good}, nil)
	c, err := reg.Resolve(context.Background(), nil, "https://example.com/x")
	if err != nil || c.Title != "ok" {
		t.Fatalf("Resolve = %+v, %v", c, err)
	}
	if skip.calls.Load() != 0 || c.URL != "https://example.com/x" {
		t.Errorf("non-matching provider ran or URL not set: %+v", c)
	}
	if _, err := NewRegistry([]Provider{bad}, nil).Resolve(context.Background(), nil, "https://example.com"); err == nil {
		t.Error("expected error when every provider fails")
	}
}

func TestServerAuthAndCache(t *testing.T) {
	p := &fakeProvider{name: "fake", match: true, card: &Card{Title: "hello", Details: []string{"a"}}}
	srv, err := New(Options{Secret: strings.Repeat("s", 16), Registry: NewRegistry([]Provider{p}, nil), Fetcher: &Fetcher{Client: http.DefaultClient}})
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()
	do := func(path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	q := "/v1/preview?url=" + url.QueryEscape("https://example.com/a")

	if rec := do(q, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", rec.Code)
	}
	if rec := do(q, "wrong-secret-xxxxx"); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", rec.Code)
	}
	if rec := do("/healthz", ""); rec.Code != http.StatusOK {
		t.Errorf("healthz: %d", rec.Code)
	}
	if rec := do("/v1/preview?url=file:///etc/passwd", strings.Repeat("s", 16)); rec.Code != http.StatusBadRequest {
		t.Errorf("bad url: %d", rec.Code)
	}
	for i := 0; i < 2; i++ {
		rec := do(q, strings.Repeat("s", 16))
		if rec.Code != 200 {
			t.Fatalf("preview: %d %s", rec.Code, rec.Body)
		}
		var c Card
		if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil || c.Title != "hello" || len(c.Details) != 1 {
			t.Fatalf("card = %+v, %v", c, err)
		}
	}
	if n := p.calls.Load(); n != 1 {
		t.Errorf("provider called %d times, want 1 (second should hit cache)", n)
	}
}

func TestServerShortSecret(t *testing.T) {
	if _, err := New(Options{Secret: "short"}); err == nil {
		t.Error("expected error for short secret")
	}
}
