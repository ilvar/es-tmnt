package proxy

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"

	"es-tmnt/internal/config"
)

type Proxy struct {
	cfg       config.Config
	upstream  *url.URL
	router    *Router
	transport http.RoundTripper
}

func New(cfg config.Config) (*Proxy, error) {
	upstream, err := url.Parse(cfg.UpstreamURL)
	if err != nil {
		return nil, fmt.Errorf("parse upstream url: %w", err)
	}
	router, err := NewRouter(cfg)
	if err != nil {
		return nil, err
	}
	return &Proxy{
		cfg:       cfg,
		upstream:  upstream,
		router:    router,
		transport: http.DefaultTransport,
	}, nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route, err := p.router.Route(r)
	if err != nil {
		http.Error(w, fmt.Sprintf("unsupported request: %v", err), http.StatusBadRequest)
		return
	}
	if route.Action != ActionPassthrough {
		if err := p.router.Rewrite(r, route); err != nil {
			http.Error(w, fmt.Sprintf("rewrite error: %v", err), http.StatusBadRequest)
			return
		}
		r.Header.Set("X-ES-Tenant", route.Tenant)
	}
	proxied := httputil.NewSingleHostReverseProxy(p.upstream)
	proxied.Transport = p.transport
	originalDirector := proxied.Director
	proxied.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = p.upstream.Host
	}
	proxied.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		http.Error(rw, fmt.Sprintf("proxy error: %v", err), http.StatusBadGateway)
	}
	proxied.ServeHTTP(w, r)
}
