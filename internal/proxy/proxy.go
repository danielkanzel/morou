// Package proxy implements a streaming-capable reverse proxy to upstreams.
package proxy

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// Proxy forwards an incoming request to a chosen upstream while preserving
// streaming (SSE) semantics and propagating client cancellation.
type Proxy struct {
	transport http.RoundTripper
	log       *slog.Logger
}

// New builds a Proxy with a transport tuned for proxying (including streaming).
func New(log *slog.Logger) *Proxy {
	if log == nil {
		log = slog.Default()
	}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   50,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Disable response buffering so SSE chunks flush immediately.
		ResponseHeaderTimeout: 0,
	}
	return &Proxy{transport: transport, log: log}
}

// Forward proxies r to target and writes the upstream response to w. The
// request context governs the upstream call, so cancellation (client
// disconnect) aborts the upstream request. timeout caps the overall duration.
func (p *Proxy) Forward(w http.ResponseWriter, r *http.Request, target *url.URL, timeout time.Duration) {
	ctx := r.Context()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	rp := &httputil.ReverseProxy{
		Transport:     p.transport,
		FlushInterval: -1, // flush immediately: required for streaming SSE
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			// Path/RawQuery are left as-is so /v1/* maps directly upstream.
		},
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			// Client cancellation is expected; do not treat as a server error.
			if req.Context().Err() != nil {
				p.log.Debug("upstream request cancelled", "url", target.String(), "err", err)
				return
			}
			p.log.Error("proxy error", "url", target.String(), "err", err)
			rw.WriteHeader(http.StatusBadGateway)
		},
	}

	rp.ServeHTTP(w, r.WithContext(ctx))
}
