package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"es-tmnt/internal/config"
)

type capturedRequest struct {
	mu     sync.Mutex
	path   string
	body   []byte
	method string
	count  int
}

func (c *capturedRequest) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.path = r.URL.Path
	c.body = body
	c.method = r.Method
	c.count++
	w.WriteHeader(http.StatusOK)
}

func (c *capturedRequest) snapshot() (path string, body []byte, method string, count int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.path, c.body, c.method, c.count
}

func newProxyWithServer(t *testing.T, cfg config.Config) (*Proxy, *capturedRequest) {
	t.Helper()
	capture := &capturedRequest{}
	server := httptest.NewServer(http.HandlerFunc(capture.handler))
	t.Cleanup(server.Close)
	cfg.UpstreamURL = server.URL
	if cfg.TenantRegex.Compiled == nil {
		compiled, err := regexp.Compile(cfg.TenantRegex.Pattern)
		if err != nil {
			t.Fatalf("compile tenant regex: %v", err)
		}
		cfg.TenantRegex.Compiled = compiled
	}
	proxyHandler, err := New(cfg)
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxyHandler.proxy.Transport = transport
	return proxyHandler, capture
}

func TestSharedIndexSearchRewrite(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"query":{"match":{"field1":"value"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/products-tenant1/_search", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, capturedBody, _, _ := capture.snapshot()
	if path != "/alias-products-tenant1/_search" {
		t.Fatalf("expected path /alias-products-tenant1/_search, got %q", path)
	}
	if string(bytes.TrimSpace(capturedBody)) != string(bytes.TrimSpace(body)) {
		t.Fatalf("expected body unchanged, got %s", string(capturedBody))
	}
}

func TestSharedIndexIndexingRewrite(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.Name = "shared-index"
	cfg.SharedIndex.TenantField = "tenant_id"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	reqBody := []byte(`{"field1":"value"}`)
	req := httptest.NewRequest(http.MethodPut, "/products-tenant1/_doc/1", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, capturedBody, _, _ := capture.snapshot()
	if path != "/shared-index/_doc/1" {
		t.Fatalf("expected path /shared-index/_doc/1, got %q", path)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if payload["tenant_id"] != "tenant1" {
		t.Fatalf("expected tenant_id tenant1, got %v", payload["tenant_id"])
	}
}

func TestIndexPerTenantSearchRewrite(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "shared-index"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	reqBody := []byte(`{"query":{"match":{"field1":"value"}},"sort":["field2"]}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_search", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, capturedBody, _, _ := capture.snapshot()
	if path != "/shared-index/_search" {
		t.Fatalf("expected path /shared-index/_search, got %q", path)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	query := payload["query"].(map[string]interface{})
	match := query["match"].(map[string]interface{})
	if _, ok := match["orders.field1"]; !ok {
		t.Fatalf("expected field orders.field1 in match, got %v", match)
	}
	sort := payload["sort"].([]interface{})
	if sort[0].(string) != "orders.field2" {
		t.Fatalf("expected sort orders.field2, got %v", sort)
	}
}

func TestIndexPerTenantBulkRewrite(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "shared-index"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	bulkPayload := strings.Join([]string{
		`{"index":{"_id":"1"}}`,
		`{"field1":"value"}`,
		"",
	}, "\n")
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_bulk", strings.NewReader(bulkPayload))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	_, capturedBody, _, _ := capture.snapshot()
	lines := strings.Split(strings.TrimSpace(string(capturedBody)), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected bulk payload lines, got %v", lines)
	}
	var action map[string]map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &action); err != nil {
		t.Fatalf("parse bulk action: %v", err)
	}
	indexMeta := action["index"]
	if indexMeta["_index"] != "shared-index" {
		t.Fatalf("expected _index shared-index, got %v", indexMeta["_index"])
	}
	var source map[string]interface{}
	if err := json.Unmarshal([]byte(lines[1]), &source); err != nil {
		t.Fatalf("parse bulk source: %v", err)
	}
	if _, ok := source["orders"]; !ok {
		t.Fatalf("expected orders wrapper in bulk source, got %v", source)
	}
}

func TestSharedIndexCreateRewrite(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.Name = "shared-{{.index}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"mappings":{"properties":{"field1":{"type":"keyword"}}}}`)
	req := httptest.NewRequest(http.MethodPut, "/products-tenant1", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, capturedBody, method, _ := capture.snapshot()
	if method != http.MethodPut {
		t.Fatalf("expected method PUT, got %s", method)
	}
	if path != "/shared-products" {
		t.Fatalf("expected path /shared-products, got %q", path)
	}
	if string(bytes.TrimSpace(capturedBody)) != string(bytes.TrimSpace(body)) {
		t.Fatalf("expected body unchanged, got %s", string(capturedBody))
	}
}

func TestIndexPerTenantMappingRewrite(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "{{.index}}-{{.tenant}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"properties":{"field1":{"type":"keyword"}}}`)
	req := httptest.NewRequest(http.MethodPut, "/orders-tenant2/_mapping", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, capturedBody, _, _ := capture.snapshot()
	if path != "/orders-tenant2/_mapping" {
		t.Fatalf("expected path /orders-tenant2/_mapping, got %q", path)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	props := payload["properties"].(map[string]interface{})
	nested := props["orders"].(map[string]interface{})
	if _, ok := nested["properties"].(map[string]interface{})["field1"]; !ok {
		t.Fatalf("expected nested mapping for field1, got %v", nested)
	}
}

func TestIndexPerTenantDeleteRewrite(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "shared-{{.tenant}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodDelete, "/orders-tenant2", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, method, _ := capture.snapshot()
	if method != http.MethodDelete {
		t.Fatalf("expected method DELETE, got %s", method)
	}
	if path != "/shared-tenant2" {
		t.Fatalf("expected path /shared-tenant2, got %q", path)
	}
}

func TestClusterPassthrough(t *testing.T) {
	cfg := config.Default()
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_cluster/health", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, _, count := capture.snapshot()
	if count != 1 {
		t.Fatalf("expected upstream call, got %d", count)
	}
	if path != "/_cluster/health" {
		t.Fatalf("expected path /_cluster/health, got %q", path)
	}
}

func TestUnsupportedRequestReturnsError(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/products-tenant1/_source/1", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
	_, _, _, count := capture.snapshot()
	if count != 0 {
		t.Fatalf("expected no upstream calls, got %d", count)
	}
}

func TestIndexPassthroughSettingsRewrite(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.Name = "shared-{{.index}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/products-tenant1/_settings", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, method, _ := capture.snapshot()
	if method != http.MethodGet {
		t.Fatalf("expected method GET, got %s", method)
	}
	if path != "/shared-products/_settings" {
		t.Fatalf("expected path /shared-products/_settings, got %q", path)
	}
}

func TestGetRequestRewritesToSearch(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/products-tenant1/_get/42", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, capturedBody, method, _ := capture.snapshot()
	if method != http.MethodPost {
		t.Fatalf("expected method POST, got %s", method)
	}
	if path != "/alias-products-tenant1/_search" {
		t.Fatalf("expected path /alias-products-tenant1/_search, got %q", path)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	query := payload["query"].(map[string]interface{})
	ids := query["ids"].(map[string]interface{})["values"].([]interface{})
	if ids[0].(string) != "42" {
		t.Fatalf("expected id 42, got %v", ids)
	}
}

func TestMgetRequestRewritesToSearch(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "shared-index"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"ids":["1","2"]}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_mget", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, capturedBody, method, _ := capture.snapshot()
	if method != http.MethodPost {
		t.Fatalf("expected method POST, got %s", method)
	}
	if path != "/shared-index/_search" {
		t.Fatalf("expected path /shared-index/_search, got %q", path)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if payload["size"].(float64) != 2 {
		t.Fatalf("expected size 2, got %v", payload["size"])
	}
}

func TestDeleteByQueryRewritesQuery(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "shared-index"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"query":{"match":{"field1":"value"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_delete_by_query", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, capturedBody, _, _ := capture.snapshot()
	if path != "/shared-index/_delete_by_query" {
		t.Fatalf("expected path /shared-index/_delete_by_query, got %q", path)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	query := payload["query"].(map[string]interface{})
	match := query["match"].(map[string]interface{})
	if _, ok := match["orders.field1"]; !ok {
		t.Fatalf("expected field orders.field1 in match, got %v", match)
	}
}
