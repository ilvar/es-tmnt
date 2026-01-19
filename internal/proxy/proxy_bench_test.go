package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"text/template"

	"es-tmnt/internal/config"
)

// setupBenchProxy creates a proxy instance for benchmarking
func setupBenchProxy(mode string) *Proxy {
	tenantRegex := regexp.MustCompile(`^(?P<prefix>[^-]+)-(?P<tenant>[^-]+)(?P<postfix>.*)$`)
	aliasTmpl := template.Must(template.New("alias").Parse("alias-{{.index}}-{{.tenant}}"))
	sharedIndex := template.Must(template.New("shared").Parse("{{.index}}"))
	perTenantIdx := template.Must(template.New("per-tenant").Parse("{{.index}}-{{.tenant}}"))

	cfg := config.Config{
		Mode:        mode,
		UpstreamURL: "http://localhost:9200",
		TenantRegex: config.TenantRegex{
			Pattern:  `^(?P<prefix>[^-]+)-(?P<tenant>[^-]+)(?P<postfix>.*)$`,
			Compiled: tenantRegex,
		},
		SharedIndex: config.SharedIndex{
			Name:          "{{.index}}",
			AliasTemplate: "alias-{{.index}}-{{.tenant}}",
			TenantField:   "tenant_id",
		},
		IndexPerTenant: config.IndexPerTenant{
			IndexTemplate: "{{.index}}-{{.tenant}}",
		},
	}

	indexGroup, tenantGroup, prefixGroup, postfixGroup, _ := groupIndexes(tenantRegex)

	return &Proxy{
		cfg:          cfg,
		aliasTmpl:    aliasTmpl,
		sharedIndex:  sharedIndex,
		perTenantIdx: perTenantIdx,
		indexGroup:   indexGroup,
		tenantGroup:  tenantGroup,
		prefixGroup:  prefixGroup,
		postfixGroup: postfixGroup,
		denyPatterns: nil,
	}
}

