package proxy

import (
	"encoding/json"
	"testing"
)

// Additional edge case tests for fastjson implementation

func TestRewriteQueryBodyFastJSON_EmptyArray(t *testing.T) {
	p := setupTestProxy("per-tenant")
	query := []byte(`{"query":{"bool":{"must":[]}}}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	queryObj := output["query"].(map[string]interface{})
	boolObj := queryObj["bool"].(map[string]interface{})
	must := boolObj["must"].([]interface{})
	if len(must) != 0 {
		t.Errorf("expected empty must array, got: %v", must)
	}
}

func TestRewriteQueryBodyFastJSON_FieldsWithNonString(t *testing.T) {
	p := setupTestProxy("per-tenant")
	// Fields array with mixed types (edge case, but should handle gracefully)
	query := []byte(`{"fields":["message",123,null,true,"level"]}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	fields := output["fields"].([]interface{})
	// First field should be rewritten
	if fields[0].(string) != "logs.message" {
		t.Errorf("expected logs.message, got: %v", fields[0])
	}
	// Non-string fields should be preserved
	if fields[1].(float64) != 123 {
		t.Errorf("expected 123, got: %v", fields[1])
	}
	if fields[2] != nil {
		t.Errorf("expected null, got: %v", fields[2])
	}
	if fields[3].(bool) != true {
		t.Errorf("expected true, got: %v", fields[3])
	}
	// Last field should be rewritten
	if fields[4].(string) != "logs.level" {
		t.Errorf("expected logs.level, got: %v", fields[4])
	}
}

func TestRewriteQueryBodyFastJSON_SourceString(t *testing.T) {
	p := setupTestProxy("per-tenant")
	// _source as a string (single field)
	query := []byte(`{"_source":"message"}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	// String _source should be preserved as-is (only arrays/objects are rewritten)
	if output["_source"].(string) != "message" {
		t.Errorf("expected message, got: %v", output["_source"])
	}
}

func TestRewriteQueryBodyFastJSON_SourceBoolean(t *testing.T) {
	p := setupTestProxy("per-tenant")
	// _source as boolean (true/false)
	query := []byte(`{"_source":false}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	// Boolean _source should be preserved as-is
	if output["_source"].(bool) != false {
		t.Errorf("expected false, got: %v", output["_source"])
	}
}

func TestRewriteQueryBodyFastJSON_SortMixedTypes(t *testing.T) {
	p := setupTestProxy("per-tenant")
	// Sort with mixed types (strings, objects, and edge cases)
	query := []byte(`{"sort":["timestamp",{"level":"asc"},123,null]}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	sort := output["sort"].([]interface{})
	// String sort should be rewritten
	if sort[0].(string) != "logs.timestamp" {
		t.Errorf("expected logs.timestamp, got: %v", sort[0])
	}
	// Object sort should be rewritten
	sortObj := sort[1].(map[string]interface{})
	if _, ok := sortObj["logs.level"]; !ok {
		t.Errorf("expected logs.level in sort object, got: %v", sortObj)
	}
	// Non-string/non-object types should be preserved
	if sort[2].(float64) != 123 {
		t.Errorf("expected 123, got: %v", sort[2])
	}
	if sort[3] != nil {
		t.Errorf("expected null, got: %v", sort[3])
	}
}

func TestRewriteQueryBodyFastJSON_SourceWithNonArrayIncludes(t *testing.T) {
	p := setupTestProxy("per-tenant")
	// _source with includes as non-array (edge case)
	query := []byte(`{"_source":{"includes":"message","excludes":["internal"]}}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	source := output["_source"].(map[string]interface{})
	// Non-array includes should be preserved as-is
	if source["includes"].(string) != "message" {
		t.Errorf("expected message, got: %v", source["includes"])
	}
	// Array excludes should be rewritten
	excludes := source["excludes"].([]interface{})
	if excludes[0].(string) != "logs.internal" {
		t.Errorf("expected logs.internal, got: %v", excludes[0])
	}
}

func TestRewriteQueryBodyFastJSON_NestedArrays(t *testing.T) {
	p := setupTestProxy("per-tenant")
	// Query with deeply nested arrays
	query := []byte(`{"query":{"bool":{"should":[[{"match":{"message":"test"}}]]}}}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	// Verify nested array structure is preserved and fields are rewritten
	queryObj := output["query"].(map[string]interface{})
	boolObj := queryObj["bool"].(map[string]interface{})
	should := boolObj["should"].([]interface{})
	nested := should[0].([]interface{})
	match := nested[0].(map[string]interface{})["match"].(map[string]interface{})
	if _, ok := match["logs.message"]; !ok {
		t.Errorf("expected logs.message in nested match, got: %v", match)
	}
}

func TestRewriteQueryBodyFastJSON_FieldObjectNonObject(t *testing.T) {
	p := setupTestProxy("per-tenant")
	// Match with non-object value (edge case, should preserve)
	query := []byte(`{"query":{"match":"simple string"}}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	queryObj := output["query"].(map[string]interface{})
	// Non-object match should be preserved as-is
	if queryObj["match"].(string) != "simple string" {
		t.Errorf("expected 'simple string', got: %v", queryObj["match"])
	}
}

func TestRewriteQueryBodyFastJSON_FieldsNonArray(t *testing.T) {
	p := setupTestProxy("per-tenant")
	// Fields as non-array value (edge case)
	query := []byte(`{"fields":"message"}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	// Non-array fields should be preserved as-is
	if output["fields"].(string) != "message" {
		t.Errorf("expected message, got: %v", output["fields"])
	}
}

func TestRewriteQueryBodyFastJSON_SortNonArray(t *testing.T) {
	p := setupTestProxy("per-tenant")
	// Sort as non-array value (edge case)
	query := []byte(`{"sort":"timestamp"}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	// Non-array sort should be preserved as-is
	if output["sort"].(string) != "timestamp" {
		t.Errorf("expected timestamp, got: %v", output["sort"])
	}
}

func TestRewriteQueryBodyFastJSON_NullValues(t *testing.T) {
	p := setupTestProxy("per-tenant")
	// Query with null values
	query := []byte(`{"query":{"match":{"message":null}},"size":null}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	// Field should still be rewritten even with null value
	queryObj := output["query"].(map[string]interface{})
	match := queryObj["match"].(map[string]interface{})
	if _, ok := match["logs.message"]; !ok {
		t.Errorf("expected logs.message field, got: %v", match)
	}
	// Null values should be preserved
	if match["logs.message"] != nil {
		t.Errorf("expected null value, got: %v", match["logs.message"])
	}
	if output["size"] != nil {
		t.Errorf("expected null size, got: %v", output["size"])
	}
}

func TestRewriteQueryBodyFastJSON_NumberValues(t *testing.T) {
	p := setupTestProxy("per-tenant")
	// Query with various number types
	query := []byte(`{"query":{"range":{"count":{"gte":10,"lte":100.5}}},"size":50}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	// Field should be rewritten
	queryObj := output["query"].(map[string]interface{})
	rangeObj := queryObj["range"].(map[string]interface{})
	if _, ok := rangeObj["logs.count"]; !ok {
		t.Errorf("expected logs.count field, got: %v", rangeObj)
	}

	// Number values should be preserved
	countRange := rangeObj["logs.count"].(map[string]interface{})
	if countRange["gte"].(float64) != 10 {
		t.Errorf("expected 10, got: %v", countRange["gte"])
	}
	if countRange["lte"].(float64) != 100.5 {
		t.Errorf("expected 100.5, got: %v", countRange["lte"])
	}
	if output["size"].(float64) != 50 {
		t.Errorf("expected 50, got: %v", output["size"])
	}
}

func TestRewriteQueryBodyFastJSON_BooleanValues(t *testing.T) {
	p := setupTestProxy("per-tenant")
	// Query with boolean values
	query := []byte(`{"track_total_hits":true,"explain":false}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	// Boolean values should be preserved
	if output["track_total_hits"].(bool) != true {
		t.Errorf("expected true, got: %v", output["track_total_hits"])
	}
	if output["explain"].(bool) != false {
		t.Errorf("expected false, got: %v", output["explain"])
	}
}

func TestRewriteQueryBodyFastJSON_AlreadyPrefixedField(t *testing.T) {
	p := setupTestProxy("per-tenant")
	// Field that's already prefixed should not be double-prefixed
	query := []byte(`{"query":{"match":{"logs.message":"test"}}}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	queryObj := output["query"].(map[string]interface{})
	match := queryObj["match"].(map[string]interface{})
	// Should remain logs.message, not logs.logs.message
	if _, ok := match["logs.message"]; !ok {
		t.Errorf("expected logs.message (not double prefixed), got: %v", match)
	}
	// Verify it's not double prefixed
	if _, ok := match["logs.logs.message"]; ok {
		t.Errorf("field was double prefixed: %v", match)
	}
}

func TestRewriteQueryBodyFastJSON_EmptyFieldName(t *testing.T) {
	p := setupTestProxy("per-tenant")
	// Empty field name should be preserved (not prefixed)
	query := []byte(`{"query":{"match":{"":"test"}}}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	queryObj := output["query"].(map[string]interface{})
	match := queryObj["match"].(map[string]interface{})
	// Empty field should remain empty (not prefixed)
	if _, ok := match[""]; !ok {
		t.Errorf("expected empty field name preserved, got: %v", match)
	}
}

func TestRewriteQueryBodyFastJSON_LargeQuery(t *testing.T) {
	p := setupTestProxy("per-tenant")
	// Large query with many nested levels
	query := []byte(`{
		"query": {
			"bool": {
				"must": [
					{"match": {"field1": "value1"}},
					{"match": {"field2": "value2"}},
					{"match": {"field3": "value3"}},
					{"bool": {
						"should": [
							{"term": {"field4": "value4"}},
							{"term": {"field5": "value5"}},
							{"range": {"field6": {"gte": 0, "lte": 100}}}
						]
					}}
				],
				"filter": [
					{"prefix": {"field7": "pre"}},
					{"wildcard": {"field8": "*wild*"}},
					{"regexp": {"field9": "reg.*"}}
				]
			}
		},
		"sort": [
			{"field10": "asc"},
			{"field11": "desc"}
		],
		"_source": ["field12", "field13", "field14"],
		"fields": ["field15", "field16"],
		"size": 1000
	}`)

	result, err := p.rewriteQueryBodyFastJSON(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	// Verify all fields are rewritten correctly
	queryObj := output["query"].(map[string]interface{})
	boolObj := queryObj["bool"].(map[string]interface{})
	must := boolObj["must"].([]interface{})

	// Check a few rewritten fields
	match1 := must[0].(map[string]interface{})["match"].(map[string]interface{})
	if _, ok := match1["logs.field1"]; !ok {
		t.Errorf("expected logs.field1, got: %v", match1)
	}

	// Check nested bool
	nestedBool := must[3].(map[string]interface{})["bool"].(map[string]interface{})
	should := nestedBool["should"].([]interface{})
	term := should[0].(map[string]interface{})["term"].(map[string]interface{})
	if _, ok := term["logs.field4"]; !ok {
		t.Errorf("expected logs.field4 in nested bool, got: %v", term)
	}

	// Check sort
	sort := output["sort"].([]interface{})
	sortObj := sort[0].(map[string]interface{})
	if _, ok := sortObj["logs.field10"]; !ok {
		t.Errorf("expected logs.field10 in sort, got: %v", sortObj)
	}

	// Check _source
	source := output["_source"].([]interface{})
	if source[0].(string) != "logs.field12" {
		t.Errorf("expected logs.field12 in _source, got: %v", source[0])
	}

	// Check fields
	fields := output["fields"].([]interface{})
	if fields[0].(string) != "logs.field15" {
		t.Errorf("expected logs.field15 in fields, got: %v", fields[0])
	}
}
