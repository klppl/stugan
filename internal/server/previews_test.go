package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klippelism/stugan/internal/core"
)

func TestParseOpenGraph(t *testing.T) {
	doc := `<html><head>
	  <title>Fallback Title</title>
	  <meta property="og:title" content="OG Title">
	  <meta property="og:description" content="A description.">
	  <meta property="og:image" content="https://example.com/img.png">
	</head><body>ignored</body></html>`
	p := parseOpenGraph(strings.NewReader(doc), "https://example.com/page")
	if p.Title != "OG Title" {
		t.Errorf("title = %q", p.Title)
	}
	if p.Description != "A description." {
		t.Errorf("description = %q", p.Description)
	}
	if p.Image != "https://example.com/img.png" {
		t.Errorf("image = %q", p.Image)
	}
}

func TestParseOpenGraphTitleFallback(t *testing.T) {
	doc := `<html><head><title>Just a Title</title></head><body>x</body></html>`
	p := parseOpenGraph(strings.NewReader(doc), "u")
	if p.Title != "Just a Title" {
		t.Errorf("title fallback = %q", p.Title)
	}
}

func TestValidTarget(t *testing.T) {
	for _, c := range []struct {
		in string
		ok bool
	}{
		{"https://example.com/x", true},
		{"http://example.com", true},
		{"ftp://example.com", false},
		{"file:///etc/passwd", false},
		{"javascript:alert(1)", false},
		{"", false},
		{"/relative", false},
	} {
		if _, ok := validTarget(c.in); ok != c.ok {
			t.Errorf("validTarget(%q) = %v, want %v", c.in, ok, c.ok)
		}
	}
}

func TestUploadRoundTrip(t *testing.T) {
	eng := core.New(core.Options{Sink: noopSink{}})
	srv := New(SingleUser(&Tenant{Engine: eng}), Options{UploadDir: t.TempDir(), MaxUpload: 1 << 20})

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "cat.png")
	fw.Write([]byte("not really a png but fine"))
	mw.Close()

	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	resp, err := http.Post(hs.URL+"/api/upload", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d", resp.StatusCode)
	}
	var out struct {
		URL  string `json:"url"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.URL, "/uploads/") || !strings.HasSuffix(out.URL, ".png") {
		t.Errorf("upload url = %q", out.URL)
	}

	// The stored file is served back.
	got, err := http.Get(hs.URL + out.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	if got.StatusCode != http.StatusOK {
		t.Fatalf("serve status = %d", got.StatusCode)
	}
	if got.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("served upload missing nosniff header")
	}

	// The uploads directory must not be browsable: a directory request
	// would otherwise enumerate every stored file, defeating the
	// unguessable-random-name access model.
	for _, path := range []string{"/uploads/", "/uploads"} {
		dir, err := http.Get(hs.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(dir.Body)
		dir.Body.Close()
		if dir.StatusCode == http.StatusOK {
			t.Errorf("GET %s = 200, want directory listing disabled", path)
		}
		if strings.Contains(string(body), ".png") {
			t.Errorf("GET %s leaked a stored filename in body", path)
		}
	}
}

func TestPreviewsSidecarForwarding(t *testing.T) {
	var gotAuth, gotPath string
	side := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		switch r.URL.Path {
		case "/v1/preview":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"url":"u","title":"T","description":"","image":"","details":["x"]}`))
		default:
			http.Error(w, "nope", http.StatusUnauthorized)
		}
	}))
	defer side.Close()

	s := &Server{previews: newPreviewRemote(side.URL, "secret-secret-secret")}
	rec := httptest.NewRecorder()
	s.handlePreview(rec, httptest.NewRequest("GET", "/api/preview?url=https://example.com", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"details":["x"]`) {
		t.Fatalf("preview via sidecar: %d %s", rec.Code, rec.Body)
	}
	if gotAuth != "Bearer secret-secret-secret" || gotPath != "/v1/preview" {
		t.Errorf("sidecar saw auth=%q path=%q", gotAuth, gotPath)
	}

	// A sidecar rejecting our secret (or any odd status) surfaces as 502, and
	// never falls back to fetching in-process.
	rec = httptest.NewRecorder()
	s.handleProxy(rec, httptest.NewRequest("GET", "/api/proxy?url=https://example.com/a.png", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("proxy with sidecar 401 = %d, want 502", rec.Code)
	}

	// An unreachable sidecar is a 502 too.
	side.Close()
	rec = httptest.NewRecorder()
	s.handlePreview(rec, httptest.NewRequest("GET", "/api/preview?url=https://example.com", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("preview with sidecar down = %d, want 502", rec.Code)
	}
}
