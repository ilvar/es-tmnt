package proxy

import (
	"encoding/json"
	"regexp"
	"testing"
	"text/template"

	"es-tmnt/internal/config"
)

// setupTestProxy creates a minimal proxy for testing
func setupTestProxy(mode string) *Proxy {
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

func TestRewriteQueryBodyFastJSON_SharedMode(t *testing.T) {
	p := setupTestProxy("shared")
	query := []byte(`{"query":{"match":{"message":"test"}}}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Shared mode should return body unchanged
	if string(result) != string(query) {
		t.Errorf("shared mode should not modify query, got: %s", result)
	}
}

func TestRewriteQueryBodyFastJSON_EmptyQuery(t *testing.T) {
	p := setupTestProxy("per-tenant")
	query := []byte(`{}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Empty query should be returned unchanged
	if string(result) != string(query) {
		t.Errorf("empty query should not be modified, got: %s", result)
	}
}

func TestRewriteQueryBodyFastJSON_InvalidJSON(t *testing.T) {
	p := setupTestProxy("per-tenant")
	query := []byte(`{invalid json`)

	_, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !contains(err.Error(), "invalid JSON body") {
		t.Errorf("expected invalid JSON error, got: %v", err)
	}
}

func TestRewriteQueryBodyFastJSON_SimpleMatch(t *testing.T) {
	p := setupTestProxy("per-tenant")
	query := []byte(`{"query":{"match":{"message":"error"}}}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	// Check that field was prefixed
	queryObj := output["query"].(map[string]interface{})
	matchObj := queryObj["match"].(map[string]interface{})
	if _, ok := matchObj["logs.message"]; !ok {
		t.Errorf("expected logs.message field, got: %v", matchObj)
	}
}

func TestRewriteQueryBodyFastJSON_Term(t *testing.T) {
	p := setupTestProxy("per-tenant")
	query := []byte(`{"query":{"term":{"level":"error"}}}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	queryObj := output["query"].(map[string]interface{})
	termObj := queryObj["term"].(map[string]interface{})
	if _, ok := termObj["logs.level"]; !ok {
		t.Errorf("expected logs.level field, got: %v", termObj)
	}
}

func TestRewriteQueryBodyFastJSON_Range(t *testing.T) {
	p := setupTestProxy("per-tenant")
	query := []byte(`{"query":{"range":{"timestamp":{"gte":"2024-01-01"}}}}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	queryObj := output["query"].(map[string]interface{})
	rangeObj := queryObj["range"].(map[string]interface{})
	if _, ok := rangeObj["logs.timestamp"]; !ok {
		t.Errorf("expected logs.timestamp field, got: %v", rangeObj)
	}
}

func TestRewriteQueryBodyFastJSON_Prefix(t *testing.T) {
	p := setupTestProxy("per-tenant")
	query := []byte(`{"query":{"prefix":{"user":"admin"}}}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	queryObj := output["query"].(map[string]interface{})
	prefixObj := queryObj["prefix"].(map[string]interface{})
	if _, ok := prefixObj["logs.user"]; !ok {
		t.Errorf("expected logs.user field, got: %v", prefixObj)
	}
}

func TestRewriteQueryBodyFastJSON_Wildcard(t *testing.T) {
	p := setupTestProxy("per-tenant")
	query := []byte(`{"query":{"wildcard":{"path":"/api/*"}}}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	queryObj := output["query"].(map[string]interface{})
	wildcardObj := queryObj["wildcard"].(map[string]interface{})
	if _, ok := wildcardObj["logs.path"]; !ok {
		t.Errorf("expected logs.path field, got: %v", wildcardObj)
	}
}

func TestRewriteQueryBodyFastJSON_Regexp(t *testing.T) {
	p := setupTestProxy("per-tenant")
	query := []byte(`{"query":{"regexp":{"hostname":"prod-.*"}}}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	queryObj := output["query"].(map[string]interface{})
	regexpObj := queryObj["regexp"].(map[string]interface{})
	if _, ok := regexpObj["logs.hostname"]; !ok {
		t.Errorf("expected logs.hostname field, got: %v", regexpObj)
	}
}

func TestRewriteQueryBodyFastJSON_FieldsList(t *testing.T) {
	p := setupTestProxy("per-tenant")
	query := []byte(`{"fields":["message","level","timestamp"]}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	fields := output["fields"].([]interface{})
	expected := []string{"logs.message", "logs.level", "logs.timestamp"}
	for i, field := range fields {
		if field.(string) != expected[i] {
			t.Errorf("expected field %s at position %d, got %s", expected[i], i, field)
		}
	}
}

func TestRewriteQueryBodyFastJSON_SortString(t *testing.T) {
	p := setupTestProxy("per-tenant")
	query := []byte(`{"sort":["timestamp","level"]}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	sort := output["sort"].([]interface{})
	expected := []string{"logs.timestamp", "logs.level"}
	for i, field := range sort {
		if field.(string) != expected[i] {
			t.Errorf("expected sort field %s at position %d, got %s", expected[i], i, field)
		}
	}
}

func TestRewriteQueryBodyFastJSON_SortObject(t *testing.T) {
	p := setupTestProxy("per-tenant")
	query := []byte(`{"sort":[{"timestamp":"desc"},{"level":"asc"}]}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	sort := output["sort"].([]interface{})

	// Check first sort field
	sort0 := sort[0].(map[string]interface{})
	if _, ok := sort0["logs.timestamp"]; !ok {
		t.Errorf("expected logs.timestamp in sort, got: %v", sort0)
	}

	// Check second sort field
	sort1 := sort[1].(map[string]interface{})
	if _, ok := sort1["logs.level"]; !ok {
		t.Errorf("expected logs.level in sort, got: %v", sort1)
	}
}

func TestRewriteQueryBodyFastJSON_SourceArray(t *testing.T) {
	p := setupTestProxy("per-tenant")
	query := []byte(`{"_source":["message","level","timestamp"]}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	source := output["_source"].([]interface{})
	expected := []string{"logs.message", "logs.level", "logs.timestamp"}
	for i, field := range source {
		if field.(string) != expected[i] {
			t.Errorf("expected _source field %s at position %d, got %s", expected[i], i, field)
		}
	}
}

func TestRewriteQueryBodyFastJSON_SourceObject(t *testing.T) {
	p := setupTestProxy("per-tenant")
	query := []byte(`{"_source":{"includes":["message","level"],"excludes":["internal"]}}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	source := output["_source"].(map[string]interface{})

	includes := source["includes"].([]interface{})
	expectedIncludes := []string{"logs.message", "logs.level"}
	for i, field := range includes {
		if field.(string) != expectedIncludes[i] {
			t.Errorf("expected includes field %s at position %d, got %s", expectedIncludes[i], i, field)
		}
	}

	excludes := source["excludes"].([]interface{})
	expectedExcludes := []string{"logs.internal"}
	for i, field := range excludes {
		if field.(string) != expectedExcludes[i] {
			t.Errorf("expected excludes field %s at position %d, got %s", expectedExcludes[i], i, field)
		}
	}
}

func TestRewriteQueryBodyFastJSON_ComplexBool(t *testing.T) {
	p := setupTestProxy("per-tenant")
	query := []byte(`{
		"query": {
			"bool": {
				"must": [
					{"match": {"message": "error"}},
					{"range": {"timestamp": {"gte": "2024-01-01"}}}
				],
				"filter": [
					{"term": {"level": "error"}}
				]
			}
		}
	}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	// Verify nested field rewriting
	queryObj := output["query"].(map[string]interface{})
	boolObj := queryObj["bool"].(map[string]interface{})

	must := boolObj["must"].([]interface{})
	match := must[0].(map[string]interface{})["match"].(map[string]interface{})
	if _, ok := match["logs.message"]; !ok {
		t.Errorf("expected logs.message in match, got: %v", match)
	}

	rangeClause := must[1].(map[string]interface{})["range"].(map[string]interface{})
	if _, ok := rangeClause["logs.timestamp"]; !ok {
		t.Errorf("expected logs.timestamp in range, got: %v", rangeClause)
	}

	filter := boolObj["filter"].([]interface{})
	term := filter[0].(map[string]interface{})["term"].(map[string]interface{})
	if _, ok := term["logs.level"]; !ok {
		t.Errorf("expected logs.level in term, got: %v", term)
	}
}

func TestRewriteQueryBodyFastJSON_NestedAggregations(t *testing.T) {
	p := setupTestProxy("per-tenant")
	query := []byte(`{
		"aggs": {
			"by_level": {
				"terms": {"field": "level"},
				"aggs": {
					"by_user": {
						"terms": {"field": "user"}
					}
				}
			}
		}
	}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	// Verify aggregations are preserved (not rewritten)
	// The rewriter only rewrites specific query clause fields like match, term, range
	// Aggregation "field" references are not rewritten
	aggs := output["aggs"].(map[string]interface{})
	byLevel := aggs["by_level"].(map[string]interface{})
	terms := byLevel["terms"].(map[string]interface{})
	if terms["field"].(string) != "level" {
		t.Errorf("expected level field (not rewritten), got: %v", terms["field"])
	}

	nestedAggs := byLevel["aggs"].(map[string]interface{})
	byUser := nestedAggs["by_user"].(map[string]interface{})
	userTerms := byUser["terms"].(map[string]interface{})
	if userTerms["field"].(string) != "user" {
		t.Errorf("expected user field (not rewritten), got: %v", userTerms["field"])
	}
}

func TestRewriteQueryBodyFastJSON_PreservesPrimitives(t *testing.T) {
	p := setupTestProxy("per-tenant")
	query := []byte(`{
		"query": {"match": {"message": "test"}},
		"size": 100,
		"from": 0,
		"timeout": "10s",
		"track_total_hits": true
	}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	// Verify primitives are preserved
	if output["size"].(float64) != 100 {
		t.Errorf("expected size 100, got: %v", output["size"])
	}
	if output["from"].(float64) != 0 {
		t.Errorf("expected from 0, got: %v", output["from"])
	}
	if output["timeout"].(string) != "10s" {
		t.Errorf("expected timeout 10s, got: %v", output["timeout"])
	}
	if output["track_total_hits"].(bool) != true {
		t.Errorf("expected track_total_hits true, got: %v", output["track_total_hits"])
	}
}

func TestRewriteQueryBodyFastJSON_ExistsClause(t *testing.T) {
	p := setupTestProxy("per-tenant")
	// Test that exists clauses preserve field names (not rewritten)
	query := []byte(`{"query":{"bool":{"must":[{"exists":{"field":"message"}}]}}}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	// Verify exists field is NOT rewritten (only match, term, range, etc are rewritten)
	queryObj := output["query"].(map[string]interface{})
	boolObj := queryObj["bool"].(map[string]interface{})
	must := boolObj["must"].([]interface{})
	exists := must[0].(map[string]interface{})["exists"].(map[string]interface{})
	if exists["field"].(string) != "message" {
		t.Errorf("expected message field (not rewritten), got: %v", exists["field"])
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) >= len(substr) && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
