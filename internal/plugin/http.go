package plugin

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	lua "github.com/yuin/gopher-lua"
)

const (
	// maxConcurrentHTTP caps simultaneous in-flight stugan.http requests so a
	// buggy script can't open an unbounded number of connections/goroutines.
	maxConcurrentHTTP = 4
	// maxHTTPBody bounds how much of a response body is read into Lua.
	maxHTTPBody = 1 << 20 // 1 MiB
	// httpReqTimeout is a backstop in case the injected client has no timeout;
	// the SSRF client (internal/safehttp) imposes its own shorter one.
	httpReqTimeout = 20 * time.Second
)

// buildHTTP constructs the per-script `stugan.http` table. The binding is
// always present; if no client was injected, calls return (false, "disabled")
// so scripts get a clear, non-fatal answer.
//
// Both get and request are asynchronous: the network call runs on a worker
// goroutine and the callback is scheduled back onto the single Lua goroutine
// (the same mechanism as hook_timer), so a slow endpoint never stalls message
// hooks or other scripts. The callback receives one table:
//
//	{ ok=bool, status=number, body=string, headers={...}, error=string|nil }
//
// ok reports whether the request completed (a transport-level success); a 404
// is ok=true with status=404. On a transport error ok=false, status=0, and
// error is set. get/request themselves return (true, nil) when the request was
// accepted, or (false, reason) if it could not even be started (http disabled,
// or too many concurrent requests) — in which case the callback never fires.
func (h *Host) buildHTTP(s *script) *lua.LTable {
	t := s.L.NewTable()

	// get(url, callback)
	t.RawSetString("get", s.L.NewFunction(func(L *lua.LState) int {
		url := L.CheckString(1)
		cb := L.CheckFunction(2)
		return h.startHTTP(L, s, "GET", url, nil, "", cb)
	}))

	// request({method=, url=, headers={}, body=}, callback)
	t.RawSetString("request", s.L.NewFunction(func(L *lua.LState) int {
		opts := L.CheckTable(1)
		cb := L.CheckFunction(2)
		method := "GET"
		if v, ok := opts.RawGetString("method").(lua.LString); ok && v != "" {
			method = strings.ToUpper(string(v))
		}
		url := lvString(opts.RawGetString("url"))
		if url == "" {
			L.ArgError(1, "request: missing url")
		}
		body := lvString(opts.RawGetString("body"))
		var headers map[string]string
		if ht, ok := opts.RawGetString("headers").(*lua.LTable); ok {
			headers = map[string]string{}
			ht.ForEach(func(k, v lua.LValue) {
				if ks, ok := k.(lua.LString); ok {
					headers[string(ks)] = L.ToStringMeta(v).String()
				}
			})
		}
		return h.startHTTP(L, s, method, url, headers, body, cb)
	}))

	return t
}

// startHTTP runs on the plugin goroutine. It gates concurrency, then hands the
// blocking work to a worker goroutine that schedules the callback back onto the
// plugin goroutine. It returns (accepted, reason) to Lua.
func (h *Host) startHTTP(L *lua.LState, s *script, method, url string, headers map[string]string, body string, cb *lua.LFunction) int {
	if h.httpClient == nil {
		L.Push(lua.LFalse)
		L.Push(lua.LString("http: disabled"))
		return 2
	}
	// Non-blocking acquire: reject immediately when at capacity rather than
	// queueing goroutines (a hot loop could otherwise exhaust memory).
	select {
	case h.httpSem <- struct{}{}:
	default:
		L.Push(lua.LFalse)
		L.Push(lua.LString("http: too many concurrent requests"))
		return 2
	}

	h.wg.Go(func() {
		defer func() { <-h.httpSem }()
		res, err := h.doHTTP(method, url, headers, body, s.name)
		h.do(func() {
			// The script may have been unloaded or reloaded (new LState) while
			// the request was in flight; only call back into the same live
			// script that registered the callback.
			if h.scripts[s.name] != s || s.disabled {
				return
			}
			h.call(s, cb, func(L *lua.LState) { L.Push(httpResultTable(L, res, err)) }, 0)
		})
	})

	L.Push(lua.LTrue)
	L.Push(lua.LNil)
	return 2
}

// httpResult is the outcome of one request (worker goroutine; no Lua).
type httpResult struct {
	status int
	body   string
	header http.Header
}

// doHTTP performs the request off the plugin goroutine.
func (h *Host) doHTTP(method, url string, headers map[string]string, body, script string) (*httpResult, error) {
	ctx, cancel := context.WithTimeout(h.ctx, httpReqTimeout)
	defer cancel()

	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "stugan-plugin/"+script)
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody))
	if err != nil {
		return nil, err
	}
	return &httpResult{status: resp.StatusCode, body: string(data), header: resp.Header}, nil
}

// httpResultTable converts a request outcome into the Lua callback argument.
func httpResultTable(L *lua.LState, res *httpResult, err error) *lua.LTable {
	t := L.NewTable()
	if err != nil {
		// Keep the table shape stable so a callback can index res.body /
		// res.headers without first checking res.ok and hitting a nil index.
		t.RawSetString("ok", lua.LFalse)
		t.RawSetString("status", lua.LNumber(0))
		t.RawSetString("body", lua.LString(""))
		t.RawSetString("headers", L.NewTable())
		t.RawSetString("error", lua.LString(err.Error()))
		return t
	}
	t.RawSetString("ok", lua.LTrue)
	t.RawSetString("status", lua.LNumber(res.status))
	t.RawSetString("body", lua.LString(res.body))
	hdr := L.NewTable()
	for k := range res.header {
		hdr.RawSetString(strings.ToLower(k), lua.LString(res.header.Get(k)))
	}
	t.RawSetString("headers", hdr)
	return t
}
