package server

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/klippelism/stugan/internal/previewproto"
)

// previewRemote forwards preview and media requests to a stugan-previews
// sidecar. There is deliberately no in-process fallback when the sidecar is
// down: quietly fetching untrusted URLs inside this process would defeat the
// isolation the sidecar exists for, so the client just gets a 502.
type previewRemote struct {
	base   string // normalized base URL, no trailing slash
	secret string
	client *http.Client
}

func newPreviewRemote(rawURL, secret string) *previewRemote {
	return &previewRemote{
		base:   previewproto.NormalizeURL(rawURL),
		secret: secret,
		client: &http.Client{
			// No overall Timeout: media streams can be long. Bound the wait
			// for the sidecar to start answering instead.
			Transport: &http.Transport{
				ResponseHeaderTimeout: 20 * time.Second,
				MaxIdleConnsPerHost:   8,
				IdleConnTimeout:       60 * time.Second,
			},
		},
	}
}

func (p *previewRemote) get(ctx context.Context, path, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+path+"?url="+url.QueryEscape(target), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.secret)
	return p.client.Do(req)
}

// relay copies the sidecar's response to w, keeping only the headers that
// matter (the sidecar already set the media-safety headers).
func relay(w http.ResponseWriter, resp *http.Response) {
	for _, h := range []string{"Content-Type", "Cache-Control", "X-Content-Type-Options", "Content-Security-Policy"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p *previewRemote) forward(w http.ResponseWriter, r *http.Request, path, target string) {
	resp, err := p.get(r.Context(), path, target)
	if err != nil {
		http.Error(w, "previews unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusUnsupportedMediaType, http.StatusBadGateway, http.StatusBadRequest:
		relay(w, resp)
	default: // 401 (wrong secret) and anything unexpected
		http.Error(w, "previews unavailable", http.StatusBadGateway)
	}
}

func (p *previewRemote) preview(w http.ResponseWriter, r *http.Request, target string) {
	p.forward(w, r, previewproto.PathPreview, target)
}

func (p *previewRemote) proxy(w http.ResponseWriter, r *http.Request, target string) {
	p.forward(w, r, previewproto.PathProxy, target)
}
