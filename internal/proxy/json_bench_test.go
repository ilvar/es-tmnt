package proxy

import (
	"encoding/json"
	"testing"
)

// Benchmark standard library vs potential alternatives
// Note: This file shows what to test. Install libraries with:
// go get github.com/json-iterator/go
// go get github.com/bytedance/sonic
// go get github.com/valyala/fastjson

var testDoc = map[string]interface{}{
	"query": map[string]interface{}{
		"bool": map[string]interface{}{
			"must": []interface{}{
				map[string]interface{}{"match": map[string]interface{}{"message": "error"}},
				map[string]interface{}{"range": map[string]interface{}{
					"timestamp": map[string]interface{}{"gte": "2024-01-01", "lte": "2024-12-31"},
				}},
			},
			"filter": []interface{}{
				map[string]interface{}{"term": map[string]interface{}{"level": "error"}},
			},
		},
	},
	"sort":    []interface{}{map[string]interface{}{"timestamp": "desc"}},
	"_source": []interface{}{"message", "level", "timestamp"},
	"size":    100,
}

var testDocJSON, _ = json.Marshal(testDoc)

// Baseline: Standard library
func BenchmarkStdJSON_Marshal(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := json.Marshal(testDoc)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStdJSON_Unmarshal(b *testing.B) {
	b.ReportAllocs()
	var result map[string]interface{}
	for i := 0; i < b.N; i++ {
		err := json.Unmarshal(testDocJSON, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStdJSON_MarshalUnmarshal(b *testing.B) {
	b.ReportAllocs()
	var result map[string]interface{}
	for i := 0; i < b.N; i++ {
		data, err := json.Marshal(testDoc)
		if err != nil {
			b.Fatal(err)
		}
		err = json.Unmarshal(data, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// To enable these benchmarks:
// 1. Uncomment the imports and benchmark functions
// 2. Run: go get github.com/json-iterator/go github.com/bytedance/sonic github.com/valyala/fastjson
// 3. Run: go test -bench=BenchmarkJSON -benchmem ./internal/proxy/

/*
import (
	jsoniter "github.com/json-iterator/go"
	"github.com/bytedance/sonic"
	"github.com/valyala/fastjson"
)

var jsoniterAPI = jsoniter.ConfigCompatibleWithStandardLibrary

func BenchmarkJsoniter_Marshal(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := jsoniterAPI.Marshal(testDoc)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJsoniter_Unmarshal(b *testing.B) {
	b.ReportAllocs()
	var result map[string]interface{}
	for i := 0; i < b.N; i++ {
		err := jsoniterAPI.Unmarshal(testDocJSON, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJsoniter_MarshalUnmarshal(b *testing.B) {
	b.ReportAllocs()
	var result map[string]interface{}
	for i := 0; i < b.N; i++ {
		data, err := jsoniterAPI.Marshal(testDoc)
		if err != nil {
			b.Fatal(err)
		}
		err = jsoniterAPI.Unmarshal(data, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSonic_Marshal(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := sonic.Marshal(testDoc)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSonic_Unmarshal(b *testing.B) {
	b.ReportAllocs()
	var result map[string]interface{}
	for i := 0; i < b.N; i++ {
		err := sonic.Unmarshal(testDocJSON, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSonic_MarshalUnmarshal(b *testing.B) {
	b.ReportAllocs()
	var result map[string]interface{}
	for i := 0; i < b.N; i++ {
		data, err := sonic.Marshal(testDoc)
		if err != nil {
			b.Fatal(err)
		}
		err = sonic.Unmarshal(data, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFastJSON_Parse(b *testing.B) {
	b.ReportAllocs()
	var p fastjson.Parser
	for i := 0; i < b.N; i++ {
		_, err := p.ParseBytes(testDocJSON)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFastJSON_ParseAndAccess(b *testing.B) {
	b.ReportAllocs()
	var p fastjson.Parser
	for i := 0; i < b.N; i++ {
		v, err := p.ParseBytes(testDocJSON)
		if err != nil {
			b.Fatal(err)
		}
		// Simulate field access for rewriting
		_ = v.Get("query", "bool", "must")
		_ = v.Get("sort")
		_ = v.Get("_source")
	}
}
*/
