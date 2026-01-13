package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	proxyHandler, err := New(cfg)
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxyHandler.transport = transport
	return proxyHandler, capture
}

func TestTenantExtraction(t *testing.T) {
	cfg := config.Default()
	router, err := NewRouter(cfg)
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/tenant/acme/index1/_search", nil)
	route, err := router.Route(req)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if route.Tenant != "acme" {
		t.Fatalf("expected tenant acme, got %q", route.Tenant)
	}
	if route.Path != "/index1/_search" {
		t.Fatalf("expected path /index1/_search, got %q", route.Path)
	}
}

func TestSharedIndexSearchRewrite(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.AliasTemplate = "{index}-{tenant}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	body := []byte(`{"query":{"match":{"field1":"value"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/tenant/acme/index1/_search", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, capturedBody, _, _ := capture.snapshot()
	if path != "/index1-acme/_search" {
		t.Fatalf("expected path /index1-acme/_search, got %q", path)
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
	req := httptest.NewRequest(http.MethodPut, "/tenant/acme/index1/_doc/1", bytes.NewReader(reqBody))
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
	if payload["tenant_id"] != "acme" {
		t.Fatalf("expected tenant_id acme, got %v", payload["tenant_id"])
	}
}

func TestIndexPerTenantSearchRewrite(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "tenant-{tenant}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	reqBody := []byte(`{"query":{"match":{"field1":"value"}},"sort":["field2"]}`)
	req := httptest.NewRequest(http.MethodPost, "/tenant/acme/index1/_search", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, capturedBody, _, _ := capture.snapshot()
	if path != "/tenant-acme/_search" {
		t.Fatalf("expected path /tenant-acme/_search, got %q", path)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	query := payload["query"].(map[string]interface{})
	match := query["match"].(map[string]interface{})
	if _, ok := match["index1.field1"]; !ok {
		t.Fatalf("expected field index1.field1 in match, got %v", match)
	}
	sort := payload["sort"].([]interface{})
	if sort[0].(string) != "index1.field2" {
		t.Fatalf("expected sort index1.field2, got %v", sort)
	}
}

func TestIndexPerTenantMappingRewrite(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "tenant-{tenant}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	reqBody := []byte(`{"properties":{"field1":{"type":"text"},"field2":{"properties":{"sub":{"type":"keyword"}}}}}`)
	req := httptest.NewRequest(http.MethodPut, "/tenant/acme/index1/_mapping", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	_, capturedBody, _, _ := capture.snapshot()
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	props := payload["properties"].(map[string]interface{})
	if _, ok := props["index1.field1"]; !ok {
		t.Fatalf("expected index1.field1 property, got %v", props)
	}
	field2 := props["index1.field2"].(map[string]interface{})
	field2Props := field2["properties"].(map[string]interface{})
	if _, ok := field2Props["sub"]; !ok {
		t.Fatalf("expected nested sub property, got %v", field2Props)
	}
}

func TestIndexPerTenantIndexingRewrite(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "tenant-{tenant}"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	reqBody := []byte(`{"field1":"value"}`)
	req := httptest.NewRequest(http.MethodPost, "/tenant/acme/index1/_doc", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	path, capturedBody, _, _ := capture.snapshot()
	if path != "/tenant-acme/_doc" {
		t.Fatalf("expected path /tenant-acme/_doc, got %q", path)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if _, ok := payload["index1"]; !ok {
		t.Fatalf("expected index1 key, got %v", payload)
	}
}

func TestUnsupportedRequestReturnsError(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "shared"
	proxyHandler, capture := newProxyWithServer(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/tenant/acme/index1/_settings", nil)
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
