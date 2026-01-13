package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"regexp"
	"strings"
	"text/template"

	"es-tmnt/internal/config"
)

type Proxy struct {
	cfg          config.Config
	proxy        *httputil.ReverseProxy
	aliasTmpl    *template.Template
	sharedIndex  *template.Template
	perTenantIdx *template.Template
	indexGroup   int
	tenantGroup  int
	prefixGroup  int
	postfixGroup int
	passthroughs []string
}

func New(cfg config.Config) (*Proxy, error) {
	parsed, err := url.Parse(cfg.UpstreamURL)
	if err != nil {
		return nil, fmt.Errorf("parse upstream url: %w", err)
	}
	aliasTmpl, err := template.New("alias").Parse(cfg.SharedIndex.AliasTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse alias template: %w", err)
	}
	sharedIndex, err := template.New("shared").Parse(cfg.SharedIndex.Name)
	if err != nil {
		return nil, fmt.Errorf("parse shared index template: %w", err)
	}
	perTenantIdx, err := template.New("index-per-tenant").Parse(cfg.IndexPerTenant.IndexTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse index per tenant template: %w", err)
	}
	indexGroup, tenantGroup, prefixGroup, postfixGroup, err := groupIndexes(cfg.TenantRegex.Compiled)
	if err != nil {
		return nil, err
	}
	reverseProxy := httputil.NewSingleHostReverseProxy(parsed)
	return &Proxy{
		cfg:          cfg,
		proxy:        reverseProxy,
		aliasTmpl:    aliasTmpl,
		sharedIndex:  sharedIndex,
		perTenantIdx: perTenantIdx,
		indexGroup:   indexGroup,
		tenantGroup:  tenantGroup,
		prefixGroup:  prefixGroup,
		postfixGroup: postfixGroup,
		passthroughs: cfg.PassthroughPaths,
	}, nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.isPassthrough(r.URL.Path) {
		p.proxy.ServeHTTP(w, r)
		return
	}
	segments := splitPath(r.URL.Path)
	if len(segments) == 0 {
		p.reject(w, "unsupported path")
		return
	}
	if strings.HasPrefix(segments[0], "_") {
		if segments[0] == "_bulk" {
			p.handleBulk(w, r, "")
			return
		}
		p.reject(w, "unsupported system endpoint")
		return
	}
	index := segments[0]
	if len(segments) == 1 {
		p.reject(w, "unsupported index endpoint")
		return
	}
	switch segments[1] {
	case "_search":
		p.handleSearch(w, r, index)
	case "_doc":
		p.handleDoc(w, r, index)
	case "_update":
		if len(segments) < 3 {
			p.reject(w, "missing document id")
			return
		}
		p.handleUpdate(w, r, index)
	case "_bulk":
		p.handleBulk(w, r, index)
	default:
		p.reject(w, "unsupported endpoint")
	}
}

func (p *Proxy) handleSearch(w http.ResponseWriter, r *http.Request, index string) {
	baseIndex, tenantID, err := p.parseIndex(index)
	if err != nil {
		p.reject(w, err.Error())
		return
	}
	aliasIndex := index
	if isSharedMode(p.cfg.Mode) {
		aliasIndex, err = p.renderAlias(baseIndex, tenantID)
		if err != nil {
			p.reject(w, err.Error())
			return
		}
	} else {
		aliasIndex, err = p.renderIndex(p.perTenantIdx, baseIndex, tenantID)
		if err != nil {
			p.reject(w, err.Error())
			return
		}
	}
	if r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			p.reject(w, "failed to read body")
			return
		}
		rewritten, err := p.rewriteQueryBody(body, baseIndex)
		if err != nil {
			p.reject(w, err.Error())
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(rewritten))
		r.ContentLength = int64(len(rewritten))
	}
	p.rewriteIndexPath(r, index, aliasIndex)
	p.proxy.ServeHTTP(w, r)
}

func (p *Proxy) handleDoc(w http.ResponseWriter, r *http.Request, index string) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		p.reject(w, "unsupported method for _doc")
		return
	}
	baseIndex, tenantID, err := p.parseIndex(index)
	if err != nil {
		p.reject(w, err.Error())
		return
	}
	if r.Body == nil {
		p.reject(w, "missing body")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		p.reject(w, "failed to read body")
		return
	}
	rewritten, err := p.rewriteDocumentBody(body, baseIndex, tenantID)
	if err != nil {
		p.reject(w, err.Error())
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(rewritten))
	r.ContentLength = int64(len(rewritten))
	targetIndex, err := p.renderIndex(p.sharedIndex, baseIndex, tenantID)
	if err != nil {
		p.reject(w, err.Error())
		return
	}
	if !isSharedMode(p.cfg.Mode) {
		targetIndex, err = p.renderIndex(p.perTenantIdx, baseIndex, tenantID)
		if err != nil {
			p.reject(w, err.Error())
			return
		}
	}
	p.rewriteIndexPath(r, index, targetIndex)
	p.proxy.ServeHTTP(w, r)
}

