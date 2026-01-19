package proxy

import (
	"fmt"
	"strings"

	"github.com/valyala/fastjson"
)

// rewriteQueryBodyFastJSON rewrites query bodies using fastjson for better performance.
// This implementation uses zero-allocation parsing and efficient field rewriting.
func (p *Proxy) rewriteQueryBodyFastJSON(body []byte, baseIndex string) ([]byte, error) {
	if isSharedMode(p.cfg.Mode) {
		return body, nil
	}

	var parser fastjson.Parser
	v, err := parser.ParseBytes(body)
	if err != nil {
		return nil, fmt.Errorf("invalid JSON body: %w", err)
	}

	// Fast path: empty query
	if v.Type() == fastjson.TypeObject && len(v.GetObject().String()) == 2 { // "{}"
		return body, nil
	}

	// Use arena for efficient memory allocation
	var arena fastjson.Arena
	rewritten := p.rewriteQueryValueFastJSON(v, baseIndex, &arena)

	return rewritten.MarshalTo(nil), nil
}

// rewriteQueryValueFastJSON recursively rewrites a fastjson Value
func (p *Proxy) rewriteQueryValueFastJSON(v *fastjson.Value, baseIndex string, arena *fastjson.Arena) *fastjson.Value {
	switch v.Type() {
	case fastjson.TypeObject:
		return p.rewriteObjectFastJSON(v, baseIndex, arena)
	case fastjson.TypeArray:
		return p.rewriteArrayFastJSON(v, baseIndex, arena)
	default:
		// Primitive values (string, number, bool, null) are returned as-is
		return v
	}
}

// rewriteObjectFastJSON rewrites a JSON object
func (p *Proxy) rewriteObjectFastJSON(v *fastjson.Value, baseIndex string, arena *fastjson.Arena) *fastjson.Value {
	obj := v.GetObject()
	if obj == nil {
		return v
	}

	result := arena.NewObject()

	obj.Visit(func(key []byte, v *fastjson.Value) {
		keyStr := string(key)

		switch keyStr {
		case "match", "term", "range", "prefix", "wildcard", "regexp":
			// Rewrite field names in query clauses
			rewritten := p.rewriteFieldObjectFastJSON(v, baseIndex, arena)
			result.Set(keyStr, rewritten)

		case "fields":
			// Rewrite field list
			rewritten := p.rewriteFieldListFastJSON(v, baseIndex, arena)
			result.Set(keyStr, rewritten)

		case "sort":
			// Rewrite sort fields
			rewritten := p.rewriteSortValueFastJSON(v, baseIndex, arena)
			result.Set(keyStr, rewritten)

		case "_source":
			// Rewrite _source filter
			rewritten := p.rewriteSourceFilterFastJSON(v, baseIndex, arena)
			result.Set(keyStr, rewritten)

		default:
			// Recursively rewrite nested values
			rewritten := p.rewriteQueryValueFastJSON(v, baseIndex, arena)
			result.Set(keyStr, rewritten)
		}
	})

	return result
}

// rewriteArrayFastJSON rewrites a JSON array
func (p *Proxy) rewriteArrayFastJSON(v *fastjson.Value, baseIndex string, arena *fastjson.Arena) *fastjson.Value {
	arr := v.GetArray()
	if arr == nil {
		return v
	}

	result := arena.NewArray()
	for _, item := range arr {
		rewritten := p.rewriteQueryValueFastJSON(item, baseIndex, arena)
		result.SetArrayItem(len(result.GetArray()), rewritten)
	}

	return result
}

// rewriteFieldObjectFastJSON rewrites field objects (match, term, range, etc.)
func (p *Proxy) rewriteFieldObjectFastJSON(v *fastjson.Value, baseIndex string, arena *fastjson.Arena) *fastjson.Value {
	obj := v.GetObject()
	if obj == nil {
		return v
	}

	result := arena.NewObject()

	obj.Visit(func(key []byte, v *fastjson.Value) {
		// Prefix the field name with baseIndex
		fieldName := string(key)
		prefixedField := p.prefixField(baseIndex, fieldName)

		// Recursively rewrite the value
		rewritten := p.rewriteQueryValueFastJSON(v, baseIndex, arena)
		result.Set(prefixedField, rewritten)
	})

	return result
}

// rewriteFieldListFastJSON rewrites a list of field names
func (p *Proxy) rewriteFieldListFastJSON(v *fastjson.Value, baseIndex string, arena *fastjson.Arena) *fastjson.Value {
	arr := v.GetArray()
	if arr == nil {
		return v
	}

	result := arena.NewArray()
	for _, item := range arr {
		if item.Type() == fastjson.TypeString {
			fieldName := string(item.GetStringBytes())
			prefixedField := p.prefixField(baseIndex, fieldName)
			result.SetArrayItem(len(result.GetArray()), arena.NewString(prefixedField))
		} else {
			result.SetArrayItem(len(result.GetArray()), item)
		}
	}

	return result
}

// rewriteSourceFilterFastJSON rewrites _source filter (string, array, or object)
func (p *Proxy) rewriteSourceFilterFastJSON(v *fastjson.Value, baseIndex string, arena *fastjson.Arena) *fastjson.Value {
	switch v.Type() {
	case fastjson.TypeArray:
		return p.rewriteFieldListFastJSON(v, baseIndex, arena)

	case fastjson.TypeObject:
		obj := v.GetObject()
		if obj == nil {
			return v
		}

		result := arena.NewObject()

		obj.Visit(func(key []byte, v *fastjson.Value) {
			keyStr := string(key)
			if keyStr == "includes" || keyStr == "excludes" {
				rewritten := p.rewriteSourceFilterFastJSON(v, baseIndex, arena)
				result.Set(keyStr, rewritten)
			} else {
				result.Set(keyStr, v)
			}
		})

		return result

	default:
		return v
	}
}

// rewriteSortValueFastJSON rewrites sort specification
func (p *Proxy) rewriteSortValueFastJSON(v *fastjson.Value, baseIndex string, arena *fastjson.Arena) *fastjson.Value {
	arr := v.GetArray()
	if arr == nil {
		return v
	}

	result := arena.NewArray()
	for _, item := range arr {
		switch item.Type() {
		case fastjson.TypeString:
			// Simple string sort field
			fieldName := string(item.GetStringBytes())
			prefixedField := p.prefixField(baseIndex, fieldName)
			result.SetArrayItem(len(result.GetArray()), arena.NewString(prefixedField))

		case fastjson.TypeObject:
			// Object with field name as key
			obj := item.GetObject()
			if obj == nil {
				result.SetArrayItem(len(result.GetArray()), item)
				continue
			}

			rewritten := arena.NewObject()
			obj.Visit(func(key []byte, v *fastjson.Value) {
				fieldName := string(key)
				prefixedField := p.prefixField(baseIndex, fieldName)
				rewrittenValue := p.rewriteQueryValueFastJSON(v, baseIndex, arena)
				rewritten.Set(prefixedField, rewrittenValue)
			})
			result.SetArrayItem(len(result.GetArray()), rewritten)

		default:
			result.SetArrayItem(len(result.GetArray()), item)
		}
	}

	return result
}

// prefixFieldFastJSON is a helper that wraps the existing prefixField method
func (p *Proxy) prefixFieldFastJSON(baseIndex, field string) string {
	if field == "" {
		return field
	}
	if strings.HasPrefix(field, baseIndex+".") {
		return field
	}
	return baseIndex + "." + field
}
