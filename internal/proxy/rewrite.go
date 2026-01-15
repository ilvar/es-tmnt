package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func (p *Proxy) rewriteDocumentBody(body []byte, baseIndex, tenantID string) ([]byte, error) {
	var doc map[string]interface{}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	if isSharedMode(p.cfg.Mode) {
		doc[p.cfg.SharedIndex.TenantField] = tenantID
		return json.Marshal(doc)
	}
	return json.Marshal(map[string]interface{}{baseIndex: doc})
}

func (p *Proxy) rewriteUpdateBody(body []byte, baseIndex, tenantID string) ([]byte, error) {
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	docValue, ok := payload["doc"]
	if !ok {
		return nil, errors.New("update body requires doc payload")
	}
	docMap, ok := docValue.(map[string]interface{})
	if !ok {
		return nil, errors.New("update doc must be an object")
	}
	if isSharedMode(p.cfg.Mode) {
		docMap[p.cfg.SharedIndex.TenantField] = tenantID
		payload["doc"] = docMap
		return json.Marshal(payload)
	}
	payload["doc"] = map[string]interface{}{baseIndex: docMap}
	return json.Marshal(payload)
}

func (p *Proxy) rewriteBulkBody(body []byte, pathIndex string) ([]byte, error) {
	lines := bytes.Split(body, []byte("\n"))
	var output bytes.Buffer
	for i := 0; i < len(lines); i++ {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		var action map[string]map[string]interface{}
		if err := json.Unmarshal(line, &action); err != nil {
			return nil, fmt.Errorf("invalid bulk action line: %w", err)
		}
		if len(action) != 1 {
			return nil, errors.New("bulk action must contain a single operation")
		}
		for op, meta := range action {
			indexName, err := p.bulkIndexName(meta, pathIndex)
			if err != nil {
				return nil, err
			}
			baseIndex, tenantID, err := p.parseIndex(indexName)
			if err != nil {
				return nil, err
			}
			targetIndex := baseIndex
			if !isSharedMode(p.cfg.Mode) {
				targetIndex, err = p.renderIndex(p.perTenantIdx, baseIndex, tenantID)
				if err != nil {
					return nil, err
				}
			} else {
				targetIndex, err = p.renderIndex(p.sharedIndex, baseIndex, tenantID)
				if err != nil {
					return nil, err
				}
			}
			meta["_index"] = targetIndex
			action[op] = meta
			encoded, err := json.Marshal(action)
			if err != nil {
				return nil, err
			}
			output.Write(encoded)
			output.WriteByte('\n')
			if op == "index" || op == "create" || op == "update" {
				if i+1 >= len(lines) {
					return nil, errors.New("bulk payload missing source")
				}
				i++
				sourceLine := bytes.TrimSpace(lines[i])
				if len(sourceLine) == 0 {
					return nil, errors.New("bulk source line empty")
				}
				if op == "update" {
					rewritten, err := p.rewriteUpdateBody(sourceLine, baseIndex, tenantID)
					if err != nil {
						return nil, err
					}
					output.Write(rewritten)
					output.WriteByte('\n')
					continue
				}
				rewritten, err := p.rewriteDocumentBody(sourceLine, baseIndex, tenantID)
				if err != nil {
					return nil, err
				}
				output.Write(rewritten)
				output.WriteByte('\n')
			}
		}
	}
	return output.Bytes(), nil
}

func (p *Proxy) bulkIndexName(meta map[string]interface{}, pathIndex string) (string, error) {
	if value, ok := meta["_index"]; ok {
		indexName, ok := value.(string)
		if ok && indexName != "" {
			return indexName, nil
		}
		return "", errors.New("bulk _index must be a string")
	}
	if pathIndex != "" {
		return pathIndex, nil
	}
	return "", errors.New("bulk request missing index")
}

func (p *Proxy) rewriteQueryBody(body []byte, baseIndex string) ([]byte, error) {
	if isSharedMode(p.cfg.Mode) {
		return body, nil
	}
	var payload interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	rewritten := rewriteQueryValue(payload, baseIndex)
	return json.Marshal(rewritten)
}

