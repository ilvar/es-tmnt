package proxy

import (
	"encoding/json"
	"strings"
	"testing"

	"es-tmnt/internal/config"
)

func TestRewriteDocumentBodyInvalidJSON(t *testing.T) {
	proxyHandler, _ := newProxyWithServer(t, config.Default())

	_, err := proxyHandler.rewriteDocumentBody([]byte("{"), "orders", "tenant1")
	if err == nil {
		t.Fatalf("expected error for invalid JSON body")
	}
}

func TestRewriteUpdateBodyErrors(t *testing.T) {
	proxyHandler, _ := newProxyWithServer(t, config.Default())

	_, err := proxyHandler.rewriteUpdateBody([]byte(`{"script":"noop"}`), "orders", "tenant1")
	if err == nil || !strings.Contains(err.Error(), "update body requires doc payload") {
		t.Fatalf("expected missing doc error, got %v", err)
	}

	_, err = proxyHandler.rewriteUpdateBody([]byte(`{"doc":"bad"}`), "orders", "tenant1")
	if err == nil || !strings.Contains(err.Error(), "update doc must be an object") {
		t.Fatalf("expected doc object error, got %v", err)
	}
}

func TestRewriteBulkBodyErrors(t *testing.T) {
	proxyHandler, _ := newProxyWithServer(t, config.Default())

	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "invalid action json",
			body:    "{not-json}\n",
			wantErr: "invalid bulk action line",
		},
		{
			name:    "multiple actions",
			body:    `{"index":{},"delete":{}}` + "\n",
			wantErr: "bulk action must contain a single operation",
		},
		{
			name:    "missing source",
			body:    `{"index":{"_id":"1"}}`,
			wantErr: "bulk payload missing source",
		},
		{
			name:    "empty source line",
			body:    `{"index":{"_id":"1"}}` + "\n\n",
			wantErr: "bulk source line empty",
		},
		{
			name:    "update doc error",
			body:    `{"update":{"_id":"1"}}` + "\n" + `{"doc":"bad"}` + "\n",
			wantErr: "update doc must be an object",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := proxyHandler.rewriteBulkBody([]byte(tc.body), "orders-tenant1")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %q error, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestBulkIndexNameErrors(t *testing.T) {
	proxyHandler, _ := newProxyWithServer(t, config.Default())

	_, err := proxyHandler.bulkIndexName(map[string]interface{}{"_index": 42}, "")
	if err == nil || !strings.Contains(err.Error(), "bulk _index must be a string") {
		t.Fatalf("expected bulk _index error, got %v", err)
	}

	_, err = proxyHandler.bulkIndexName(map[string]interface{}{}, "")
	if err == nil || !strings.Contains(err.Error(), "bulk request missing index") {
		t.Fatalf("expected missing index error, got %v", err)
	}
}

func TestRewriteQueryBodyInvalidJSON(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	proxyHandler, _ := newProxyWithServer(t, cfg)

	_, err := proxyHandler.rewriteQueryBody([]byte("{"), "orders")
	if err == nil || !strings.Contains(err.Error(), "invalid JSON body") {
		t.Fatalf("expected invalid JSON error, got %v", err)
	}
}

func TestRewriteQueryBodyComplex(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	proxyHandler, _ := newProxyWithServer(t, cfg)

	body := []byte(`{
		"query": {
			"bool": {
				"must": [
					{"match": {"field1": "value"}},
					{"range": {"created_at": {"gte": "now-1d"}}}
				],
				"filter": {"term": {"status": "open"}}
			}
		},
		"fields": ["field1", "field2"],
		"_source": {
			"includes": ["field1", "nested.field2"],
			"excludes": ["field3"]
		},
		"sort": ["field1", {"field2": {"order": "desc"}}]
	}`)

	rewritten, err := proxyHandler.rewriteQueryBody(body, "orders")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatalf("parse rewritten body: %v", err)
	}
	query := payload["query"].(map[string]interface{})
	boolQuery := query["bool"].(map[string]interface{})
	must := boolQuery["must"].([]interface{})
	firstMatch := must[0].(map[string]interface{})["match"].(map[string]interface{})
	if firstMatch["orders.field1"] == nil {
		t.Fatalf("expected orders.field1 in match query, got %v", firstMatch)
	}
	secondRange := must[1].(map[string]interface{})["range"].(map[string]interface{})
	if secondRange["orders.created_at"] == nil {
		t.Fatalf("expected orders.created_at in range query, got %v", secondRange)
	}
	filter := boolQuery["filter"].(map[string]interface{})["term"].(map[string]interface{})
	if filter["orders.status"] == nil {
		t.Fatalf("expected orders.status in filter query, got %v", filter)
	}
	fields := payload["fields"].([]interface{})
	if fields[0].(string) != "orders.field1" || fields[1].(string) != "orders.field2" {
		t.Fatalf("expected prefixed fields, got %v", fields)
	}
	source := payload["_source"].(map[string]interface{})
	includes := source["includes"].([]interface{})
	excludes := source["excludes"].([]interface{})
	if includes[0].(string) != "orders.field1" || includes[1].(string) != "orders.nested.field2" {
		t.Fatalf("expected prefixed includes, got %v", includes)
	}
	if excludes[0].(string) != "orders.field3" {
		t.Fatalf("expected prefixed excludes, got %v", excludes)
	}
	sort := payload["sort"].([]interface{})
	if sort[0].(string) != "orders.field1" {
		t.Fatalf("expected prefixed sort field, got %v", sort[0])
	}
	sortObj := sort[1].(map[string]interface{})
	if sortObj["orders.field2"] == nil {
		t.Fatalf("expected prefixed sort object, got %v", sortObj)
	}
}