// BenchmarkParseIndex tests index parsing and regex matching overhead
func BenchmarkParseIndex(b *testing.B) {
	p := setupBenchProxy("shared")
	indexName := "logs-acme-prod"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := p.parseIndex(indexName)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRenderTemplates tests template rendering overhead
func BenchmarkRenderTemplates(b *testing.B) {
	p := setupBenchProxy("shared")

	b.Run("RenderAlias", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := p.renderAlias("logs", "acme")
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("RenderSharedIndex", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := p.renderIndex(p.sharedIndex, "logs", "acme")
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("RenderPerTenantIndex", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := p.renderIndex(p.perTenantIdx, "logs", "acme")
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkRewriteDocumentBody tests document rewriting overhead
func BenchmarkRewriteDocumentBody(b *testing.B) {
	doc := []byte(`{"message":"test log message","level":"info","timestamp":"2024-01-01T00:00:00Z","user_id":"user123","request_id":"req456"}`)

	b.Run("SharedMode", func(b *testing.B) {
		p := setupBenchProxy("shared")
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := p.rewriteDocumentBody(doc, "logs", "acme")
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("PerTenantMode", func(b *testing.B) {
		p := setupBenchProxy("per-tenant")
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := p.rewriteDocumentBody(doc, "logs", "acme")
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkRewriteQueryBody tests query rewriting overhead
func BenchmarkRewriteQueryBody(b *testing.B) {
	query := []byte(`{
		"query": {
			"bool": {
				"must": [
					{"match": {"message": "error"}},
					{"range": {"timestamp": {"gte": "2024-01-01", "lte": "2024-12-31"}}}
				],
				"filter": [
					{"term": {"level": "error"}}
				]
			}
		},
		"sort": [{"timestamp": "desc"}],
		"_source": ["message", "level", "timestamp"],
		"size": 100
	}`)

	b.Run("SharedMode_NoRewrite", func(b *testing.B) {
		p := setupBenchProxy("shared")
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := p.rewriteQueryBody(query, "logs")
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("PerTenantMode_WithRewrite", func(b *testing.B) {
		p := setupBenchProxy("per-tenant")
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := p.rewriteQueryBody(query, "logs")
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkRewriteBulkBody tests bulk request rewriting overhead
func BenchmarkRewriteBulkBody(b *testing.B) {
	// Generate bulk payload with 10 index operations
	bulk := generateBulkPayload(10)

	b.Run("SharedMode_10ops", func(b *testing.B) {
		p := setupBenchProxy("shared")
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := p.rewriteBulkBody(bulk, "logs-acme-prod")
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("PerTenantMode_10ops", func(b *testing.B) {
		p := setupBenchProxy("per-tenant")
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := p.rewriteBulkBody(bulk, "logs-acme-prod")
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	// Test with larger bulk
	bulk100 := generateBulkPayload(100)

	b.Run("SharedMode_100ops", func(b *testing.B) {
		p := setupBenchProxy("shared")
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := p.rewriteBulkBody(bulk100, "logs-acme-prod")
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("PerTenantMode_100ops", func(b *testing.B) {
		p := setupBenchProxy("per-tenant")
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := p.rewriteBulkBody(bulk100, "logs-acme-prod")
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkFullRequestHandling tests complete HTTP request handling
func BenchmarkFullRequestHandling(b *testing.B) {
	// Mock upstream server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"took": 1,
			"hits": map[string]interface{}{
				"total": map[string]interface{}{"value": 0},
				"hits":  []interface{}{},
			},
		})
	}))
	defer upstream.Close()

	// Setup proxy pointing to mock upstream
	cfg := config.Config{
		Mode:        "shared",
		UpstreamURL: upstream.URL,
		TenantRegex: config.TenantRegex{
			Pattern:  `^(?P<prefix>[^-]+)-(?P<tenant>[^-]+)(?P<postfix>.*)$`,
			Compiled: regexp.MustCompile(`^(?P<prefix>[^-]+)-(?P<tenant>[^-]+)(?P<postfix>.*)$`),
		},
		SharedIndex: config.SharedIndex{
			Name:          "{{.index}}",
			AliasTemplate: "alias-{{.index}}-{{.tenant}}",
			TenantField:   "tenant_id",
		},
		IndexPerTenant: config.IndexPerTenant{
			IndexTemplate: "{{.index}}-{{.tenant}}",
		},
	}
	p, err := New(cfg)
	if err != nil {
		b.Fatal(err)
	}

	searchBody := []byte(`{"query":{"match":{"message":"test"}}}`)

	b.Run("Search", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			req := httptest.NewRequest("POST", "/logs-acme-prod/_search", bytes.NewReader(searchBody))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			p.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				b.Fatalf("unexpected status: %d", w.Code)
			}
		}
	})

	docBody := []byte(`{"message":"test","level":"info"}`)

	b.Run("Index", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			req := httptest.NewRequest("POST", "/logs-acme-prod/_doc", bytes.NewReader(docBody))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			p.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				b.Fatalf("unexpected status: %d", w.Code)
			}
		}
	})

	bulkBody := generateBulkPayload(10)

	b.Run("Bulk_10ops", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			req := httptest.NewRequest("POST", "/_bulk", bytes.NewReader(bulkBody))
			req.Header.Set("Content-Type", "application/x-ndjson")
			w := httptest.NewRecorder()
			p.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				b.Fatalf("unexpected status: %d", w.Code)
			}
		}
	})
}

// BenchmarkJSONMarshalUnmarshal tests JSON processing overhead
func BenchmarkJSONProcessing(b *testing.B) {
	doc := map[string]interface{}{
		"message":    "test log message",
		"level":      "info",
		"timestamp":  "2024-01-01T00:00:00Z",
		"user_id":    "user123",
		"request_id": "req456",
	}

	b.Run("Marshal", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, err := json.Marshal(doc)
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	docJSON, _ := json.Marshal(doc)

	b.Run("Unmarshal", func(b *testing.B) {
		var result map[string]interface{}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			err := json.Unmarshal(docJSON, &result)
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("MarshalUnmarshal", func(b *testing.B) {
		var result map[string]interface{}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			data, err := json.Marshal(doc)
			if err != nil {
				b.Fatal(err)
			}
			err = json.Unmarshal(data, &result)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkIOOperations tests I/O overhead
func BenchmarkIOOperations(b *testing.B) {
	data := []byte(`{"message":"test log message","level":"info","timestamp":"2024-01-01T00:00:00Z"}`)

	b.Run("ReadAll", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			reader := bytes.NewReader(data)
			_, err := io.ReadAll(reader)
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("BytesNewReader", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = bytes.NewReader(data)
		}
	})

	b.Run("NopCloser", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			reader := bytes.NewReader(data)
			_ = io.NopCloser(reader)
		}
	})
}

// Helper function to generate bulk payloads
func generateBulkPayload(numOps int) []byte {
	var buf bytes.Buffer
	for i := 0; i < numOps; i++ {
		action := map[string]map[string]interface{}{
			"index": {
				"_index": "logs-acme-prod",
				"_id":    "doc-" + string(rune(i)),
			},
		}
		doc := map[string]interface{}{
			"message":   "test log message",
			"level":     "info",
			"timestamp": "2024-01-01T00:00:00Z",
		}
		actionJSON, _ := json.Marshal(action)
		docJSON, _ := json.Marshal(doc)
		buf.Write(actionJSON)
		buf.WriteByte('\n')
		buf.Write(docJSON)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}
