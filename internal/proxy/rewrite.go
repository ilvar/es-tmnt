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
					// If total lines is 2 (action + one empty line from trailing newline), it's missing source
					// If total lines is 3+ (action + empty source + more), it's empty source line
					if len(lines) <= 2 {
						return nil, errors.New("bulk payload missing source")
					}
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

func (p *Proxy) rewriteMultiSearchBody(body []byte, pathIndex string) ([]byte, error) {
	lines := bytes.Split(body, []byte("\n"))
	var output bytes.Buffer

	expectHeader := true
	var baseIndex string

	for i := 0; i < len(lines); i++ {
		line := bytes.TrimSpace(lines[i])

		if expectHeader {
			if len(line) == 0 {
				// Skip empty lines when looking for the next header.
				continue
			}

			var header map[string]interface{}
			if err := json.Unmarshal(line, &header); err != nil {
				return nil, fmt.Errorf("invalid msearch header: %w", err)
			}

			indexName := pathIndex
			if value, ok := header["index"]; ok {
				indexValue, ok := value.(string)
				if !ok {
					return nil, errors.New("msearch index must be a string")
				}
				indexName = indexValue
			}
			if indexName == "" {
				return nil, errors.New("msearch request missing index")
			}

			var tenantID string
			var err error
			baseIndex, tenantID, err = p.parseIndex(indexName)
			if err != nil {
				return nil, err
			}
			if isSharedMode(p.cfg.Mode) {
				indexName, err = p.renderAlias(baseIndex, tenantID)
			} else {
				indexName, err = p.renderIndex(p.perTenantIdx, baseIndex, tenantID)
			}
			if err != nil {
				return nil, err
			}
			header["index"] = indexName
			encodedHeader, err := json.Marshal(header)
			if err != nil {
				return nil, err
			}
			output.Write(encodedHeader)
			output.WriteByte('\n')

			// Next non-empty line must be the body for this header.
			expectHeader = false
			continue
		}

		// Expecting body line corresponding to the last header.
		if len(line) == 0 {
			// If total lines is 2 (header + one empty line from trailing newline), it's missing body
			// If total lines is 3+ (header + empty body + more), it's empty body line
			if len(lines) <= 2 {
				return nil, errors.New("msearch payload missing body")
			}
			return nil, errors.New("msearch body line empty")
		}

		rewrittenBody, err := p.rewriteQueryBody(line, baseIndex)
		if err != nil {
			return nil, fmt.Errorf("failed to rewrite msearch body at NDJSON line %d: %w", i+1, err)
		}
		output.Write(rewrittenBody)
		output.WriteByte('\n')

		// After a body, the next non-empty line should be a header.
		expectHeader = true
	}

	if !expectHeader {
		// We ended after a header without seeing its body.
		return nil, errors.New("msearch payload missing body")
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
	rewritten := p.rewriteQueryValue(payload, baseIndex)
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
	if rollupIndexValue, ok := payload["rollup_index"]; ok {
		rewritten, err := p.rewriteTargetIndexValue(rollupIndexValue)
		if err != nil {
			return nil, err
		}
		payload["rollup_index"] = rewritten
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
			alias, err := p.renderAlias(baseIndex, tenantID)
			if err == nil && alias != index {
				p.logVerbose("index rewrite (alias): %s -> %s", index, alias)
			}
			return alias, err
		}
		target, err := p.renderIndex(p.sharedIndex, baseIndex, tenantID)
		if err == nil && target != index {
			p.logVerbose("index rewrite (shared): %s -> %s", index, target)
		}
		return target, err
	}
	target, err := p.renderIndex(p.perTenantIdx, baseIndex, tenantID)
	if err == nil && target != index {
		p.logVerbose("index rewrite (per-tenant): %s -> %s", index, target)
	}
	return target, err
}

func (p *Proxy) rewriteQueryValue(value interface{}, baseIndex string) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		output := make(map[string]interface{}, len(typed))
		for key, val := range typed {
			switch key {
			case "match", "term", "range", "prefix", "wildcard", "regexp":
				output[key] = p.rewriteFieldObject(val, baseIndex)
			case "fields":
				output[key] = p.rewriteFieldList(val, baseIndex)
			case "sort":
				output[key] = p.rewriteSortValue(val, baseIndex)
			case "_source":
				output[key] = p.rewriteSourceFilter(val, baseIndex)
			default:
				output[key] = p.rewriteQueryValue(val, baseIndex)
			}
		}
		return output
	case []interface{}:
		items := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			items = append(items, p.rewriteQueryValue(item, baseIndex))
		}
		return items
	default:
		return typed
	}
}

func (p *Proxy) rewriteFieldObject(value interface{}, baseIndex string) interface{} {
	obj, ok := value.(map[string]interface{})
	if !ok {
		return value
	}
	output := make(map[string]interface{}, len(obj))
	for key, val := range obj {
		output[p.prefixField(baseIndex, key)] = p.rewriteQueryValue(val, baseIndex)
	}
	return output
}

func (p *Proxy) rewriteFieldList(value interface{}, baseIndex string) interface{} {
	list, ok := value.([]interface{})
	if !ok {
		return value
	}
	output := make([]interface{}, 0, len(list))
	for _, item := range list {
		s, ok := item.(string)
		if ok {
			output = append(output, p.prefixField(baseIndex, s))
			continue
		}
		output = append(output, item)
	}
	return output
}

func (p *Proxy) rewriteSourceFilter(value interface{}, baseIndex string) interface{} {
	switch typed := value.(type) {
	case []interface{}:
		output := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			s, ok := item.(string)
			if ok {
				output = append(output, p.prefixField(baseIndex, s))
				continue
			}
			output = append(output, item)
		}
		return output
	case map[string]interface{}:
		includes, ok := typed["includes"]
		if ok {
			typed["includes"] = p.rewriteSourceFilter(includes, baseIndex)
		}
		excludes, ok := typed["excludes"]
		if ok {
			typed["excludes"] = p.rewriteSourceFilter(excludes, baseIndex)
		}
		return typed
	default:
		return typed
	}
}

func (p *Proxy) rewriteSortValue(value interface{}, baseIndex string) interface{} {
	list, ok := value.([]interface{})
	if !ok {
		return value
	}
	output := make([]interface{}, 0, len(list))
	for _, item := range list {
		switch typed := item.(type) {
		case string:
			output = append(output, p.prefixField(baseIndex, typed))
		case map[string]interface{}:
			rewritten := make(map[string]interface{}, len(typed))
			for key, val := range typed {
				rewritten[p.prefixField(baseIndex, key)] = p.rewriteQueryValue(val, baseIndex)
			}
			output = append(output, rewritten)
		default:
			output = append(output, item)
		}
	}
	return output
}

func (p *Proxy) prefixField(baseIndex, field string) string {
	if field == "" {
		return field
	}
	if strings.HasPrefix(field, baseIndex+".") {
		return field
	}
	rewritten := baseIndex + "." + field
	if p.cfg.Verbose {
		p.logVerbose("field rewrite: %s -> %s", field, rewritten)
	}
	return rewritten
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
