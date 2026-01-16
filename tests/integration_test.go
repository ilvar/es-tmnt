package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"text/template"
	"time"
)

type responseBody map[string]interface{}

func TestSharedMode(t *testing.T) {
	if os.Getenv("TEST_MODE") != "shared" {
		t.Skip("shared mode test skipped")
	}
	proxyURL := mustEnv(t, "PROXY_URL")
	esURL := mustEnv(t, "ES_URL")
	aliasTemplate := mustEnv(t, "ALIAS_TEMPLATE")
	tenantField := mustEnv(t, "TENANT_FIELD")
	indexName := "products"
	tenant := "tenant1"
	aliasName := renderAlias(t, aliasTemplate, indexName, tenant)

	cleanupIndex(t, esURL, indexName)
	cleanupAlias(t, esURL, aliasName)

	request(t, http.MethodPut, esURL+"/"+indexName, nil)
	aliasPayload := map[string]interface{}{
		"actions": []map[string]interface{}{
			{"add": map[string]interface{}{
				"index":  indexName,
				"alias":  aliasName,
				"filter": map[string]interface{}{"term": map[string]interface{}{tenantField: tenant}},
			}},
		},
	}
	request(t, http.MethodPost, esURL+"/_aliases", aliasPayload)

	indexBody := map[string]interface{}{"name": "shoe"}
	logKeyRequest(t, http.MethodPut, proxyURL+"/"+indexName+"-"+tenant+"/_doc/1", indexBody)
	request(t, http.MethodPut, proxyURL+"/"+indexName+"-"+tenant+"/_doc/1", indexBody)

	searchBody := map[string]interface{}{"query": map[string]interface{}{"match": map[string]interface{}{"name": "shoe"}}}
	logKeyRequest(t, http.MethodPost, proxyURL+"/"+indexName+"-"+tenant+"/_search", searchBody)
	searchResp := request(t, http.MethodPost, proxyURL+"/"+indexName+"-"+tenant+"/_search", searchBody)
	if hitsTotal(searchResp) < 1 {
		t.Fatalf("expected search results, got %v", searchResp)
	}

	logKeyRequest(t, http.MethodGet, esURL+"/"+indexName+"/_doc/1", nil)
	docResp := request(t, http.MethodGet, esURL+"/"+indexName+"/_doc/1", nil)
	source := extractSource(t, docResp)
	if source[tenantField] != tenant {
		t.Fatalf("expected tenant field %s, got %v", tenantField, source)
	}
}

func TestSharedModeUpdate(t *testing.T) {
	if os.Getenv("TEST_MODE") != "shared" {
		t.Skip("shared mode test skipped")
	}
	proxyURL := mustEnv(t, "PROXY_URL")
	esURL := mustEnv(t, "ES_URL")
	aliasTemplate := mustEnv(t, "ALIAS_TEMPLATE")
	tenantField := mustEnv(t, "TENANT_FIELD")
	indexName := "catalog"
	tenant := "tenant3"
	aliasName := renderAlias(t, aliasTemplate, indexName, tenant)

	cleanupIndex(t, esURL, indexName)
	cleanupAlias(t, esURL, aliasName)

	request(t, http.MethodPut, esURL+"/"+indexName, nil)
	aliasPayload := map[string]interface{}{
		"actions": []map[string]interface{}{
			{"add": map[string]interface{}{
				"index":  indexName,
				"alias":  aliasName,
				"filter": map[string]interface{}{"term": map[string]interface{}{tenantField: tenant}},
			}},
		},
	}
	request(t, http.MethodPost, esURL+"/_aliases", aliasPayload)

	indexBody := map[string]interface{}{"name": "bag"}
	logKeyRequest(t, http.MethodPut, proxyURL+"/"+indexName+"-"+tenant+"/_doc/42", indexBody)
	request(t, http.MethodPut, proxyURL+"/"+indexName+"-"+tenant+"/_doc/42", indexBody)

	updateBody := map[string]interface{}{"doc": map[string]interface{}{"name": "bag-updated"}}
	logKeyRequest(t, http.MethodPost, proxyURL+"/"+indexName+"-"+tenant+"/_update/42", updateBody)
	request(t, http.MethodPost, proxyURL+"/"+indexName+"-"+tenant+"/_update/42", updateBody)

	logKeyRequest(t, http.MethodGet, esURL+"/"+indexName+"/_doc/42", nil)
	docResp := request(t, http.MethodGet, esURL+"/"+indexName+"/_doc/42", nil)
	source := extractSource(t, docResp)
	if source["name"] != "bag-updated" || source[tenantField] != tenant {
		t.Fatalf("expected updated doc with tenant, got %v", source)
	}
}

