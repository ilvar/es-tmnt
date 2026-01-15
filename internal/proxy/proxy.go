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
	"strconv"
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

const (
	responseModeHeader      = "X-ES-TMNT"
	responseModeHandled     = "handled"
	responseModePassthrough = "pass-through"
)

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
	proxy := &Proxy{
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
	}
	reverseProxy.ModifyResponse = proxy.modifyResponse
	return proxy, nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.isPassthrough(r.URL.Path) {
		p.setResponseMode(w, responseModePassthrough)
		p.proxy.ServeHTTP(w, r)
		return
	}
	segments := splitPath(r.URL.Path)
	if len(segments) == 0 {
		p.setResponseMode(w, responseModeHandled)
		p.reject(w, "unsupported path")
		return
	}
	if strings.HasPrefix(segments[0], "_") {
		if segments[0] == "_bulk" {
			p.setResponseMode(w, responseModeHandled)
			p.handleBulk(w, r, "")
			return
		}
		if p.isCatIndices(r.URL.Path) {
			p.setResponseMode(w, responseModeHandled)
			p.proxy.ServeHTTP(w, r)
			return
		}
		if segments[0] == "_transform" {
			p.setResponseMode(w, responseModeHandled)
			p.handleTransform(w, r)
			return
		}
		if segments[0] == "_rollup" {
			p.setResponseMode(w, responseModeHandled)
			p.handleRollup(w, r)
			return
		}
		if p.isSystemPassthrough(r.URL.Path) {
			p.setResponseMode(w, responseModePassthrough)
			p.proxy.ServeHTTP(w, r)
			return
		}
		p.setResponseMode(w, responseModeHandled)
		p.reject(w, "unsupported system endpoint")
		return
	}
	index := segments[0]
	if len(segments) == 1 {
		p.setResponseMode(w, responseModeHandled)
		p.handleIndexRoot(w, r, index)
		return
	}
	p.setResponseMode(w, responseModeHandled)
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
	case "_mapping":
		p.handleMapping(w, r, index)
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

func (p *Proxy) handleIndexRoot(w http.ResponseWriter, r *http.Request, index string) {
	switch r.Method {
	case http.MethodPut:
		p.handleIndexCreate(w, r, index)
	case http.MethodDelete:
		p.handleIndexDelete(w, r, index)
	default:
		p.reject(w, "unsupported index endpoint")
	}
}

func (p *Proxy) handleIndexCreate(w http.ResponseWriter, r *http.Request, index string) {
	baseIndex, tenantID, err := p.parseIndex(index)
	if err != nil {
		p.reject(w, err.Error())
		return
	}
	if r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			p.reject(w, "failed to read body")
			return
		}
		if len(bytes.TrimSpace(body)) != 0 {
			rewritten, err := p.rewriteMappingBody(body, baseIndex)
			if err != nil {
				p.reject(w, err.Error())
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(rewritten))
			r.ContentLength = int64(len(rewritten))
		}
	}
	targetIndex, err := p.renderTargetIndex(baseIndex, tenantID)
	if err != nil {
		p.reject(w, err.Error())
		return
	}
	p.rewriteIndexPath(r, index, targetIndex)
	p.proxy.ServeHTTP(w, r)
}

func (p *Proxy) handleIndexDelete(w http.ResponseWriter, r *http.Request, index string) {
	baseIndex, tenantID, err := p.parseIndex(index)
	if err != nil {
		p.reject(w, err.Error())
		return
	}
	targetIndex, err := p.renderTargetIndex(baseIndex, tenantID)
	if err != nil {
		p.reject(w, err.Error())
		return
	}
	p.rewriteIndexPath(r, index, targetIndex)
	p.proxy.ServeHTTP(w, r)
}

func (p *Proxy) handleMapping(w http.ResponseWriter, r *http.Request, index string) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		p.reject(w, "unsupported method for _mapping")
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
	rewritten, err := p.rewriteMappingBody(body, baseIndex)
	if err != nil {
		p.reject(w, err.Error())
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(rewritten))
	r.ContentLength = int64(len(rewritten))
	targetIndex, err := p.renderTargetIndex(baseIndex, tenantID)
	if err != nil {
		p.reject(w, err.Error())
		return
	}
	p.rewriteIndexPath(r, index, targetIndex)
	p.proxy.ServeHTTP(w, r)
}

func (p *Proxy) handleTransform(w http.ResponseWriter, r *http.Request) {
	if r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			p.reject(w, "failed to read body")
			return
		}
		if len(bytes.TrimSpace(body)) != 0 {
			rewritten, err := p.rewriteTransformBody(body)
			if err != nil {
				p.reject(w, err.Error())
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(rewritten))
			r.ContentLength = int64(len(rewritten))
		}
	}
	p.proxy.ServeHTTP(w, r)
}

