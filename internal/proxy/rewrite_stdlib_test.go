package proxy

import (
	"encoding/json"
	"testing"
)

// Tests for stdlib implementation to maintain coverage
// These test the original encoding/json-based rewriter

func TestRewriteQueryBodyStdlib_SharedMode(t *testing.T) {
	p := setupTestProxy("shared")
	query := []byte(`{"query":{"match":{"message":"test"}}}`)

	result, err := p.rewriteQueryBodyStdlib(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Shared mode should return body unchanged
	if string(result) != string(query) {
		t.Errorf("shared mode should not modify query, got: %s", result)
	}
}

func TestRewriteQueryBodyStdlib_SimpleQuery(t *testing.T) {
	p := setupTestProxy("per-tenant")
	query := []byte(`{"query":{"match":{"message":"error"}}}`)

	result, err := p.rewriteQueryBodyStdlib(query, "logs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal(result, &output); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}

	queryObj := output["query"].(map[string]interface{})
	matchObj := queryObj["match"].(map[string]interface{})
	if _, ok := matchObj["logs.message"]; !ok {
		t.Errorf("expected logs.message field, got: %v", matchObj)
	}
}

func TestRewriteQueryBodyStdlib_InvalidJSON(t *testing.T) {
	p := setupTestProxy("per-tenant")
	query := []byte(`{invalid`)

	_, err := p.rewriteQueryBodyStdlib(query, "logs")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestRewriteQueryValue_NestedStructures(t *testing.T) {
	p := setupTestProxy("per-tenant")

	// Test match clause
	input := map[string]interface{}{
		"match": map[string]interface{}{
			"field1": "value1",
		},
	}
	result := p.rewriteQueryValue(input, "logs")
	resultMap := result.(map[string]interface{})
	matchMap := resultMap["match"].(map[string]interface{})
	if _, ok := matchMap["logs.field1"]; !ok {
		t.Errorf("expected logs.field1 in match, got: %v", matchMap)
	}

	// Test term clause
	input = map[string]interface{}{
		"term": map[string]interface{}{
			"status": "active",
		},
	}
	result = p.rewriteQueryValue(input, "logs")
	resultMap = result.(map[string]interface{})
	termMap := resultMap["term"].(map[string]interface{})
	if _, ok := termMap["logs.status"]; !ok {
		t.Errorf("expected logs.status in term, got: %v", termMap)
	}

	// Test range clause
	input = map[string]interface{}{
		"range": map[string]interface{}{
			"timestamp": map[string]interface{}{
				"gte": "2024-01-01",
			},
		},
	}
	result = p.rewriteQueryValue(input, "logs")
	resultMap = result.(map[string]interface{})
	rangeMap := resultMap["range"].(map[string]interface{})
	if _, ok := rangeMap["logs.timestamp"]; !ok {
		t.Errorf("expected logs.timestamp in range, got: %v", rangeMap)
	}

	// Test prefix clause
	input = map[string]interface{}{
		"prefix": map[string]interface{}{
			"user": "admin",
		},
	}
	result = p.rewriteQueryValue(input, "logs")
	resultMap = result.(map[string]interface{})
	prefixMap := resultMap["prefix"].(map[string]interface{})
	if _, ok := prefixMap["logs.user"]; !ok {
		t.Errorf("expected logs.user in prefix, got: %v", prefixMap)
	}

	// Test wildcard clause
	input = map[string]interface{}{
		"wildcard": map[string]interface{}{
			"path": "/api/*",
		},
	}
	result = p.rewriteQueryValue(input, "logs")
	resultMap = result.(map[string]interface{})
	wildcardMap := resultMap["wildcard"].(map[string]interface{})
	if _, ok := wildcardMap["logs.path"]; !ok {
		t.Errorf("expected logs.path in wildcard, got: %v", wildcardMap)
	}

	// Test regexp clause
	input = map[string]interface{}{
		"regexp": map[string]interface{}{
			"hostname": "prod-.*",
		},
	}
	result = p.rewriteQueryValue(input, "logs")
	resultMap = result.(map[string]interface{})
	regexpMap := resultMap["regexp"].(map[string]interface{})
	if _, ok := regexpMap["logs.hostname"]; !ok {
		t.Errorf("expected logs.hostname in regexp, got: %v", regexpMap)
	}
}

func TestRewriteQueryValue_FieldsList(t *testing.T) {
	p := setupTestProxy("per-tenant")

	input := map[string]interface{}{
		"fields": []interface{}{"field1", "field2", "field3"},
	}
	result := p.rewriteQueryValue(input, "logs")
	resultMap := result.(map[string]interface{})
	fields := resultMap["fields"].([]interface{})

	expected := []string{"logs.field1", "logs.field2", "logs.field3"}
	for i, field := range fields {
		if field.(string) != expected[i] {
			t.Errorf("expected %s at position %d, got %s", expected[i], i, field)
		}
	}
}

func TestRewriteSourceFilter_Array(t *testing.T) {
	p := setupTestProxy("per-tenant")

	// Array of field names
	input := []interface{}{"field1", "field2", "field3"}
	result := p.rewriteSourceFilter(input, "logs")
	resultArray := result.([]interface{})

	expected := []string{"logs.field1", "logs.field2", "logs.field3"}
	for i, field := range resultArray {
		if field.(string) != expected[i] {
			t.Errorf("expected %s at position %d, got %s", expected[i], i, field)
		}
	}

	// Array with non-string values
	input = []interface{}{"field1", 123, nil, true}
	result = p.rewriteSourceFilter(input, "logs")
	resultArray = result.([]interface{})
	if resultArray[0].(string) != "logs.field1" {
		t.Errorf("expected logs.field1, got: %v", resultArray[0])
	}
	if resultArray[1].(int) != 123 {
		t.Errorf("expected 123, got: %v", resultArray[1])
	}
}

func TestRewriteSourceFilter_Object(t *testing.T) {
	p := setupTestProxy("per-tenant")

	// Object with includes array
	input := map[string]interface{}{
		"includes": []interface{}{"field1", "field2"},
		"excludes": []interface{}{"field3", "field4"},
	}
	result := p.rewriteSourceFilter(input, "logs")
	resultMap := result.(map[string]interface{})

	includes := resultMap["includes"].([]interface{})
	if includes[0].(string) != "logs.field1" {
		t.Errorf("expected logs.field1 in includes, got: %v", includes[0])
	}
	if includes[1].(string) != "logs.field2" {
		t.Errorf("expected logs.field2 in includes, got: %v", includes[1])
	}

	excludes := resultMap["excludes"].([]interface{})
	if excludes[0].(string) != "logs.field3" {
		t.Errorf("expected logs.field3 in excludes, got: %v", excludes[0])
	}
	if excludes[1].(string) != "logs.field4" {
		t.Errorf("expected logs.field4 in excludes, got: %v", excludes[1])
	}

	// Object with includes as non-array
	input = map[string]interface{}{
		"includes": "field1",
	}
	result = p.rewriteSourceFilter(input, "logs")
	resultMap = result.(map[string]interface{})
	if resultMap["includes"].(string) != "field1" {
		t.Errorf("expected field1, got: %v", resultMap["includes"])
	}
}

func TestRewriteSourceFilter_Primitives(t *testing.T) {
	p := setupTestProxy("per-tenant")

	// String
	result := p.rewriteSourceFilter("field1", "logs")
	if result.(string) != "field1" {
		t.Errorf("expected field1, got: %v", result)
	}

	// Number
	result = p.rewriteSourceFilter(123, "logs")
	if result.(int) != 123 {
		t.Errorf("expected 123, got: %v", result)
	}

	// Boolean
	result = p.rewriteSourceFilter(true, "logs")
	if result.(bool) != true {
		t.Errorf("expected true, got: %v", result)
	}

	// Nil
	result = p.rewriteSourceFilter(nil, "logs")
	if result != nil {
		t.Errorf("expected nil, got: %v", result)
	}
}

func TestRewriteSortValue_StringArray(t *testing.T) {
	p := setupTestProxy("per-tenant")

	input := []interface{}{"field1", "field2", "field3"}
	result := p.rewriteSortValue(input, "logs")
	resultArray := result.([]interface{})

	expected := []string{"logs.field1", "logs.field2", "logs.field3"}
	for i, field := range resultArray {
		if field.(string) != expected[i] {
			t.Errorf("expected %s at position %d, got %s", expected[i], i, field)
		}
	}
}

func TestRewriteSortValue_ObjectArray(t *testing.T) {
	p := setupTestProxy("per-tenant")

	input := []interface{}{
		map[string]interface{}{"field1": "asc"},
		map[string]interface{}{"field2": "desc"},
	}
	result := p.rewriteSortValue(input, "logs")
	resultArray := result.([]interface{})

	// Check first sort field
	sort0 := resultArray[0].(map[string]interface{})
	if _, ok := sort0["logs.field1"]; !ok {
		t.Errorf("expected logs.field1 in sort, got: %v", sort0)
	}
	if sort0["logs.field1"].(string) != "asc" {
		t.Errorf("expected asc order, got: %v", sort0["logs.field1"])
	}

	// Check second sort field
	sort1 := resultArray[1].(map[string]interface{})
	if _, ok := sort1["logs.field2"]; !ok {
		t.Errorf("expected logs.field2 in sort, got: %v", sort1)
	}
	if sort1["logs.field2"].(string) != "desc" {
		t.Errorf("expected desc order, got: %v", sort1["logs.field2"])
	}
}

func TestRewriteSortValue_MixedTypes(t *testing.T) {
	p := setupTestProxy("per-tenant")

	input := []interface{}{
		"field1",
		map[string]interface{}{"field2": "desc"},
		123,
		nil,
		true,
	}
	result := p.rewriteSortValue(input, "logs")
	resultArray := result.([]interface{})

	// String should be rewritten
	if resultArray[0].(string) != "logs.field1" {
		t.Errorf("expected logs.field1, got: %v", resultArray[0])
	}

	// Object should be rewritten
	sortObj := resultArray[1].(map[string]interface{})
	if _, ok := sortObj["logs.field2"]; !ok {
		t.Errorf("expected logs.field2 in sort object, got: %v", sortObj)
	}

	// Other types should be preserved
	if resultArray[2].(int) != 123 {
		t.Errorf("expected 123, got: %v", resultArray[2])
	}
	if resultArray[3] != nil {
		t.Errorf("expected nil, got: %v", resultArray[3])
	}
	if resultArray[4].(bool) != true {
		t.Errorf("expected true, got: %v", resultArray[4])
	}
}

func TestRewriteSortValue_NonArray(t *testing.T) {
	p := setupTestProxy("per-tenant")

	// Non-array should be returned as-is
	result := p.rewriteSortValue("field1", "logs")
	if result.(string) != "field1" {
		t.Errorf("expected field1, got: %v", result)
	}

	result = p.rewriteSortValue(123, "logs")
	if result.(int) != 123 {
		t.Errorf("expected 123, got: %v", result)
	}
}

func TestRewriteQueryValue_Arrays(t *testing.T) {
	p := setupTestProxy("per-tenant")

	// Array of primitives
	input := []interface{}{"value1", 123, true, nil}
	result := p.rewriteQueryValue(input, "logs")
	resultArray := result.([]interface{})
	if len(resultArray) != 4 {
		t.Errorf("expected 4 elements, got %d", len(resultArray))
	}

	// Array of objects
	input = []interface{}{
		map[string]interface{}{"match": map[string]interface{}{"field1": "value1"}},
		map[string]interface{}{"term": map[string]interface{}{"field2": "value2"}},
	}
	result = p.rewriteQueryValue(input, "logs")
	resultArray = result.([]interface{})

	match := resultArray[0].(map[string]interface{})["match"].(map[string]interface{})
	if _, ok := match["logs.field1"]; !ok {
		t.Errorf("expected logs.field1 in match, got: %v", match)
	}

	term := resultArray[1].(map[string]interface{})["term"].(map[string]interface{})
	if _, ok := term["logs.field2"]; !ok {
		t.Errorf("expected logs.field2 in term, got: %v", term)
	}
}

func TestRewriteQueryValue_Primitives(t *testing.T) {
	p := setupTestProxy("per-tenant")

	// Test all primitive types are returned as-is
	result := p.rewriteQueryValue("string", "logs")
	if result.(string) != "string" {
		t.Errorf("expected string, got: %v", result)
	}

	result = p.rewriteQueryValue(123, "logs")
	if result.(int) != 123 {
		t.Errorf("expected 123, got: %v", result)
	}

	result = p.rewriteQueryValue(123.45, "logs")
	if result.(float64) != 123.45 {
		t.Errorf("expected 123.45, got: %v", result)
	}

	result = p.rewriteQueryValue(true, "logs")
	if result.(bool) != true {
		t.Errorf("expected true, got: %v", result)
	}

	result = p.rewriteQueryValue(false, "logs")
	if result.(bool) != false {
		t.Errorf("expected false, got: %v", result)
	}

	result = p.rewriteQueryValue(nil, "logs")
	if result != nil {
		t.Errorf("expected nil, got: %v", result)
	}
}

func TestRewriteQueryValue_NestedBool(t *testing.T) {
	p := setupTestProxy("per-tenant")

	input := map[string]interface{}{
		"bool": map[string]interface{}{
			"must": []interface{}{
				map[string]interface{}{"match": map[string]interface{}{"field1": "value1"}},
				map[string]interface{}{"term": map[string]interface{}{"field2": "value2"}},
			},
			"should": []interface{}{
				map[string]interface{}{"range": map[string]interface{}{"field3": map[string]interface{}{"gte": 100}}},
			},
			"filter": []interface{}{
				map[string]interface{}{"prefix": map[string]interface{}{"field4": "pre"}},
			},
		},
	}

	result := p.rewriteQueryValue(input, "logs")
	resultMap := result.(map[string]interface{})
	boolMap := resultMap["bool"].(map[string]interface{})

	// Check must clause
	must := boolMap["must"].([]interface{})
	match := must[0].(map[string]interface{})["match"].(map[string]interface{})
	if _, ok := match["logs.field1"]; !ok {
		t.Errorf("expected logs.field1 in must match, got: %v", match)
	}

	term := must[1].(map[string]interface{})["term"].(map[string]interface{})
	if _, ok := term["logs.field2"]; !ok {
		t.Errorf("expected logs.field2 in must term, got: %v", term)
	}

	// Check should clause
	should := boolMap["should"].([]interface{})
	rangeClause := should[0].(map[string]interface{})["range"].(map[string]interface{})
	if _, ok := rangeClause["logs.field3"]; !ok {
		t.Errorf("expected logs.field3 in should range, got: %v", rangeClause)
	}

	// Check filter clause
	filter := boolMap["filter"].([]interface{})
	prefixClause := filter[0].(map[string]interface{})["prefix"].(map[string]interface{})
	if _, ok := prefixClause["logs.field4"]; !ok {
		t.Errorf("expected logs.field4 in filter prefix, got: %v", prefixClause)
	}
}

func TestRewriteQueryValue_Sort(t *testing.T) {
	p := setupTestProxy("per-tenant")

	input := map[string]interface{}{
		"sort": []interface{}{
			"field1",
			map[string]interface{}{"field2": "desc"},
		},
	}

	result := p.rewriteQueryValue(input, "logs")
	resultMap := result.(map[string]interface{})
	sort := resultMap["sort"].([]interface{})

	if sort[0].(string) != "logs.field1" {
		t.Errorf("expected logs.field1, got: %v", sort[0])
	}

	sortObj := sort[1].(map[string]interface{})
	if _, ok := sortObj["logs.field2"]; !ok {
		t.Errorf("expected logs.field2 in sort, got: %v", sortObj)
	}
}

func TestRewriteQueryValue_Source(t *testing.T) {
	p := setupTestProxy("per-tenant")

	// _source as array
	input := map[string]interface{}{
		"_source": []interface{}{"field1", "field2"},
	}
	result := p.rewriteQueryValue(input, "logs")
	resultMap := result.(map[string]interface{})
	source := resultMap["_source"].([]interface{})
	if source[0].(string) != "logs.field1" {
		t.Errorf("expected logs.field1, got: %v", source[0])
	}

	// _source as object
	input = map[string]interface{}{
		"_source": map[string]interface{}{
			"includes": []interface{}{"field1"},
			"excludes": []interface{}{"field2"},
		},
	}
	result = p.rewriteQueryValue(input, "logs")
	resultMap = result.(map[string]interface{})
	sourceObj := resultMap["_source"].(map[string]interface{})
	includes := sourceObj["includes"].([]interface{})
	if includes[0].(string) != "logs.field1" {
		t.Errorf("expected logs.field1 in includes, got: %v", includes[0])
	}
}