func TestPerTenantMode(t *testing.T) {
	if os.Getenv("TEST_MODE") != "per-tenant" {
		t.Skip("per-tenant mode test skipped")
	}
	proxyURL := mustEnv(t, "PROXY_URL")
	esURL := mustEnv(t, "ES_URL")
	realIndex := mustEnv(t, "REAL_INDEX")
	indexName := "orders"
	tenant := "tenant2"

	cleanupIndex(t, esURL, realIndex)

	indexBody := map[string]interface{}{"field1": "value"}
	logKeyRequest(t, http.MethodPut, proxyURL+"/"+indexName+"-"+tenant+"/_doc/1", indexBody)
	request(t, http.MethodPut, proxyURL+"/"+indexName+"-"+tenant+"/_doc/1", indexBody)

	logKeyRequest(t, http.MethodGet, esURL+"/"+realIndex+"/_doc/1", nil)
	docResp := request(t, http.MethodGet, esURL+"/"+realIndex+"/_doc/1", nil)
	source := extractSource(t, docResp)
	if _, ok := source[indexName]; !ok {
		t.Fatalf("expected nested index field in source: %v", source)
	}

	searchBody := map[string]interface{}{"query": map[string]interface{}{"match": map[string]interface{}{"field1": "value"}}}
	logKeyRequest(t, http.MethodPost, proxyURL+"/"+indexName+"-"+tenant+"/_search", searchBody)
	searchResp := request(t, http.MethodPost, proxyURL+"/"+indexName+"-"+tenant+"/_search", searchBody)
	if hitsTotal(searchResp) < 1 {
		t.Fatalf("expected search results, got %v", searchResp)
	}
}

func TestPerTenantModeUpdate(t *testing.T) {
	if os.Getenv("TEST_MODE") != "per-tenant" {
		t.Skip("per-tenant mode test skipped")
	}
	proxyURL := mustEnv(t, "PROXY_URL")
	esURL := mustEnv(t, "ES_URL")
	realIndex := mustEnv(t, "REAL_INDEX")
	indexName := "invoices"
	tenant := "tenant4"

	cleanupIndex(t, esURL, realIndex)

	indexBody := map[string]interface{}{"field1": "initial"}
	logKeyRequest(t, http.MethodPut, proxyURL+"/"+indexName+"-"+tenant+"/_doc/7", indexBody)
	request(t, http.MethodPut, proxyURL+"/"+indexName+"-"+tenant+"/_doc/7", indexBody)

	updateBody := map[string]interface{}{"doc": map[string]interface{}{"field1": "updated"}}
	logKeyRequest(t, http.MethodPost, proxyURL+"/"+indexName+"-"+tenant+"/_update/7", updateBody)
	request(t, http.MethodPost, proxyURL+"/"+indexName+"-"+tenant+"/_update/7", updateBody)

	logKeyRequest(t, http.MethodGet, esURL+"/"+realIndex+"/_doc/7", nil)
	docResp := request(t, http.MethodGet, esURL+"/"+realIndex+"/_doc/7", nil)
	source := extractSource(t, docResp)
	wrapped, ok := source[indexName].(map[string]interface{})
	if !ok || wrapped["field1"] != "updated" {
		t.Fatalf("expected nested updated doc, got %v", source)
	}
}

func request(t *testing.T, method, url string, body interface{}) responseBody {
	client := &http.Client{Timeout: 10 * time.Second}
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		var errBody responseBody
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("request %s %s failed: %d %v", method, url, resp.StatusCode, errBody)
	}
	var decoded responseBody
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return responseBody{}
	}
	return decoded
}

func logKeyRequest(t *testing.T, method, url string, body interface{}) {
	var payload string
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body for log: %v", err)
		}
		payload = string(encoded)
	}
	fmt.Printf("key request: method=%s endpoint=%s payload=%s\n", method, url, payload)
}

func hitsTotal(body responseBody) int {
	hits, ok := body["hits"].(map[string]interface{})
	if !ok {
		return 0
	}
	total, ok := hits["total"].(map[string]interface{})
	if !ok {
		return 0
	}
	value, ok := total["value"].(float64)
	if !ok {
		return 0
	}
	return int(value)
}

func extractSource(t *testing.T, body responseBody) map[string]interface{} {
	source, ok := body["_source"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing _source in response: %v", body)
	}
	return source
}

func mustEnv(t *testing.T, key string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		t.Fatalf("missing env %s", key)
	}
	return value
}

func cleanupIndex(t *testing.T, esURL, index string) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/%s", esURL, index), nil)
	if err != nil {
		t.Fatalf("delete request: %v", err)
	}
	_, _ = client.Do(req)
}

func cleanupAlias(t *testing.T, esURL, alias string) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/_alias/%s", esURL, alias), nil)
	if err != nil {
		t.Fatalf("delete alias request: %v", err)
	}
	_, _ = client.Do(req)
}

func renderAlias(t *testing.T, tmpl string, index, tenant string) string {
	parsed, err := template.New("alias").Parse(tmpl)
	if err != nil {
		t.Fatalf("parse alias template: %v", err)
	}
	var builder strings.Builder
	if err := parsed.Execute(&builder, map[string]string{"index": index, "tenant": tenant}); err != nil {
		t.Fatalf("render alias template: %v", err)
	}
	return builder.String()
}