func (p *Proxy) handleRollup(w http.ResponseWriter, r *http.Request) {
	if r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			p.reject(w, "failed to read body")
			return
		}
		if len(bytes.TrimSpace(body)) != 0 {
			rewritten, err := p.rewriteRollupBody(body)
			if err != nil {
				p.reject(w, err.Error())
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(rewritten))
			r.ContentLength = int64(len(rewritten))
		}
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

func (p *Proxy) renderTargetIndex(baseIndex, tenantID string) (string, error) {
	if isSharedMode(p.cfg.Mode) {
		return p.renderIndex(p.sharedIndex, baseIndex, tenantID)
	}
	return p.renderIndex(p.perTenantIdx, baseIndex, tenantID)
}

func (p *Proxy) isSystemPassthrough(pathValue string) bool {
	return strings.HasPrefix(pathValue, "/_cluster") ||
		strings.HasPrefix(pathValue, "/_cat") ||
		strings.HasPrefix(pathValue, "/_nodes") ||
		strings.HasPrefix(pathValue, "/_snapshot") ||
		strings.HasPrefix(pathValue, "/_searchable_snapshots") ||
		strings.HasPrefix(pathValue, "/_slm") ||
		strings.HasPrefix(pathValue, "/_ilm") ||
		strings.HasPrefix(pathValue, "/_tasks") ||
		strings.HasPrefix(pathValue, "/_scripts") ||
		strings.HasPrefix(pathValue, "/_autoscaling") ||
		strings.HasPrefix(pathValue, "/_migration") ||
		strings.HasPrefix(pathValue, "/_features") ||
		strings.HasPrefix(pathValue, "/_security") ||
		strings.HasPrefix(pathValue, "/_license") ||
		strings.HasPrefix(pathValue, "/_ml") ||
		strings.HasPrefix(pathValue, "/_watcher") ||
		strings.HasPrefix(pathValue, "/_graph") ||
		strings.HasPrefix(pathValue, "/_ccr")
}

func (p *Proxy) setResponseMode(w http.ResponseWriter, mode string) {
	w.Header().Set(responseModeHeader, mode)
}

func (p *Proxy) isCatIndices(pathValue string) bool {
	segments := splitPath(pathValue)
	return len(segments) == 2 && segments[0] == "_cat" && segments[1] == "indices"
}

func (p *Proxy) modifyResponse(resp *http.Response) error {
	if resp == nil || resp.Request == nil {
		return nil
	}
	if !p.isCatIndices(resp.Request.URL.Path) || resp.Request.Method != http.MethodGet {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if len(body) == 0 {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "application/json") {
		rewritten, err := p.addTenantToCatIndicesJSON(body)
		if err != nil {
			resp.Body = io.NopCloser(bytes.NewReader(body))
			return nil
		}
		p.replaceResponseBody(resp, rewritten)
		return nil
	}
	rewritten := p.addTenantToCatIndicesText(body)
	p.replaceResponseBody(resp, rewritten)
	return nil
}

func (p *Proxy) addTenantToCatIndicesJSON(body []byte) ([]byte, error) {
	var payload []map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	for _, item := range payload {
		indexValue, ok := item["index"].(string)
		if !ok {
			continue
		}
		if tenantID, ok := p.tenantIDForIndex(indexValue); ok {
			item["tenant_id"] = tenantID
		}
	}
	return json.Marshal(payload)
}

func (p *Proxy) addTenantToCatIndicesText(body []byte) []byte {
	text := string(body)
	trailingNewline := strings.HasSuffix(text, "\n")
	trimmed := strings.TrimRight(text, "\n")
	if trimmed == "" {
		return body
	}
	lines := strings.Split(trimmed, "\n")
	headerAdded := false
	for idx, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if !headerAdded && strings.Contains(line, "index") && strings.Contains(line, "health") {
			lines[idx] = line + " TENANT_ID"
			headerAdded = true
			continue
		}
		indexValue := fields[len(fields)-1]
		tenantID, ok := p.tenantIDForIndex(indexValue)
		if ok {
			lines[idx] = line + " " + tenantID
			if !headerAdded {
				headerAdded = true
			}
		} else if headerAdded {
			lines[idx] = line + " -"
		}
	}
	rewritten := strings.Join(lines, "\n")
	if trailingNewline {
		rewritten += "\n"
	}
	return []byte(rewritten)
}

func (p *Proxy) tenantIDForIndex(index string) (string, bool) {
	matches := p.cfg.TenantRegex.Compiled.FindStringSubmatch(index)
	if matches == nil {
		return "", false
	}
	if p.tenantGroup >= len(matches) {
		return "", false
	}
	tenantID := matches[p.tenantGroup]
	if tenantID == "" {
		return "", false
	}
	return tenantID, true
}

func (p *Proxy) replaceResponseBody(resp *http.Response, body []byte) {
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
}