func TestRewriteMappingBodyErrors(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = "index-per-tenant"
	proxyHandler, _ := newProxyWithServer(t, cfg)

	_, err := proxyHandler.rewriteMappingBody([]byte(`{"mappings":"bad"}`), "orders")
	if err == nil || !strings.Contains(err.Error(), "mappings must be an object") {
		t.Fatalf("expected mappings object error, got %v", err)
	}

	_, err = proxyHandler.rewriteMappingBody([]byte(`{"mappings":{"properties":"bad"}}`), "orders")
	if err == nil || !strings.Contains(err.Error(), "mappings.properties must be an object") {
		t.Fatalf("expected mappings.properties error, got %v", err)
	}

	_, err = proxyHandler.rewriteMappingBody([]byte(`{"properties":"bad"}`), "orders")
	if err == nil || !strings.Contains(err.Error(), "properties must be an object") {
		t.Fatalf("expected properties object error, got %v", err)
	}
}

func TestRewriteMultiSearchBodyErrors(t *testing.T) {
	proxyHandler, _ := newProxyWithServer(t, config.Default())

	cases := []struct {
		name      string
		body      string
		pathIndex string
		wantErr   string
	}{
		{
			name:    "invalid header json",
			body:    "{bad}\n",
			wantErr: "invalid msearch header",
		},
		{
			name:    "index not string",
			body:    `{"index":1}` + "\n" + `{"query":{"match_all":{}}}` + "\n",
			wantErr: "msearch index must be a string",
		},
		{
			name:    "missing index",
			body:    "{}\n" + `{"query":{"match_all":{}}}` + "\n",
			wantErr: "msearch request missing index",
		},
		{
			name:    "empty body line",
			body:    `{"index":"orders-tenant1"}` + "\n\n",
			wantErr: "msearch body line empty",
		},
		{
			name:    "missing body",
			body:    `{"index":"orders-tenant1"}`,
			wantErr: "msearch payload missing body",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := proxyHandler.rewriteMultiSearchBody([]byte(tc.body), tc.pathIndex)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %q error, got %v", tc.wantErr, err)
			}
		})
	}
}