func (p *Proxy) handleUpdate(w http.ResponseWriter, r *http.Request, index string) {
	if r.Method != http.MethodPost {
		p.reject(w, "unsupported method for _update")
		return
	}
	baseIndex, tenantID, err := p.parseIndex(index)
	if err != nil {
		p.reject(w, err.Error())
		return
	}
	if r.Body == nil {
		p.reject(w, "missing body")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		p.reject(w, "failed to read body")
		return
	}
	rewritten, err := p.rewriteUpdateBody(body, baseIndex, tenantID)
	if err != nil {
		p.reject(w, err.Error())
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(rewritten))
	r.ContentLength = int64(len(rewritten))
	targetIndex, err := p.renderIndex(p.sharedIndex, baseIndex, tenantID)
	if err != nil {
		p.reject(w, err.Error())
		return
	}
	if !isSharedMode(p.cfg.Mode) {
		targetIndex, err = p.renderIndex(p.perTenantIdx, baseIndex, tenantID)
		if err != nil {
			p.reject(w, err.Error())
			return
		}
	}
	p.rewriteIndexPath(r, index, targetIndex)
	p.proxy.ServeHTTP(w, r)
}

func (p *Proxy) handleBulk(w http.ResponseWriter, r *http.Request, index string) {
	if r.Method != http.MethodPost {
		p.reject(w, "unsupported method for bulk")
		return
	}
	if r.Body == nil {
		p.reject(w, "missing body")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		p.reject(w, "failed to read body")
		return
	}
	rewritten, err := p.rewriteBulkBody(body, index)
	if err != nil {
		p.reject(w, err.Error())
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(rewritten))
	r.ContentLength = int64(len(rewritten))
	if index != "" {
		targetIndex := index
		baseIndex, tenantID, err := p.parseIndex(index)
		if err != nil {
			p.reject(w, err.Error())
			return
		}
		if isSharedMode(p.cfg.Mode) {
			targetIndex, err = p.renderIndex(p.sharedIndex, baseIndex, tenantID)
			if err != nil {
				p.reject(w, err.Error())
				return
			}
		} else {
			targetIndex, err = p.renderIndex(p.perTenantIdx, baseIndex, tenantID)
			if err != nil {
				p.reject(w, err.Error())
				return
			}
		}
		p.rewriteIndexPath(r, index, targetIndex)
	}
	p.proxy.ServeHTTP(w, r)
}

func (p *Proxy) rewriteIndexPath(r *http.Request, original, replacement string) {
	segments := splitPath(r.URL.Path)
	if len(segments) == 0 {
		return
	}
	if segments[0] != original {
		return
	}
	segments[0] = replacement
	r.URL.Path = "/" + path.Join(segments...)
	r.RequestURI = r.URL.Path
}

func (p *Proxy) parseIndex(index string) (string, string, error) {
	matches := p.cfg.TenantRegex.Compiled.FindStringSubmatch(index)
	if matches == nil {
		return "", "", fmt.Errorf("index '%s' does not match tenant regex", index)
	}
	if p.indexGroup >= len(matches) || p.tenantGroup >= len(matches) ||
		p.prefixGroup >= len(matches) || p.postfixGroup >= len(matches) {
		return "", "", errors.New("tenant regex missing required groups")
	}
	prefix := matches[p.prefixGroup]
	postfix := matches[p.postfixGroup]
	baseIndex := ""
	if p.indexGroup >= 0 && p.indexGroup < len(matches) {
		baseIndex = matches[p.indexGroup]
	}
	tenantID := matches[p.tenantGroup]
	if baseIndex == "" {
		baseIndex = prefix + postfix
	}
	if baseIndex == "" || tenantID == "" {
		return "", "", fmt.Errorf("invalid index '%s'", index)
	}
	return baseIndex, tenantID, nil
}

func (p *Proxy) renderAlias(index, tenant string) (string, error) {
	var builder strings.Builder
	data := map[string]string{"index": index, "tenant": tenant}
	if err := p.aliasTmpl.Execute(&builder, data); err != nil {
		return "", fmt.Errorf("render alias: %w", err)
	}
	return builder.String(), nil
}

func (p *Proxy) renderIndex(tmpl *template.Template, index, tenant string) (string, error) {
	var builder strings.Builder
	data := map[string]string{"index": index, "tenant": tenant}
	if err := tmpl.Execute(&builder, data); err != nil {
		return "", fmt.Errorf("render index: %w", err)
	}
	return builder.String(), nil
}

func (p *Proxy) isPassthrough(pathValue string) bool {
	for _, allowed := range p.passthroughs {
		if allowed == "" {
			continue
		}
		if strings.HasSuffix(allowed, "*") {
			prefix := strings.TrimSuffix(allowed, "*")
			if strings.HasPrefix(pathValue, prefix) {
				return true
			}
			continue
		}
		if pathValue == allowed {
			return true
		}
	}
	return false
}

func (p *Proxy) reject(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   "unsupported_request",
		"message": message,
	})
}

func splitPath(pathValue string) []string {
	trimmed := strings.Trim(pathValue, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func groupIndexes(regex *regexp.Regexp) (int, int, int, int, error) {
	indexGroup := -1
	tenantGroup := -1
	prefixGroup := -1
	postfixGroup := -1
	for idx, name := range regex.SubexpNames() {
		if name == "index" {
			indexGroup = idx
		}
		if name == "tenant" {
			tenantGroup = idx
		}
		if name == "prefix" {
			prefixGroup = idx
		}
		if name == "postfix" {
			postfixGroup = idx
		}
	}
	if tenantGroup == -1 || prefixGroup == -1 || postfixGroup == -1 {
		return 0, 0, 0, 0, errors.New("TENANT_REGEX must include named groups 'prefix', 'tenant', and 'postfix'")
	}
	return indexGroup, tenantGroup, prefixGroup, postfixGroup, nil
}

func isSharedMode(mode string) bool {
	return strings.EqualFold(mode, "shared")
}
