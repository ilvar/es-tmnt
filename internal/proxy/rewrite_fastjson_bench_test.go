package proxy

import (
	"regexp"
	"testing"
	"text/template"

	"es-tmnt/internal/config"
)

// setupProxyForBench creates a proxy instance for benchmarking query rewrites
func setupProxyForBench(mode string) *Proxy {
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

// Test queries of varying complexity
var (
	simpleQuery = []byte(`{
		"query": {
			"match": {"message": "error"}
		}
	}`)

	complexQuery = []byte(`{
		"query": {
			"bool": {
				"must": [
					{"match": {"message": "error"}},
					{"range": {"timestamp": {"gte": "2024-01-01", "lte": "2024-12-31"}}}
				],
				"filter": [
					{"term": {"level": "error"}},
					{"prefix": {"user": "admin"}}
				]
			}
		},
		"sort": [
			{"timestamp": "desc"},
			{"score": {"order": "desc"}}
		],
		"_source": ["message", "level", "timestamp", "user"],
		"size": 100,
		"from": 0
	}`)

	veryComplexQuery = []byte(`{
		"query": {
			"bool": {
				"must": [
					{"match": {"message": "error"}},
					{"range": {"timestamp": {"gte": "2024-01-01", "lte": "2024-12-31"}}}
				],
				"filter": [
					{"term": {"level": "error"}},
					{"prefix": {"user": "admin"}},
					{"wildcard": {"path": "/api/*"}},
					{"regexp": {"hostname": "prod-.*"}}
				],
				"should": [
					{"match": {"category": "authentication"}},
					{"match": {"category": "authorization"}}
				],
				"minimum_should_match": 1
			}
		},
		"aggs": {
			"by_level": {
				"terms": {"field": "level"},
				"aggs": {
					"by_user": {
						"terms": {"field": "user"}
					}
				}
			},
			"error_rate": {
				"date_histogram": {
					"field": "timestamp",
					"interval": "1h"
				}
			}
		},
		"sort": [
			{"timestamp": "desc"},
			{"score": {"order": "desc"}},
			{"user": "asc"}
		],
		"_source": {
			"includes": ["message", "level", "timestamp", "user", "path", "hostname"],
			"excludes": ["internal.*"]
		},
		"fields": ["message", "level", "user"],
		"size": 100,
		"from": 0
	}`)

	emptyQuery = []byte(`{}`)
	matchAllQuery = []byte(`{"query": {"match_all": {}}}`)
)

// Benchmark: Simple query - stdlib
func BenchmarkRewriteQuery_Simple_Stdlib(b *testing.B) {
	p := setupProxyForBench("per-tenant")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := p.rewriteQueryBodyStdlib(simpleQuery, "logs")
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Benchmark: Simple query - fastjson
func BenchmarkRewriteQuery_Simple_FastJSON(b *testing.B) {
	p := setupProxyForBench("per-tenant")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := p.rewriteQueryBodyFastJSON(simpleQuery, "logs")
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Benchmark: Complex query - stdlib
func BenchmarkRewriteQuery_Complex_Stdlib(b *testing.B) {
	p := setupProxyForBench("per-tenant")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := p.rewriteQueryBodyStdlib(complexQuery, "logs")
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Benchmark: Complex query - fastjson
func BenchmarkRewriteQuery_Complex_FastJSON(b *testing.B) {
	p := setupProxyForBench("per-tenant")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := p.rewriteQueryBodyFastJSON(complexQuery, "logs")
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Benchmark: Very complex query - stdlib
func BenchmarkRewriteQuery_VeryComplex_Stdlib(b *testing.B) {
	p := setupProxyForBench("per-tenant")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := p.rewriteQueryBodyStdlib(veryComplexQuery, "logs")
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Benchmark: Very complex query - fastjson
func BenchmarkRewriteQuery_VeryComplex_FastJSON(b *testing.B) {
	p := setupProxyForBench("per-tenant")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := p.rewriteQueryBodyFastJSON(veryComplexQuery, "logs")
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Benchmark: Empty query - stdlib
func BenchmarkRewriteQuery_Empty_Stdlib(b *testing.B) {
	p := setupProxyForBench("per-tenant")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := p.rewriteQueryBodyStdlib(emptyQuery, "logs")
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Benchmark: Empty query - fastjson
func BenchmarkRewriteQuery_Empty_FastJSON(b *testing.B) {
	p := setupProxyForBench("per-tenant")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := p.rewriteQueryBodyFastJSON(emptyQuery, "logs")
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Benchmark: Match all query - stdlib
func BenchmarkRewriteQuery_MatchAll_Stdlib(b *testing.B) {
	p := setupProxyForBench("per-tenant")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := p.rewriteQueryBodyStdlib(matchAllQuery, "logs")
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Benchmark: Match all query - fastjson
func BenchmarkRewriteQuery_MatchAll_FastJSON(b *testing.B) {
	p := setupProxyForBench("per-tenant")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := p.rewriteQueryBodyFastJSON(matchAllQuery, "logs")
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Benchmark: Shared mode (should be no-op for both)
func BenchmarkRewriteQuery_SharedMode_Stdlib(b *testing.B) {
	p := setupProxyForBench("shared")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := p.rewriteQueryBodyStdlib(complexQuery, "logs")
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRewriteQuery_SharedMode_FastJSON(b *testing.B) {
	p := setupProxyForBench("shared")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := p.rewriteQueryBodyFastJSON(complexQuery, "logs")
		if err != nil {
			b.Fatal(err)
		}
	}
}
