package plugin

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newHTTPHost is like newHost but injects an HTTP client. The httptest client
// dials loopback, which the production SSRF guard would reject — injection is
// exactly what lets tests exercise the binding.
func newHTTPHost(t *testing.T, api *fakeAPI, doer HTTPDoer, script string) *Host {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "t.lua"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := New(Options{API: api, Dir: dir, HTTP: doer})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

// waitSends polls the fake API until it has at least one raw send, or fails.
func waitSends(t *testing.T, api *fakeAPI) [][2]string {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if raw := api.sentRaw(); len(raw) > 0 {
			return raw
		}
		select {
		case <-deadline:
			t.Fatal("callback never fired")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestHTTPGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("hello-body"))
	}))
	defer srv.Close()

	api := &fakeAPI{}
	// The callback echoes the result back through stugan.send so the test can
	// observe the async outcome via the fake API.
	script := `stugan.http.get("` + srv.URL + `", function(res)
		stugan.send("n", res.body .. "|" .. tostring(res.status) .. "|" .. res.headers["content-type"])
	end)`
	newHTTPHost(t, api, srv.Client(), script)

	got := waitSends(t, api)[0][1]
	if got != "hello-body|200|text/plain" {
		t.Errorf("callback got %q", got)
	}
}

func TestHTTPGetTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening now → connection refused

	api := &fakeAPI{}
	script := `stugan.http.get("` + url + `", function(res)
		stugan.send("n", tostring(res.ok) .. "|" .. tostring(res.status) .. "|" .. (res.error ~= nil and "err" or "noerr"))
	end)`
	newHTTPHost(t, api, http.DefaultClient, script)

	got := waitSends(t, api)[0][1]
	if got != "false|0|err" {
		t.Errorf("transport error result = %q, want false|0|err", got)
	}
}

func TestHTTPDisabledWithoutClient(t *testing.T) {
	api := &fakeAPI{}
	// No HTTP doer injected: the call returns (false, reason) synchronously and
	// the callback never fires. The script reports the reason via send.
	script := `local ok, err = stugan.http.get("http://example.com", function() end)
	stugan.send("n", tostring(ok) .. "|" .. tostring(err))`
	newHostNoHTTP(t, api, script)

	got := waitSends(t, api)[0][1]
	if got != "false|http: disabled" {
		t.Errorf("disabled result = %q, want false|http: disabled", got)
	}
}

// newHostNoHTTP loads a single script with no HTTP client (http disabled).
func newHostNoHTTP(t *testing.T, api *fakeAPI, script string) *Host {
	t.Helper()
	return newHost(t, api, map[string]string{"t.lua": script}, nil)
}