func (p *Proxy) rewriteMappingBody(body []byte, baseIndex string) ([]byte, error) {
	if isSharedMode(p.cfg.Mode) {
		return body, nil
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	if mappingsValue, ok := payload["mappings"]; ok {
		mappings, ok := mappingsValue.(map[string]interface{})
		if !ok {
			return nil, errors.New("mappings must be an object")
		}
		if propsValue, ok := mappings["properties"]; ok {
			props, ok := propsValue.(map[string]interface{})
			if !ok {
				return nil, errors.New("mappings.properties must be an object")
			}
			mappings["properties"] = wrapProperties(props, baseIndex)
			payload["mappings"] = mappings
		}
		return json.Marshal(payload)
	}
	if propsValue, ok := payload["properties"]; ok {
		props, ok := propsValue.(map[string]interface{})
		if !ok {
			return nil, errors.New("properties must be an object")
		}
		payload["properties"] = wrapProperties(props, baseIndex)
	}
	return json.Marshal(payload)
}

func (p *Proxy) rewriteTransformBody(body []byte) ([]byte, error) {
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	if sourceValue, ok := payload["source"]; ok {
		source, ok := sourceValue.(map[string]interface{})
		if !ok {
			return nil, errors.New("transform source must be an object")
		}
		if indexValue, ok := source["index"]; ok {
			rewritten, err := p.rewriteSourceIndexValue(indexValue)
			if err != nil {
				return nil, err
			}
			source["index"] = rewritten
			payload["source"] = source
		}
	}
	if destValue, ok := payload["dest"]; ok {
		dest, ok := destValue.(map[string]interface{})
		if !ok {
			return nil, errors.New("transform dest must be an object")
		}
		if indexValue, ok := dest["index"]; ok {
			rewritten, err := p.rewriteTargetIndexValue(indexValue)
			if err != nil {
				return nil, err
			}
			dest["index"] = rewritten
			payload["dest"] = dest
		}
	}
	return json.Marshal(payload)
}

func (p *Proxy) rewriteRollupBody(body []byte) ([]byte, error) {
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	if patternValue, ok := payload["index_pattern"]; ok {
		rewritten, err := p.rewriteSourceIndexValue(patternValue)
		if err != nil {
			return nil, err
		}
		payload["index_pattern"] = rewritten
	}
	return json.Marshal(payload)
}

func (p *Proxy) rewriteSourceIndexValue(value interface{}) (interface{}, error) {
	return p.rewriteIndexValue(value, true)
}

func (p *Proxy) rewriteTargetIndexValue(value interface{}) (interface{}, error) {
	return p.rewriteIndexValue(value, false)
}

func (p *Proxy) rewriteIndexValue(value interface{}, aliasForShared bool) (interface{}, error) {
	switch typed := value.(type) {
	case string:
		return p.rewriteIndexName(typed, aliasForShared)
	case []interface{}:
		output := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			itemString, ok := item.(string)
			if !ok {
				return nil, errors.New("index list values must be strings")
			}
			rewritten, err := p.rewriteIndexName(itemString, aliasForShared)
			if err != nil {
				return nil, err
			}
			output = append(output, rewritten)
		}
		return output, nil
	default:
		return nil, errors.New("index must be a string or list")
	}
}

func (p *Proxy) rewriteIndexName(index string, aliasForShared bool) (string, error) {
	baseIndex, tenantID, err := p.parseIndex(index)
	if err != nil {
		return "", err
	}
	if isSharedMode(p.cfg.Mode) {
		if aliasForShared {
			return p.renderAlias(baseIndex, tenantID)
		}
		return p.renderIndex(p.sharedIndex, baseIndex, tenantID)
	}
	return p.renderIndex(p.perTenantIdx, baseIndex, tenantID)
}

func rewriteQueryValue(value interface{}, baseIndex string) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		output := make(map[string]interface{}, len(typed))
		for key, val := range typed {
			switch key {
			case "match", "term", "range", "prefix", "wildcard", "regexp":
				output[key] = rewriteFieldObject(val, baseIndex)
			case "fields":
				output[key] = rewriteFieldList(val, baseIndex)
			case "sort":
				output[key] = rewriteSortValue(val, baseIndex)
			case "_source":
				output[key] = rewriteSourceFilter(val, baseIndex)
			default:
				output[key] = rewriteQueryValue(val, baseIndex)
			}
		}
		return output
	case []interface{}:
		items := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			items = append(items, rewriteQueryValue(item, baseIndex))
		}
		return items
	default:
		return typed
	}
}

func rewriteFieldObject(value interface{}, baseIndex string) interface{} {
	obj, ok := value.(map[string]interface{})
	if !ok {
		return value
	}
	output := make(map[string]interface{}, len(obj))
	for key, val := range obj {
		output[prefixField(baseIndex, key)] = rewriteQueryValue(val, baseIndex)
	}
	return output
}

func rewriteFieldList(value interface{}, baseIndex string) interface{} {
	list, ok := value.([]interface{})
	if !ok {
		return value
	}
	output := make([]interface{}, 0, len(list))
	for _, item := range list {
		s, ok := item.(string)
		if ok {
			output = append(output, prefixField(baseIndex, s))
			continue
		}
		output = append(output, item)
	}
	return output
}

func rewriteSourceFilter(value interface{}, baseIndex string) interface{} {
	switch typed := value.(type) {
	case []interface{}:
		output := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			s, ok := item.(string)
			if ok {
				output = append(output, prefixField(baseIndex, s))
				continue
			}
			output = append(output, item)
		}
		return output
	case map[string]interface{}:
		includes, ok := typed["includes"]
		if ok {
			typed["includes"] = rewriteSourceFilter(includes, baseIndex)
		}
		excludes, ok := typed["excludes"]
		if ok {
			typed["excludes"] = rewriteSourceFilter(excludes, baseIndex)
		}
		return typed
	default:
		return typed
	}
}

func rewriteSortValue(value interface{}, baseIndex string) interface{} {
	list, ok := value.([]interface{})
	if !ok {
		return value
	}
	output := make([]interface{}, 0, len(list))
	for _, item := range list {
		switch typed := item.(type) {
		case string:
			output = append(output, prefixField(baseIndex, typed))
		case map[string]interface{}:
			rewritten := make(map[string]interface{}, len(typed))
			for key, val := range typed {
				rewritten[prefixField(baseIndex, key)] = rewriteQueryValue(val, baseIndex)
			}
			output = append(output, rewritten)
		default:
			output = append(output, item)
		}
	}
	return output
}

func prefixField(baseIndex, field string) string {
	if field == "" {
		return field
	}
	if strings.HasPrefix(field, baseIndex+".") {
		return field
	}
	return baseIndex + "." + field
}

func wrapProperties(props map[string]interface{}, baseIndex string) map[string]interface{} {
	if existing, ok := props[baseIndex]; ok {
		if inner, ok := existing.(map[string]interface{}); ok {
			if _, ok := inner["properties"]; ok {
				return props
			}
		}
	}
	return map[string]interface{}{
		baseIndex: map[string]interface{}{
			"properties": props,
		},
	}
}
