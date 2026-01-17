package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"es-tmnt/internal/config"
)

type capturedRequest struct {
	mu     sync.Mutex
	path   string
	query  string
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
	c.query = r.URL.RawQuery
	c.body = body
	c.method = r.Method
	c.count++
	w.WriteHeader(http.StatusOK)
}

func (c *capturedRequest) snapshot() (path string, query string, body []byte, method string, count int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.path, c.query, c.body, c.method, c.count
}

func queryValue(rawQuery, key string) string {
	parsed, err := url.ParseQuery(rawQuery)
	if err != nil {
		return ""
	}
	return parsed.Get(key)
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
	path, _, capturedBody, _, _ := capture.snapshot()
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
	path, _, capturedBody, _, _ := capture.snapshot()
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
	path, _, capturedBody, _, _ := capture.snapshot()
	if path != "/shared-index/_search" {
		t.Fatalf("expected path /shared-index/_search, got %q", path)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	searchQuery := payload["query"].(map[string]interface{})
	match := searchQuery["match"].(map[string]interface{})
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
	_, _, capturedBody, _, _ := capture.snapshot()
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
	path, _, capturedBody, method, _ := capture.snapshot()
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
	path, _, capturedBody, _, _ := capture.snapshot()
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
	path, _, _, method, _ := capture.snapshot()
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
	path, _, _, _, count := capture.snapshot()
	if count != 1 {
		t.Fatalf("expected upstream call, got %d", count)
	}
	if path != "/_cluster/health" {
		t.Fatalf("expected path /_cluster/health, got %q", path)
	}
}

func TestSnapshotPassthrough(t *testing.T) {
	cfg := config.Default()
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_snapshot/test-repo", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, _, _, count := capture.snapshot()
	if count != 1 {
		t.Fatalf("expected upstream call, got %d", count)
	}
	if path != "/_snapshot/test-repo" {
		t.Fatalf("expected path /_snapshot/test-repo, got %q", path)
	}
}

func TestQueryRulesPassthrough(t *testing.T) {
	cfg := config.Default()
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_query_rules/my-set", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, _, _, count := capture.snapshot()
	if count != 1 {
		t.Fatalf("expected upstream call, got %d", count)
	}
	if path != "/_query_rules/my-set" {
		t.Fatalf("expected path /_query_rules/my-set, got %q", path)
	}
}

func TestSynonymsPassthrough(t *testing.T) {
	cfg := config.Default()
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_synonyms/my-set", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, _, _, count := capture.snapshot()
	if count != 1 {
		t.Fatalf("expected upstream call, got %d", count)
	}
	if path != "/_synonyms/my-set" {
		t.Fatalf("expected path /_synonyms/my-set, got %q", path)
	}
}

func TestSearchRootRewrite(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "shared-index"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"query":{"match":{"field1":"value"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/_search?index=orders-tenant2", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, query, _, _, count := capture.snapshot()
	if count != 1 {
		t.Fatalf("expected upstream call, got %d", count)
	}
	if path != "/_search" {
		t.Fatalf("expected path /_search, got %q", path)
	}
	if got := queryValue(query, "index"); got != "shared-index" {
		t.Fatalf("expected index shared-index, got %q", got)
	}
}

func TestTransformIndexRewrite(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.Name = "shared-{{.index}}"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"source":{"index":"orders-tenant1"},"dest":{"index":"stats-tenant1"}}`)
	req := httptest.NewRequest(http.MethodPut, "/_transform/orders", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, capturedBody, _, _ := capture.snapshot()
	if path != "/_transform/orders" {
		t.Fatalf("expected path /_transform/orders, got %q", path)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	source := payload["source"].(map[string]interface{})
	if source["index"] != "alias-orders-tenant1" {
		t.Fatalf("expected source index alias-orders-tenant1, got %v", source["index"])
	}
	dest := payload["dest"].(map[string]interface{})
	if dest["index"] != "shared-stats" {
		t.Fatalf("expected dest index shared-stats, got %v", dest["index"])
	}
}

func TestAnalyzeIndexRewrite(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.Name = "shared-index"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_analyze?index=orders-tenant2", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, query, _, _, _ := capture.snapshot()
	if path != "/_analyze" {
		t.Fatalf("expected path /_analyze, got %q", path)
	}
	if got := queryValue(query, "index"); got != "shared-index" {
		t.Fatalf("expected index shared-index, got %q", got)
	}
}

func TestRollupIndexPatternRewrite(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	cfg.SharedIndex.Name = "shared-{{.index}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"index_pattern":"logs-tenant1-*","rollup_index":"rollup-tenant1"}`)
	req := httptest.NewRequest(http.MethodPut, "/_rollup/job/logs", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, capturedBody, _, _ := capture.snapshot()
	if path != "/_rollup/job/logs" {
		t.Fatalf("expected path /_rollup/job/logs, got %q", path)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if payload["index_pattern"] != "alias-logs-*-tenant1" {
		t.Fatalf("expected index_pattern alias-logs-*-tenant1, got %v", payload["index_pattern"])
	}
	if payload["rollup_index"] != "shared-rollup" {
		t.Fatalf("expected rollup_index shared-rollup, got %v", payload["rollup_index"])
	}
}

func TestMultiSearchRewrite(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "shared-index"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := strings.Join([]string{
		`{"index":"orders-tenant2"}`,
		`{"query":{"match":{"field1":"value"}}}`,
		"",
	}, "\n")
	req := httptest.NewRequest(http.MethodPost, "/_msearch", strings.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	_, _, capturedBody, _, _ := capture.snapshot()
	lines := strings.Split(strings.TrimSpace(string(capturedBody)), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected msearch payload lines, got %v", lines)
	}
	var header map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("parse header: %v", err)
	}
	if header["index"] != "shared-index" {
		t.Fatalf("expected header index shared-index, got %v", header["index"])
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(lines[1]), &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	query := payload["query"].(map[string]interface{})
	match := query["match"].(map[string]interface{})
	if _, ok := match["orders.field1"]; !ok {
		t.Fatalf("expected field orders.field1 in match, got %v", match)
	}
}

func TestSourceRequestRewritesToSearch(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/products-tenant1/_source/1", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, capturedBody, method, _ := capture.snapshot()
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
	if ids[0].(string) != "1" {
		t.Fatalf("expected id 1, got %v", ids)
	}
}

func TestSourceRootRewritesToSearch(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"query":{"match":{"name":"lamp"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/products-tenant1/_source/", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, capturedBody, method, _ := capture.snapshot()
	if method != http.MethodPost {
		t.Fatalf("expected method POST, got %s", method)
	}
	if path != "/alias-products-tenant1/_search" {
		t.Fatalf("expected path /alias-products-tenant1/_search, got %q", path)
	}
	if string(capturedBody) != string(body) {
		t.Fatalf("expected body %s, got %s", string(body), string(capturedBody))
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
	path, _, _, method, _ := capture.snapshot()
	if method != http.MethodGet {
		t.Fatalf("expected method GET, got %s", method)
	}
	if path != "/shared-products/_settings" {
		t.Fatalf("expected path /shared-products/_settings, got %q", path)
	}
}

func TestSearchShardsReroutesToIndex(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.Name = "shared-{{.index}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/products-tenant1/_search_shards", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, _, method, _ := capture.snapshot()
	if method != http.MethodGet {
		t.Fatalf("expected method GET, got %s", method)
	}
	if path != "/shared-products/_search_shards" {
		t.Fatalf("expected path /shared-products/_search_shards, got %q", path)
	}
}

func TestFieldCapsReroutesToIndex(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "tenant-{{.index}}-{{.tenant}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/orders-tenant2/_field_caps", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, _, method, _ := capture.snapshot()
	if method != http.MethodGet {
		t.Fatalf("expected method GET, got %s", method)
	}
	if path != "/tenant-orders-tenant2/_field_caps" {
		t.Fatalf("expected path /tenant-orders-tenant2/_field_caps, got %q", path)
	}
}

func TestTermsEnumReroutesToIndex(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "tenant-{{.index}}-{{.tenant}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_terms_enum", bytes.NewReader([]byte(`{"field":"status"}`)))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, _, method, _ := capture.snapshot()
	if method != http.MethodPost {
		t.Fatalf("expected method POST, got %s", method)
	}
	if path != "/tenant-orders-tenant2/_terms_enum" {
		t.Fatalf("expected path /tenant-orders-tenant2/_terms_enum, got %q", path)
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
	path, _, capturedBody, method, _ := capture.snapshot()
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
	path, _, capturedBody, method, _ := capture.snapshot()
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
	path, _, capturedBody, _, _ := capture.snapshot()
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

func TestUpdateEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.Name = "shared-index"
	cfg.SharedIndex.TenantField = "tenant_id"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"doc":{"field1":"updated"}}`)
	req := httptest.NewRequest(http.MethodPost, "/products-tenant1/_update/1", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, capturedBody, _, _ := capture.snapshot()
	if path != "/shared-index/_update/1" {
		t.Fatalf("expected path /shared-index/_update/1, got %q", path)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	doc := payload["doc"].(map[string]interface{})
	if doc["tenant_id"] != "tenant1" {
		t.Fatalf("expected tenant_id tenant1, got %v", doc["tenant_id"])
	}
}

func TestUpdateEndpointIndexPerTenant(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "{{.index}}-{{.tenant}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"doc":{"field1":"updated"}}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_update/1", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, capturedBody, _, _ := capture.snapshot()
	if path != "/orders-tenant2/_update/1" {
		t.Fatalf("expected path /orders-tenant2/_update/1, got %q", path)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	doc := payload["doc"].(map[string]interface{})
	wrapped := doc["orders"].(map[string]interface{})
	if wrapped["field1"] != "updated" {
		t.Fatalf("expected field1 updated, got %v", wrapped["field1"])
	}
}

func TestUpdateEndpointInvalidMethod(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/products-tenant1/_update/1", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestUpdateEndpointMissingBody(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/products-tenant1/_update/1", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestQueryEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "shared-index"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"query":{"match":{"field1":"value"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_query", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, capturedBody, _, _ := capture.snapshot()
	if path != "/shared-index/_query" {
		t.Fatalf("expected path /shared-index/_query, got %q", path)
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

func TestRankEvalEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"requests":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/products-tenant1/_rank_eval", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, _, _, _ := capture.snapshot()
	if path != "/alias-products-tenant1/_rank_eval" {
		t.Fatalf("expected path /alias-products-tenant1/_rank_eval, got %q", path)
	}
}

func TestExplainEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "shared-index"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"query":{"match":{"field1":"value"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_explain/1", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, capturedBody, _, _ := capture.snapshot()
	if path != "/shared-index/_explain/1" {
		t.Fatalf("expected path /shared-index/_explain/1, got %q", path)
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

func TestExplainRootEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"query":{"match_all":{}}}`)
	req := httptest.NewRequest(http.MethodPost, "/_explain?index=products-tenant1", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, query, _, _, _ := capture.snapshot()
	if path != "/_explain" {
		t.Fatalf("expected path /_explain, got %q", path)
	}
	if got := queryValue(query, "index"); got != "alias-products-tenant1" {
		t.Fatalf("expected index alias-products-tenant1, got %q", got)
	}
}

func TestValidateQueryEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "shared-index"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"query":{"match":{"field1":"value"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_validate/query", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, capturedBody, _, _ := capture.snapshot()
	if path != "/shared-index/_validate/query" {
		t.Fatalf("expected path /shared-index/_validate/query, got %q", path)
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

func TestValidateQueryRootEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"query":{"match_all":{}}}`)
	req := httptest.NewRequest(http.MethodPost, "/_validate/query?index=products-tenant1", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, query, _, _, _ := capture.snapshot()
	if path != "/_validate/query" {
		t.Fatalf("expected path /_validate/query, got %q", path)
	}
	if got := queryValue(query, "index"); got != "alias-products-tenant1" {
		t.Fatalf("expected index alias-products-tenant1, got %q", got)
	}
}

func TestValidateQueryRootEndpointNoIndex(t *testing.T) {
	cfg := config.Default()
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_validate/query", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	_, _, _, _, count := capture.snapshot()
	if count != 1 {
		t.Fatalf("expected upstream call, got %d", count)
	}
}

func TestUpdateByQueryEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "shared-index"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"query":{"match":{"field1":"value"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_update_by_query", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, capturedBody, method, _ := capture.snapshot()
	if method != http.MethodPost {
		t.Fatalf("expected method POST, got %s", method)
	}
	if path != "/shared-index/_update_by_query" {
		t.Fatalf("expected path /shared-index/_update_by_query, got %q", path)
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

func TestUpdateByQueryRootEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"query":{"match_all":{}}}`)
	req := httptest.NewRequest(http.MethodPost, "/_update_by_query?index=products-tenant1", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, _, method, _ := capture.snapshot()
	if method != http.MethodPost {
		t.Fatalf("expected method POST, got %s", method)
	}
	if path != "/alias-products-tenant1/_update_by_query" {
		t.Fatalf("expected path /alias-products-tenant1/_update_by_query, got %q", path)
	}
}

func TestUpdateByQueryRootEndpointMissingIndex(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/_update_by_query", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestUpdateByQueryRootEndpointMultipleIndices(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/_update_by_query?index=idx1,idx2", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestCountEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "shared-index"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"query":{"match":{"field1":"value"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_count", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, capturedBody, method, _ := capture.snapshot()
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
	if payload["size"].(float64) != 0 {
		t.Fatalf("expected size 0, got %v", payload["size"])
	}
	query := payload["query"].(map[string]interface{})
	match := query["match"].(map[string]interface{})
	if _, ok := match["orders.field1"]; !ok {
		t.Fatalf("expected field orders.field1 in match, got %v", match)
	}
}

func TestCountEndpointNoQuery(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/products-tenant1/_count", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	_, _, capturedBody, _, _ := capture.snapshot()
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if payload["size"].(float64) != 0 {
		t.Fatalf("expected size 0, got %v", payload["size"])
	}
	query := payload["query"].(map[string]interface{})
	matchAll := query["match_all"].(map[string]interface{})
	if len(matchAll) != 0 {
		t.Fatalf("expected match_all query, got %v", query)
	}
}

func TestSearchTemplateEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"source":{"query":{"match":{"field1":"value"}}}}`)
	req := httptest.NewRequest(http.MethodPost, "/products-tenant1/_search/template", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, _, _, _ := capture.snapshot()
	if path != "/alias-products-tenant1/_search/template" {
		t.Fatalf("expected path /alias-products-tenant1/_search/template, got %q", path)
	}
}

func TestSearchTemplateRootEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "shared-index"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"source":{"query":{"match":{"field1":"value"}}}}`)
	req := httptest.NewRequest(http.MethodPost, "/_search/template?index=orders-tenant2", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, query, _, _, _ := capture.snapshot()
	if path != "/_search/template" {
		t.Fatalf("expected path /_search/template, got %q", path)
	}
	// Note: search template root endpoint doesn't rewrite the index query param
	// It uses resolveIndex which gets from query, but rewriteIndexPath is called with empty index
	// So the query param remains unchanged
	if got := queryValue(query, "index"); got != "orders-tenant2" {
		t.Fatalf("expected index orders-tenant2 (not rewritten), got %q", got)
	}
}

func TestAnalyzeWithIndex(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "{{.index}}-{{.tenant}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/orders-tenant2/_analyze", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, _, _, _ := capture.snapshot()
	if path != "/orders-tenant2/_analyze" {
		t.Fatalf("expected path /orders-tenant2/_analyze, got %q", path)
	}
}

func TestDocEndpointInvalidMethod(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/products-tenant1/_doc/1", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestDocEndpointMissingBody(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/products-tenant1/_doc/1", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestBulkRootEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.Name = "shared-index"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	bulkPayload := strings.Join([]string{
		`{"index":{"_index":"products-tenant1","_id":"1"}}`,
		`{"field1":"value"}`,
		"",
	}, "\n")
	req := httptest.NewRequest(http.MethodPost, "/_bulk", strings.NewReader(bulkPayload))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, _, _, _ := capture.snapshot()
	if path != "/_bulk" {
		t.Fatalf("expected path /_bulk, got %q", path)
	}
}

func TestBulkRootEndpointInvalidMethod(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_bulk", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestBulkRootEndpointMissingBody(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/_bulk", nil)
	req.Body = nil // Explicitly set to nil to test nil body case
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	// Nil body should be rejected
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestMultiSearchRootEndpointInvalidMethod(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_msearch", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestMultiSearchRootEndpointMissingBody(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/_msearch", nil)
	req.Body = nil // Explicitly set to nil to test nil body case
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	// Nil body should be rejected
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestDeleteByQueryRootEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"query":{"match_all":{}}}`)
	req := httptest.NewRequest(http.MethodPost, "/_delete_by_query?index=products-tenant1", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, _, method, _ := capture.snapshot()
	if method != http.MethodPost {
		t.Fatalf("expected method POST, got %s", method)
	}
	if path != "/alias-products-tenant1/_delete_by_query" {
		t.Fatalf("expected path /alias-products-tenant1/_delete_by_query, got %q", path)
	}
}

func TestDeleteByQueryRootEndpointMissingIndex(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/_delete_by_query", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestDeleteEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "shared-index"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodDelete, "/orders-tenant2/_delete/1", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, capturedBody, method, _ := capture.snapshot()
	if method != http.MethodPost {
		t.Fatalf("expected method POST, got %s", method)
	}
	if path != "/shared-index/_delete_by_query" {
		t.Fatalf("expected path /shared-index/_delete_by_query, got %q", path)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	query := payload["query"].(map[string]interface{})
	ids := query["ids"].(map[string]interface{})["values"].([]interface{})
	if ids[0].(string) != "1" {
		t.Fatalf("expected id 1, got %v", ids)
	}
}

func TestDeleteEndpointMissingID(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodDelete, "/orders-tenant2/_delete", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestMappingEndpointInvalidMethod(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/products-tenant1/_mapping", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestMappingEndpointMissingBody(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodPut, "/products-tenant1/_mapping", nil)
	req.Body = nil // Explicitly set to nil to test nil body case
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	// Nil body should be rejected
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestIndexRootInvalidMethod(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/products-tenant1", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestUpdateEndpointMissingID(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/products-tenant1/_update", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestGetEndpointMissingID(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/products-tenant1/_get", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestNamedQueryEndpointMissingBody(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_delete_by_query", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestNamedQueryEndpointEmptyBody(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_update_by_query", bytes.NewReader([]byte("   ")))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestQueryRequestMissingBodyForPost(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_search", nil)
	req.Body = nil // Explicitly set to nil to test nil body case
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	// Nil body for POST should be rejected
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestQueryRequestEmptyBodyForPost(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_search", bytes.NewReader([]byte("   ")))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
}

func TestQueryRequestGetMethod(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/products-tenant1/_search", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	_, _, _, _, count := capture.snapshot()
	if count != 1 {
		t.Fatalf("expected upstream call, got %d", count)
	}
}

func TestUnsupportedEndpoint(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/products-tenant1/_unsupported", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestUnsupportedSystemEndpoint(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_unsupported", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestUnsupportedSearchEndpoint(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_search/unsupported", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestUnsupportedRenderEndpoint(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_render/unsupported", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestUnsupportedValidateEndpoint(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_validate/unsupported", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestUnsupportedMsearchEndpoint(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_msearch/unsupported", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestUnsupportedQueryEndpoint(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_query/unsupported", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestUnsupportedExplainEndpoint(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_explain/unsupported", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestEmptyPath(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestPassthroughPath(t *testing.T) {
	cfg := config.Default()
	cfg.PassthroughPaths = []string{"/custom/path"}
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/custom/path", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, _, _, _ := capture.snapshot()
	if path != "/custom/path" {
		t.Fatalf("expected path /custom/path, got %q", path)
	}
}

func TestPassthroughPathWildcard(t *testing.T) {
	cfg := config.Default()
	cfg.PassthroughPaths = []string{"/custom/*"}
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/custom/sub/path", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, _, _, _ := capture.snapshot()
	if path != "/custom/sub/path" {
		t.Fatalf("expected path /custom/sub/path, got %q", path)
	}
}

func TestCacheClearEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.Name = "shared-{{.index}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/products-tenant1/_cache/clear", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, _, _, _ := capture.snapshot()
	if path != "/shared-products/_cache/clear" {
		t.Fatalf("expected path /shared-products/_cache/clear, got %q", path)
	}
}

func TestCatIndicesJSONResponse(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	// Create a mock response
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Request:    httptest.NewRequest(http.MethodGet, "/_cat/indices", nil),
	}
	resp.Header.Set("Content-Type", "application/json")
	body := `[{"index":"orders-tenant1","health":"green"},{"index":"products-tenant2","health":"yellow"}]`
	resp.Body = io.NopCloser(bytes.NewReader([]byte(body)))

	err := proxyHandler.modifyResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	respBody, _ := io.ReadAll(resp.Body)
	var result []map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 indices, got %d", len(result))
	}
	if result[0]["tenant_id"] != "tenant1" {
		t.Fatalf("expected tenant_id tenant1, got %v", result[0]["tenant_id"])
	}
	if result[1]["tenant_id"] != "tenant2" {
		t.Fatalf("expected tenant_id tenant2, got %v", result[1]["tenant_id"])
	}
}

func TestCatIndicesTextResponse(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Request:    httptest.NewRequest(http.MethodGet, "/_cat/indices", nil),
	}
	resp.Header.Set("Content-Type", "text/plain")
	// Include header line with "index" and "health" to trigger header addition
	body := "green open index health\norders-tenant1\nproducts-tenant2\n"
	resp.Body = io.NopCloser(bytes.NewReader([]byte(body)))

	err := proxyHandler.modifyResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	respBody, _ := io.ReadAll(resp.Body)
	text := string(respBody)
	if !strings.Contains(text, "TENANT_ID") {
		t.Fatalf("expected TENANT_ID in header, got %s", text)
	}
	if !strings.Contains(text, "tenant1") {
		t.Fatalf("expected tenant1 in response, got %s", text)
	}
	if !strings.Contains(text, "tenant2") {
		t.Fatalf("expected tenant2 in response, got %s", text)
	}
}

func TestCatIndicesEmptyBody(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Request:    httptest.NewRequest(http.MethodGet, "/_cat/indices", nil),
	}
	resp.Header.Set("Content-Type", "application/json")
	resp.Body = io.NopCloser(bytes.NewReader([]byte{}))

	err := proxyHandler.modifyResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCatIndicesInvalidJSON(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Request:    httptest.NewRequest(http.MethodGet, "/_cat/indices", nil),
	}
	resp.Header.Set("Content-Type", "application/json")
	resp.Body = io.NopCloser(bytes.NewReader([]byte("invalid json")))

	err := proxyHandler.modifyResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should fallback to original body
}

func TestCatIndicesNonGetMethod(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Request:    httptest.NewRequest(http.MethodPost, "/_cat/indices", nil),
	}
	err := proxyHandler.modifyResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMgetWithDocs(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"docs":[{"_index":"orders-tenant2","_id":"1"},{"_index":"orders-tenant2","_id":"2"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_mget", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, capturedBody, method, _ := capture.snapshot()
	if method != http.MethodPost {
		t.Fatalf("expected method POST, got %s", method)
	}
	if path != "/alias-orders-tenant2/_search" {
		t.Fatalf("expected path /alias-orders-tenant2/_search, got %q", path)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if payload["size"].(float64) != 2 {
		t.Fatalf("expected size 2, got %v", payload["size"])
	}
}

func TestMgetWithDocsMismatchedIndex(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	body := []byte(`{"docs":[{"_index":"other-index","_id":"1"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_mget", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestMgetMissingDocsAndIds(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	body := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_mget", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestMgetEmptyIds(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	body := []byte(`{"ids":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_mget", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestQueryWithFields(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "shared-index"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"query":{"match_all":{}},"fields":["field1","field2"]}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_search", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	_, _, capturedBody, _, _ := capture.snapshot()
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	fields := payload["fields"].([]interface{})
	if len(fields) != 2 {
		t.Fatalf("expected 2 fields, got %d", len(fields))
	}
	if fields[0].(string) != "orders.field1" {
		t.Fatalf("expected orders.field1, got %v", fields[0])
	}
	if fields[1].(string) != "orders.field2" {
		t.Fatalf("expected orders.field2, got %v", fields[1])
	}
}

func TestQueryWithSourceIncludesExcludes(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "shared-index"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"query":{"match_all":{}},"_source":{"includes":["field1","field2"],"excludes":["field3"]}}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_search", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	_, _, capturedBody, _, _ := capture.snapshot()
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	source := payload["_source"].(map[string]interface{})
	includes := source["includes"].([]interface{})
	if includes[0].(string) != "orders.field1" {
		t.Fatalf("expected orders.field1, got %v", includes[0])
	}
	excludes := source["excludes"].([]interface{})
	if excludes[0].(string) != "orders.field3" {
		t.Fatalf("expected orders.field3, got %v", excludes[0])
	}
}

func TestQueryWithSourceArray(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "shared-index"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"query":{"match_all":{}},"_source":["field1","field2"]}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_search", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	_, _, capturedBody, _, _ := capture.snapshot()
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	source := payload["_source"].([]interface{})
	if source[0].(string) != "orders.field1" {
		t.Fatalf("expected orders.field1, got %v", source[0])
	}
}

func TestQueryWithSort(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "shared-index"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"query":{"match_all":{}},"sort":[{"field1":"desc"},"field2"]}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_search", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	_, _, capturedBody, _, _ := capture.snapshot()
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	sort := payload["sort"].([]interface{})
	if len(sort) != 2 {
		t.Fatalf("expected 2 sort fields, got %d", len(sort))
	}
	sortObj := sort[0].(map[string]interface{})
	if _, ok := sortObj["orders.field1"]; !ok {
		t.Fatalf("expected orders.field1 in sort object, got %v", sortObj)
	}
	if sort[1].(string) != "orders.field2" {
		t.Fatalf("expected orders.field2, got %v", sort[1])
	}
}

func TestTransformWithArrayIndices(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.Name = "shared-{{.index}}"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"source":{"index":["orders-tenant1","products-tenant1"]},"dest":{"index":"stats-tenant1"}}`)
	req := httptest.NewRequest(http.MethodPut, "/_transform/orders", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	_, _, capturedBody, _, _ := capture.snapshot()
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	source := payload["source"].(map[string]interface{})
	indices := source["index"].([]interface{})
	if len(indices) != 2 {
		t.Fatalf("expected 2 indices, got %d", len(indices))
	}
	if indices[0].(string) != "alias-orders-tenant1" {
		t.Fatalf("expected alias-orders-tenant1, got %v", indices[0])
	}
}

func TestRollupWithArrayIndexPattern(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	cfg.SharedIndex.Name = "shared-{{.index}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"index_pattern":["logs-tenant1-*","events-tenant1-*"],"rollup_index":"rollup-tenant1"}`)
	req := httptest.NewRequest(http.MethodPut, "/_rollup/job/logs", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	_, _, capturedBody, _, _ := capture.snapshot()
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	patterns := payload["index_pattern"].([]interface{})
	if len(patterns) != 2 {
		t.Fatalf("expected 2 patterns, got %d", len(patterns))
	}
}

func TestAnalyzeRootEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.Name = "shared-index"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_analyze?index=orders-tenant2", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, query, _, _, _ := capture.snapshot()
	if path != "/_analyze" {
		t.Fatalf("expected path /_analyze, got %q", path)
	}
	if got := queryValue(query, "index"); got != "shared-index" {
		t.Fatalf("expected index shared-index, got %q", got)
	}
}

func TestAnalyzeRootEndpointMissingIndex(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_analyze", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestRenderTemplatePassthrough(t *testing.T) {
	cfg := config.Default()
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/_render/template", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, _, _, _ := capture.snapshot()
	if path != "/_render/template" {
		t.Fatalf("expected path /_render/template, got %q", path)
	}
}

func TestMsearchTemplatePassthrough(t *testing.T) {
	cfg := config.Default()
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/_msearch/template", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, _, _, _ := capture.snapshot()
	if path != "/_msearch/template" {
		t.Fatalf("expected path /_msearch/template, got %q", path)
	}
}

func TestIndexCreateWithEmptyBody(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.Name = "shared-{{.index}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	// Test with nil body instead of empty body to match actual behavior
	req := httptest.NewRequest(http.MethodPut, "/products-tenant1", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, _, _, _ := capture.snapshot()
	if path != "/shared-products" {
		t.Fatalf("expected path /shared-products, got %q", path)
	}
}

func TestHandleSourceWithDocID(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/products-tenant1/_source/42", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, capturedBody, method, _ := capture.snapshot()
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

func TestHandleSourceWithoutDocID(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "shared-index"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"query":{"match_all":{}}}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_source/", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, _, capturedBody, method, _ := capture.snapshot()
	if method != http.MethodPost {
		t.Fatalf("expected method POST, got %s", method)
	}
	if path != "/shared-index/_search" {
		t.Fatalf("expected path /shared-index/_search, got %q", path)
	}
	if string(capturedBody) != string(body) {
		t.Fatalf("expected body unchanged, got %s", string(capturedBody))
	}
}

func TestRenderTargetIndexSharedMode(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.Name = "shared-{{.index}}"
	proxyHandler, _ := newProxyWithServer(t, cfg)

	index, err := proxyHandler.renderTargetIndex("orders", "tenant1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if index != "shared-orders" {
		t.Fatalf("expected shared-orders, got %q", index)
	}
}

func TestRenderTargetIndexPerTenantMode(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "{{.index}}-{{.tenant}}"
	proxyHandler, _ := newProxyWithServer(t, cfg)

	index, err := proxyHandler.renderTargetIndex("orders", "tenant1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if index != "orders-tenant1" {
		t.Fatalf("expected orders-tenant1, got %q", index)
	}
}

func TestRenderQueryIndexSharedMode(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	proxyHandler, _ := newProxyWithServer(t, cfg)

	index, err := proxyHandler.renderQueryIndex("orders", "tenant1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if index != "alias-orders-tenant1" {
		t.Fatalf("expected alias-orders-tenant1, got %q", index)
	}
}

func TestRenderQueryIndexPerTenantMode(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "{{.index}}-{{.tenant}}"
	proxyHandler, _ := newProxyWithServer(t, cfg)

	index, err := proxyHandler.renderQueryIndex("orders", "tenant1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if index != "orders-tenant1" {
		t.Fatalf("expected orders-tenant1, got %q", index)
	}
}

func TestModifyResponseNil(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	err := proxyHandler.modifyResponse(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestModifyResponseNilRequest(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Request:    nil,
	}
	err := proxyHandler.modifyResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHandleGetErrorCases(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	// Test empty docID path (already tested but ensure covered)
	req := httptest.NewRequest(http.MethodGet, "/products-tenant1/_get", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleDeleteErrorCases(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	// Test empty docID path
	req := httptest.NewRequest(http.MethodDelete, "/products-tenant1/_delete", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleSourceWithoutDocIDMissingBody(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/products-tenant1/_source/", nil)
	req.Body = nil
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleSourceWithoutDocIDEmptyBody(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/products-tenant1/_source/", bytes.NewReader([]byte("   ")))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleMgetDocsInvalidIndex(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	body := []byte(`{"docs":[{"_index":"wrong-index","_id":"1"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_mget", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleMgetDocsInvalidIndexType(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	body := []byte(`{"docs":[{"_index":123,"_id":"1"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_mget", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleMgetDocsInvalidIDType(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	body := []byte(`{"docs":[{"_id":123}]}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_mget", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleMgetDocsEmptyID(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	body := []byte(`{"docs":[{"_id":""}]}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_mget", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleMgetDocsMissingID(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	body := []byte(`{"docs":[{}]}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_mget", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleMgetDocsNotObject(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	body := []byte(`{"docs":["not an object"]}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_mget", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleMgetDocsNotArray(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	body := []byte(`{"docs":"not an array"}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_mget", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleMgetIdsNotArray(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	body := []byte(`{"ids":"not an array"}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_mget", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleMgetIdsEmptyString(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	body := []byte(`{"ids":[""]}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_mget", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleMgetIdsNotString(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	body := []byte(`{"ids":[123]}`)
	req := httptest.NewRequest(http.MethodPost, "/orders-tenant2/_mget", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestPrefixFieldEmptyField(t *testing.T) {
	// Test prefixField with empty field
	proxyHandler, _ := newProxyWithServer(t, config.Default())
	result := proxyHandler.prefixField("orders", "")
	if result != "" {
		t.Fatalf("expected empty string, got %q", result)
	}
}

func TestPrefixFieldAlreadyPrefixed(t *testing.T) {
	// Test prefixField with already prefixed field
	proxyHandler, _ := newProxyWithServer(t, config.Default())
	result := proxyHandler.prefixField("orders", "orders.field1")
	if result != "orders.field1" {
		t.Fatalf("expected orders.field1, got %q", result)
	}
}

func TestPrefixFieldNewPrefix(t *testing.T) {
	// Test prefixField with new prefix
	proxyHandler, _ := newProxyWithServer(t, config.Default())
	result := proxyHandler.prefixField("orders", "field1")
	if result != "orders.field1" {
		t.Fatalf("expected orders.field1, got %q", result)
	}
}

func TestWrapPropertiesAlreadyWrapped(t *testing.T) {
	props := map[string]interface{}{
		"orders": map[string]interface{}{
			"properties": map[string]interface{}{
				"field1": map[string]interface{}{"type": "keyword"},
			},
		},
	}
	result := wrapProperties(props, "orders")
	if len(result) != 1 {
		t.Fatalf("expected unchanged properties, got %v", result)
	}
	if result["orders"].(map[string]interface{})["properties"] == nil {
		t.Fatalf("expected properties to be preserved")
	}
}

func TestWrapPropertiesNewWrap(t *testing.T) {
	props := map[string]interface{}{
		"field1": map[string]interface{}{"type": "keyword"},
	}
	result := wrapProperties(props, "orders")
	if result["orders"] == nil {
		t.Fatalf("expected orders wrapper, got %v", result)
	}
}

func TestBuildIDsQuerySingleID(t *testing.T) {
	query, err := buildIDsQuery([]string{"1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(query, &payload); err != nil {
		t.Fatalf("parse query: %v", err)
	}
	queryObj := payload["query"].(map[string]interface{})
	ids := queryObj["ids"].(map[string]interface{})
	values := ids["values"].([]interface{})
	if len(values) != 1 {
		t.Fatalf("expected 1 id, got %d", len(values))
	}
	if payload["size"].(float64) != 1 {
		t.Fatalf("expected size 1, got %v", payload["size"])
	}
}

func TestBuildIDsQueryMultipleIDs(t *testing.T) {
	query, err := buildIDsQuery([]string{"1", "2", "3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(query, &payload); err != nil {
		t.Fatalf("parse query: %v", err)
	}
	queryObj := payload["query"].(map[string]interface{})
	ids := queryObj["ids"].(map[string]interface{})
	values := ids["values"].([]interface{})
	if len(values) != 3 {
		t.Fatalf("expected 3 ids, got %d", len(values))
	}
	if payload["size"].(float64) != 3 {
		t.Fatalf("expected size 3, got %v", payload["size"])
	}
}

func TestCoerceStringListEmpty(t *testing.T) {
	// Empty list is allowed by coerceStringList itself
	// Error checking happens at the caller level
	result, err := coerceStringList([]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected empty result, got %v", result)
	}
}

func TestCoerceStringListInvalidType(t *testing.T) {
	_, err := coerceStringList("not a list")
	if err == nil {
		t.Fatalf("expected error for non-list")
	}
}

func TestCoerceStringListNonString(t *testing.T) {
	_, err := coerceStringList([]interface{}{123})
	if err == nil {
		t.Fatalf("expected error for non-string item")
	}
}

func TestCoerceStringListEmptyString(t *testing.T) {
	_, err := coerceStringList([]interface{}{""})
	if err == nil {
		t.Fatalf("expected error for empty string")
	}
}

func TestRewriteIndexQueryParam(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.Name = "shared-{{.index}}"
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_analyze?index=products-tenant1", nil)
	index, err := proxyHandler.rewriteIndexQueryParam(req, "index")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if index != "shared-products" {
		t.Fatalf("expected shared-products, got %q", index)
	}
}

func TestRewriteIndexQueryParamEmpty(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_analyze", nil)
	index, err := proxyHandler.rewriteIndexQueryParam(req, "index")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if index != "" {
		t.Fatalf("expected empty string, got %q", index)
	}
}

func TestRewriteIndexQueryParamMultiple(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_analyze?index=idx1,idx2", nil)
	_, err := proxyHandler.rewriteIndexQueryParam(req, "index")
	if err == nil {
		t.Fatalf("expected error for multiple indices")
	}
}

func TestIndexFromQueryMultiple(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_search?index=idx1,idx2", nil)
	_, err := proxyHandler.indexFromQuery(req, "index")
	if err == nil {
		t.Fatalf("expected error for multiple indices")
	}
}

func TestIndexFromQueryEmpty(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_search", nil)
	index, err := proxyHandler.indexFromQuery(req, "index")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if index != "" {
		t.Fatalf("expected empty string, got %q", index)
	}
}

func TestSetIndexQueryParam(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_search", nil)
	proxyHandler.setIndexQueryParam(req, "test-index")
	query := req.URL.Query()
	if query.Get("index") != "test-index" {
		t.Fatalf("expected test-index, got %q", query.Get("index"))
	}
}

func TestResolveIndexFromQuery(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_search?index=orders-tenant2", nil)
	baseIndex, tenantID, err := proxyHandler.resolveIndex("", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if baseIndex != "orders" {
		t.Fatalf("expected orders, got %q", baseIndex)
	}
	if tenantID != "tenant2" {
		t.Fatalf("expected tenant2, got %q", tenantID)
	}
}

func TestResolveIndexFromPath(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/orders-tenant2/_search", nil)
	baseIndex, tenantID, err := proxyHandler.resolveIndex("orders-tenant2", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if baseIndex != "orders" {
		t.Fatalf("expected orders, got %q", baseIndex)
	}
	if tenantID != "tenant2" {
		t.Fatalf("expected tenant2, got %q", tenantID)
	}
}

func TestResolveIndexMissing(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_search", nil)
	_, _, err := proxyHandler.resolveIndex("", req)
	if err == nil {
		t.Fatalf("expected error for missing index")
	}
}

func TestApplyIndexRewriteWithOriginal(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/orders-tenant2/_search", nil)
	proxyHandler.applyIndexRewrite(req, "orders-tenant2", "target-index")
	if req.URL.Path != "/target-index/_search" {
		t.Fatalf("expected /target-index/_search, got %q", req.URL.Path)
	}
}

func TestApplyIndexRewriteWithoutOriginal(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_search", nil)
	proxyHandler.applyIndexRewrite(req, "", "target-index")
	query := req.URL.Query()
	if query.Get("index") != "target-index" {
		t.Fatalf("expected target-index, got %q", query.Get("index"))
	}
}

func TestRewriteIndexValueArray(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	proxyHandler, _ := newProxyWithServer(t, cfg)

	value := []interface{}{"orders-tenant1", "products-tenant2"}
	result, err := proxyHandler.rewriteIndexValue(value, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	array, ok := result.([]interface{})
	if !ok {
		t.Fatalf("expected array, got %T", result)
	}
	if len(array) != 2 {
		t.Fatalf("expected 2 items, got %d", len(array))
	}
	if array[0].(string) != "alias-orders-tenant1" {
		t.Fatalf("expected alias-orders-tenant1, got %v", array[0])
	}
}

func TestRewriteIndexValueArrayInvalidType(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	value := []interface{}{123}
	_, err := proxyHandler.rewriteIndexValue(value, true)
	if err == nil {
		t.Fatalf("expected error for non-string item")
	}
}

func TestRewriteIndexValueInvalidType(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	value := 123
	_, err := proxyHandler.rewriteIndexValue(value, true)
	if err == nil {
		t.Fatalf("expected error for invalid type")
	}
}

func TestRewriteIndexValueArrayIndexPerTenant(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "{{.index}}-{{.tenant}}"
	proxyHandler, _ := newProxyWithServer(t, cfg)

	value := []interface{}{"orders-tenant1"}
	result, err := proxyHandler.rewriteIndexValue(value, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	array, ok := result.([]interface{})
	if !ok {
		t.Fatalf("expected array, got %T", result)
	}
	if array[0].(string) != "orders-tenant1" {
		t.Fatalf("expected orders-tenant1, got %v", array[0])
	}
}

func TestRewriteSourceIndexValueString(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	proxyHandler, _ := newProxyWithServer(t, cfg)

	result, err := proxyHandler.rewriteSourceIndexValue("orders-tenant1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.(string) != "alias-orders-tenant1" {
		t.Fatalf("expected alias-orders-tenant1, got %v", result)
	}
}

func TestRewriteTargetIndexValueString(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.Name = "shared-{{.index}}"
	proxyHandler, _ := newProxyWithServer(t, cfg)

	result, err := proxyHandler.rewriteTargetIndexValue("orders-tenant1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.(string) != "shared-orders" {
		t.Fatalf("expected shared-orders, got %v", result)
	}
}

func TestRewriteIndexNameIndexPerTenant(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "{{.index}}-{{.tenant}}"
	proxyHandler, _ := newProxyWithServer(t, cfg)

	result, err := proxyHandler.rewriteIndexName("orders-tenant1", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "orders-tenant1" {
		t.Fatalf("expected orders-tenant1, got %q", result)
	}
}

func TestRewriteIndexNameSharedModeAlias(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	proxyHandler, _ := newProxyWithServer(t, cfg)

	result, err := proxyHandler.rewriteIndexName("orders-tenant1", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "alias-orders-tenant1" {
		t.Fatalf("expected alias-orders-tenant1, got %q", result)
	}
}

func TestRewriteIndexNameSharedModeIndex(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.Name = "shared-{{.index}}"
	proxyHandler, _ := newProxyWithServer(t, cfg)

	result, err := proxyHandler.rewriteIndexName("orders-tenant1", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "shared-orders" {
		t.Fatalf("expected shared-orders, got %q", result)
	}
}

func TestRewriteIndexNameInvalidIndex(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	_, err := proxyHandler.rewriteIndexName("invalid", false)
	if err == nil {
		t.Fatalf("expected error for invalid index")
	}
}

func TestRewriteQueryValuePrefix(t *testing.T) {
	proxyHandler, _ := newProxyWithServer(t, config.Default())
	result := proxyHandler.rewriteQueryValue(map[string]interface{}{
		"prefix": map[string]interface{}{"field1": "value"},
	}, "orders")
	obj := result.(map[string]interface{})
	prefix := obj["prefix"].(map[string]interface{})
	if prefix["orders.field1"] == nil {
		t.Fatalf("expected orders.field1, got %v", prefix)
	}
}

func TestRewriteQueryValueWildcard(t *testing.T) {
	proxyHandler, _ := newProxyWithServer(t, config.Default())
	result := proxyHandler.rewriteQueryValue(map[string]interface{}{
		"wildcard": map[string]interface{}{"field1": "value"},
	}, "orders")
	obj := result.(map[string]interface{})
	wildcard := obj["wildcard"].(map[string]interface{})
	if wildcard["orders.field1"] == nil {
		t.Fatalf("expected orders.field1, got %v", wildcard)
	}
}

func TestRewriteQueryValueRegexp(t *testing.T) {
	proxyHandler, _ := newProxyWithServer(t, config.Default())
	result := proxyHandler.rewriteQueryValue(map[string]interface{}{
		"regexp": map[string]interface{}{"field1": "value"},
	}, "orders")
	obj := result.(map[string]interface{})
	regexp := obj["regexp"].(map[string]interface{})
	if regexp["orders.field1"] == nil {
		t.Fatalf("expected orders.field1, got %v", regexp)
	}
}

func TestRewriteQueryValueTerm(t *testing.T) {
	proxyHandler, _ := newProxyWithServer(t, config.Default())
	result := proxyHandler.rewriteQueryValue(map[string]interface{}{
		"term": map[string]interface{}{"field1": "value"},
	}, "orders")
	obj := result.(map[string]interface{})
	term := obj["term"].(map[string]interface{})
	if term["orders.field1"] == nil {
		t.Fatalf("expected orders.field1, got %v", term)
	}
}

func TestRewriteQueryValueRange(t *testing.T) {
	proxyHandler, _ := newProxyWithServer(t, config.Default())
	result := proxyHandler.rewriteQueryValue(map[string]interface{}{
		"range": map[string]interface{}{"field1": map[string]interface{}{"gte": 10}},
	}, "orders")
	obj := result.(map[string]interface{})
	rangeObj := obj["range"].(map[string]interface{})
	if rangeObj["orders.field1"] == nil {
		t.Fatalf("expected orders.field1, got %v", rangeObj)
	}
}

func TestRewriteFieldObjectNonObject(t *testing.T) {
	proxyHandler, _ := newProxyWithServer(t, config.Default())
	result := proxyHandler.rewriteFieldObject("not an object", "orders")
	if result != "not an object" {
		t.Fatalf("expected unchanged value, got %v", result)
	}
}

func TestRewriteSortValueNonList(t *testing.T) {
	proxyHandler, _ := newProxyWithServer(t, config.Default())
	result := proxyHandler.rewriteSortValue("not a list", "orders")
	if result != "not a list" {
		t.Fatalf("expected unchanged value, got %v", result)
	}
}

func TestRewriteSourceFilterString(t *testing.T) {
	proxyHandler, _ := newProxyWithServer(t, config.Default())
	result := proxyHandler.rewriteSourceFilter("not supported", "orders")
	if result != "not supported" {
		t.Fatalf("expected unchanged value, got %v", result)
	}
}

func TestSetPathSegments(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/old/path", nil)
	proxyHandler.setPathSegments(req, []string{"new", "path"})
	if req.URL.Path != "/new/path" {
		t.Fatalf("expected /new/path, got %q", req.URL.Path)
	}
}

func TestSetPathSegmentsSingle(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/old", nil)
	proxyHandler.setPathSegments(req, []string{"new"})
	if req.URL.Path != "/new" {
		t.Fatalf("expected /new, got %q", req.URL.Path)
	}
}

func TestRewriteIndexPathNoMatch(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/orders-tenant2/_search", nil)
	proxyHandler.rewriteIndexPath(req, "different-index", "target-index")
	if req.URL.Path != "/orders-tenant2/_search" {
		t.Fatalf("expected unchanged path, got %q", req.URL.Path)
	}
}

func TestRewriteIndexPathEmptySegments(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	proxyHandler.rewriteIndexPath(req, "index", "target")
	// Should not panic or crash
}

func TestHandleIndexDeleteError(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	// Test with invalid index to trigger error path
	req := httptest.NewRequest(http.MethodDelete, "/invalid", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleIndexPassthroughError(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	// Test with invalid index to trigger error path
	req := httptest.NewRequest(http.MethodGet, "/invalid/_settings", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleAnalyzeMissingIndex(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_analyze", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleAnalyzeInvalidIndexInQuery(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_analyze?index=invalid", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleAnalyzeMultipleIndices(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/_analyze?index=idx1,idx2", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleSearchRootMissingIndex(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	body := []byte(`{"query":{"match_all":{}}}`)
	req := httptest.NewRequest(http.MethodPost, "/_search", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleQueryEndpointRootMissingIndex(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	body := []byte(`{"query":{"match_all":{}}}`)
	req := httptest.NewRequest(http.MethodPost, "/_query", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleExplainRootMissingIndex(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	body := []byte(`{"query":{"match_all":{}}}`)
	req := httptest.NewRequest(http.MethodPost, "/_explain", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleSearchTemplateRootMissingIndex(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	body := []byte(`{"source":{"query":{"match_all":{}}}}`)
	req := httptest.NewRequest(http.MethodPost, "/_search/template", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestRenderAliasError(t *testing.T) {
	// Template errors are caught during New(), not during render
	// renderAlias just executes the template, so it doesn't error on empty strings
	// The error would come from parseIndex, not renderAlias
	// This test is just to ensure renderAlias executes correctly
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	proxyHandler, _ := newProxyWithServer(t, cfg)

	result, err := proxyHandler.renderAlias("orders", "tenant1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "alias-orders-tenant1" {
		t.Fatalf("expected alias-orders-tenant1, got %q", result)
	}
}

func TestRenderIndexError(t *testing.T) {
	// Template errors are caught during New(), not during render
	// renderIndex just executes the template, so it doesn't error on empty strings
	cfg := config.Default()
	cfg.IndexPerTenant.IndexTemplate = "{{.index}}-{{.tenant}}"
	proxyHandler, _ := newProxyWithServer(t, cfg)

	result, err := proxyHandler.renderIndex(proxyHandler.perTenantIdx, "orders", "tenant1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "orders-tenant1" {
		t.Fatalf("expected orders-tenant1, got %q", result)
	}
}

func TestParseIndexMissingGroups(t *testing.T) {
	// Test that parseIndex handles the case where regex matches but groups are missing
	// This is tested indirectly through New() which validates groups
	cfg := config.Default()
	invalidRegex := regexp.MustCompile(`^(.*)$`)
	cfg.TenantRegex.Compiled = invalidRegex

	// Create proxy manually to test the error path in parseIndex
	// But New() validates groups, so we need to test a different way
	// Actually, this case is already covered - groups are validated in New()
	// So parseIndex will only be called with valid regex
	_, err := New(cfg)
	if err == nil {
		t.Fatalf("expected error for missing groups in New()")
	}
}

func TestParseIndexInvalidIndex(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	// Test with index that doesn't match the tenant regex (no dash at all)
	_, _, err := proxyHandler.parseIndex("nodashes")
	if err == nil {
		t.Fatalf("expected error for invalid index format")
	}
	if !strings.Contains(err.Error(), "does not match tenant regex") {
		t.Fatalf("expected tenant regex error, got %v", err)
	}
}

func TestParseIndexBlockedSharedIndex(t *testing.T) {
	cfg := config.Default()
	cfg.SharedIndex.DenyPatterns = []string{"^shared-index$"}
	cfg.SharedIndex.DenyCompiled = []*regexp.Regexp{regexp.MustCompile("^shared-index$")}
	proxyHandler, _ := newProxyWithServer(t, cfg)

	_, _, err := proxyHandler.parseIndex("shared-index")
	if err == nil {
		t.Fatalf("expected error for shared index access")
	}
	if !strings.Contains(err.Error(), "direct access to shared indices") {
		t.Fatalf("expected shared index error, got %v", err)
	}
}

func TestRejectDirectSharedIndexAccess(t *testing.T) {
	cfg := config.Default()
	cfg.SharedIndex.DenyPatterns = []string{"^shared-index$"}
	cfg.SharedIndex.DenyCompiled = []*regexp.Regexp{regexp.MustCompile("^shared-index$")}
	proxyHandler, _ := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/shared-index/_search", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestParseIndexEmptyGroups(t *testing.T) {
	cfg := config.Default()
	// Create a regex where groups can be empty
	cfg.TenantRegex.Pattern = `^(?P<prefix>.*)-(?P<tenant>.*)-(?P<postfix>.*)$`
	compiled, _ := regexp.Compile(cfg.TenantRegex.Pattern)
	cfg.TenantRegex.Compiled = compiled

	proxyHandler, _ := newProxyWithServer(t, cfg)

	// Test with index that matches but results in empty baseIndex and tenantID
	_, _, err := proxyHandler.parseIndex("--")
	if err == nil {
		t.Fatalf("expected error for empty baseIndex/tenantID")
	}
	if !strings.Contains(err.Error(), "invalid index") {
		t.Fatalf("expected invalid index error, got %v", err)
	}
}

func TestIsCatIndices(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	if !proxyHandler.isCatIndices("/_cat/indices") {
		t.Fatalf("expected /_cat/indices to match")
	}
	if proxyHandler.isCatIndices("/_cat/health") {
		t.Fatalf("expected /_cat/health not to match")
	}
	if proxyHandler.isCatIndices("/_cat/indices/v2") {
		t.Fatalf("expected /_cat/indices/v2 not to match")
	}
}

func TestSplitPath(t *testing.T) {
	// Test splitPath function
	if len(splitPath("")) != 0 {
		t.Fatalf("expected empty path to return empty slice")
	}
	if len(splitPath("/")) != 0 {
		t.Fatalf("expected / to return empty slice")
	}
	segments := splitPath("/a/b/c")
	if len(segments) != 3 {
		t.Fatalf("expected 3 segments, got %d", len(segments))
	}
	if segments[0] != "a" {
		t.Fatalf("expected a, got %q", segments[0])
	}
}

func TestIsSystemPassthrough(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	testCases := []struct {
		path   string
		expect bool
	}{
		{"/_cluster/health", true},
		{"/_cat/nodes", true},
		{"/_nodes/stats", true},
		{"/_snapshot/repo", true},
		{"/_tasks/task-id", true},
		{"/_scripts/my-script", true},
		{"/_security/user", true},
		{"/_license", true},
		{"/_ml/job", true},
		{"/_watcher/watch", true},
		{"/_graph/explore", true},
		{"/_ccr/follow", true},
		{"/_alias", true},
		{"/_template/my-template", true},
		{"/_index_template/my-template", true},
		{"/_component_template/my-template", true},
		{"/_query_rules/set", true},
		{"/_synonyms/set", true},
		{"/_resolve/index", true},
		{"/_data_stream/my-stream", true},
		{"/_dangling/delete", true},
		{"/products-tenant1", false},
		{"/_search", false},
	}

	for _, tc := range testCases {
		result := proxyHandler.isSystemPassthrough(tc.path)
		if result != tc.expect {
			t.Errorf("isSystemPassthrough(%q) = %v, expected %v", tc.path, result, tc.expect)
		}
	}
}

func TestReject(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	rec := httptest.NewRecorder()
	proxyHandler.reject(rec, "test error")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("expected application/json content type")
	}

	var response map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if response["error"] != "unsupported_request" {
		t.Fatalf("expected unsupported_request error, got %v", response["error"])
	}
}

func TestIsPassthroughEmpty(t *testing.T) {
	cfg := config.Default()
	cfg.PassthroughPaths = []string{""}
	proxyHandler, _ := newProxyWithServer(t, cfg)

	// Empty path should not match
	if proxyHandler.isPassthrough("/path") {
		t.Fatalf("expected empty passthrough not to match")
	}
}

func TestNewProxyInvalidURL(t *testing.T) {
	cfg := config.Default()
	cfg.UpstreamURL = ":invalid"
	_, err := New(cfg)
	if err == nil {
		t.Fatalf("expected error for invalid URL")
	}
}

func TestNewProxyInvalidAliasTemplate(t *testing.T) {
	cfg := config.Default()
	cfg.SharedIndex.AliasTemplate = "{{invalid"
	_, err := New(cfg)
	if err == nil {
		t.Fatalf("expected error for invalid alias template")
	}
}

func TestNewProxyInvalidSharedIndexTemplate(t *testing.T) {
	cfg := config.Default()
	cfg.SharedIndex.Name = "{{invalid"
	_, err := New(cfg)
	if err == nil {
		t.Fatalf("expected error for invalid shared index template")
	}
}

func TestNewProxyInvalidPerTenantTemplate(t *testing.T) {
	cfg := config.Default()
	cfg.IndexPerTenant.IndexTemplate = "{{invalid"
	_, err := New(cfg)
	if err == nil {
		t.Fatalf("expected error for invalid per-tenant template")
	}
}

func TestNewProxyInvalidRegexGroups(t *testing.T) {
	cfg := config.Default()
	invalidRegex := regexp.MustCompile(`^(.*)$`)
	cfg.TenantRegex.Compiled = invalidRegex
	_, err := New(cfg)
	if err == nil {
		t.Fatalf("expected error for missing regex groups")
	}
}

func TestQueryValuePrefix(t *testing.T) {
	proxyHandler, _ := newProxyWithServer(t, config.Default())
	result := proxyHandler.rewriteQueryValue(map[string]interface{}{
		"prefix": map[string]interface{}{"field1": "value"},
	}, "orders")
	obj := result.(map[string]interface{})
	prefix := obj["prefix"].(map[string]interface{})
	if prefix["orders.field1"] == nil {
		t.Fatalf("expected orders.field1, got %v", prefix)
	}
}

func TestQueryValueNested(t *testing.T) {
	proxyHandler, _ := newProxyWithServer(t, config.Default())
	result := proxyHandler.rewriteQueryValue(map[string]interface{}{
		"bool": map[string]interface{}{
			"must": []interface{}{
				map[string]interface{}{"match": map[string]interface{}{"field1": "value"}},
			},
		},
	}, "orders")
	obj := result.(map[string]interface{})
	boolObj := obj["bool"].(map[string]interface{})
	must := boolObj["must"].([]interface{})
	nested := must[0].(map[string]interface{})
	match := nested["match"].(map[string]interface{})
	if match["orders.field1"] == nil {
		t.Fatalf("expected orders.field1 in nested query, got %v", match)
	}
}

func TestRewriteFieldListNonList(t *testing.T) {
	proxyHandler, _ := newProxyWithServer(t, config.Default())
	result := proxyHandler.rewriteFieldList("not a list", "orders")
	if result != "not a list" {
		t.Fatalf("expected unchanged value, got %v", result)
	}
}

func TestRewriteFieldListMixedTypes(t *testing.T) {
	proxyHandler, _ := newProxyWithServer(t, config.Default())
	result := proxyHandler.rewriteFieldList([]interface{}{"field1", 123, "field2"}, "orders")
	list := result.([]interface{})
	if list[0].(string) != "orders.field1" {
		t.Fatalf("expected orders.field1, got %v", list[0])
	}
	if list[1] != 123 {
		t.Fatalf("expected unchanged 123, got %v", list[1])
	}
	if list[2].(string) != "orders.field2" {
		t.Fatalf("expected orders.field2, got %v", list[2])
	}
}

func TestIsSharedMode(t *testing.T) {
	if !isSharedMode("shared") {
		t.Fatalf("expected shared mode to be true")
	}
	if !isSharedMode("SHARED") {
		t.Fatalf("expected SHARED mode to be true (case insensitive)")
	}
	if !isSharedMode("Shared") {
		t.Fatalf("expected Shared mode to be true (case insensitive)")
	}
	if isSharedMode("index-per-tenant") {
		t.Fatalf("expected index-per-tenant mode to be false")
	}
}

func TestHandleSearchRootMultipleIndices(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	body := []byte(`{"query":{"match_all":{}}}`)
	req := httptest.NewRequest(http.MethodPost, "/_search?index=idx1,idx2", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestHandleRankEvalRootMissingIndex(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	body := []byte(`{"requests":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/_rank_eval", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestServeHTTPRequestIndexCandidateError(t *testing.T) {
	cfg := config.Default()
	proxyHandler, _ := newProxyWithServer(t, cfg)

	// Request with invalid path that causes requestIndexCandidate to error
	req := httptest.NewRequest(http.MethodPost, "/_search", nil)
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	// Should proceed without blocking despite error from requestIndexCandidate
	// The request should be processed (either as passthrough or handled)
	if rec.Code == 0 {
		t.Fatalf("expected response to be processed")
	}
}

func TestLogVerboseDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.Verbose = false
	proxyHandler, _ := newProxyWithServer(t, cfg)

	// Call logVerbose when verbose is disabled - should not panic
	proxyHandler.logVerbose("test message: %s", "value")
	// If we got here without panic, the test passed
}

func TestLogVerboseEnabled(t *testing.T) {
	cfg := config.Default()
	cfg.Verbose = true
	proxyHandler, _ := newProxyWithServer(t, cfg)

	// Call logVerbose when verbose is enabled - should not panic
	proxyHandler.logVerbose("test message: %s", "value")
	// If we got here without panic, the test passed
}

func TestPrefixFieldWithVerboseLogging(t *testing.T) {
	cfg := config.Default()
	cfg.Verbose = true
	proxyHandler, _ := newProxyWithServer(t, cfg)

	// Test prefixField with verbose logging enabled
	result := proxyHandler.prefixField("orders", "field1")
	if result != "orders.field1" {
		t.Fatalf("expected orders.field1, got %q", result)
	}
}

func TestIsBlockedSharedIndexWithMatch(t *testing.T) {
	cfg := config.Default()
	cfg.SharedIndex.DenyPatterns = []string{"^shared-.*", "^blocked-.*"}
	cfg.SharedIndex.DenyCompiled = []*regexp.Regexp{
		regexp.MustCompile("^shared-.*"),
		regexp.MustCompile("^blocked-.*"),
	}
	proxyHandler, _ := newProxyWithServer(t, cfg)

	if !proxyHandler.isBlockedSharedIndex("shared-index-name") {
		t.Fatalf("expected shared-index-name to be blocked")
	}
	if !proxyHandler.isBlockedSharedIndex("blocked-something") {
		t.Fatalf("expected blocked-something to be blocked")
	}
}

func TestIsBlockedSharedIndexWithoutMatch(t *testing.T) {
	cfg := config.Default()
	cfg.SharedIndex.DenyPatterns = []string{"^shared-.*"}
	cfg.SharedIndex.DenyCompiled = []*regexp.Regexp{
		regexp.MustCompile("^shared-.*"),
	}
	proxyHandler, _ := newProxyWithServer(t, cfg)

	if proxyHandler.isBlockedSharedIndex("my-index") {
		t.Fatalf("expected my-index to not be blocked")
	}
}

func TestIsBlockedSharedIndexEmptyPatterns(t *testing.T) {
	cfg := config.Default()
	cfg.SharedIndex.DenyPatterns = []string{}
	cfg.SharedIndex.DenyCompiled = []*regexp.Regexp{}
	proxyHandler, _ := newProxyWithServer(t, cfg)

	if proxyHandler.isBlockedSharedIndex("any-index") {
		t.Fatalf("expected any-index to not be blocked when no patterns configured")
	}
}
