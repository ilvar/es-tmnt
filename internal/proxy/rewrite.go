package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"es-tmnt/internal/config"
)

type RequestAction string

const (
	ActionPassthrough RequestAction = "passthrough"
	ActionSearch      RequestAction = "search"
	ActionIndex       RequestAction = "index"
	ActionUpdate      RequestAction = "update"
	ActionMapping     RequestAction = "mapping"
	ActionBulk        RequestAction = "bulk"
	ActionCatIndices  RequestAction = "cat_indices"
)

type RouteResult struct {
	Action  RequestAction
	Index   string
	Tenant  string
	Path    string
	Method  string
	RawPath string
}

type Router struct {
	cfg             config.Config
	tenantExtractor *TenantExtractor
}

func NewRouter(cfg config.Config) (*Router, error) {
	extractor, err := NewTenantExtractor(cfg)
	if err != nil {
		return nil, err
	}
	return &Router{
		cfg:             cfg,
		tenantExtractor: extractor,
	}, nil
}

func (r *Router) Route(req *http.Request) (RouteResult, error) {
	if r.isPassthrough(req.URL.Path) {
		return RouteResult{
			Action:  ActionPassthrough,
			Path:    req.URL.Path,
			Method:  req.Method,
			RawPath: req.URL.Path,
		}, nil
	}

	tenant, rewrittenPath, ok := r.tenantExtractor.Extract(req.URL.Path)
	if !ok {
		return RouteResult{}, fmt.Errorf("tenant not found in path")
	}

	action, index, err := routeForPath(req.Method, rewrittenPath)
	if err != nil {
		return RouteResult{}, err
	}

	return RouteResult{
		Action:  action,
		Index:   index,
		Tenant:  tenant,
		Path:    rewrittenPath,
		Method:  req.Method,
		RawPath: req.URL.Path,
	}, nil
}

func (r *Router) Rewrite(req *http.Request, route RouteResult) error {
	mode := strings.ToLower(strings.TrimSpace(r.cfg.Mode))
	switch mode {
	case "shared":
		return r.rewriteSharedIndex(req, route)
	case "index-per-tenant":
		return r.rewriteIndexPerTenant(req, route)
	default:
		return fmt.Errorf("unsupported mode %q", r.cfg.Mode)
	}
}

func (r *Router) rewriteSharedIndex(req *http.Request, route RouteResult) error {
	switch route.Action {
	case ActionSearch:
		if route.Index == "" {
			if r.cfg.SharedIndex.Name == "" {
				return fmt.Errorf("shared index name is required")
			}
			req.URL.Path = "/" + strings.TrimPrefix(r.cfg.SharedIndex.Name, "/") + "/_search"
			return rewriteRequestBody(req, func(payload interface{}) (interface{}, error) {
				return addTenantFilter(payload, r.cfg.SharedIndex.TenantField, route.Tenant)
			})
		}
		alias := formatTemplate(r.cfg.SharedIndex.AliasTemplate, route.Index, route.Tenant)
		req.URL.Path = replaceIndex(route.Path, alias)
		return nil
	case ActionIndex:
		if r.cfg.SharedIndex.Name == "" {
			return fmt.Errorf("shared index name is required")
		}
		req.URL.Path = replaceIndex(route.Path, r.cfg.SharedIndex.Name)
		return rewriteRequestBody(req, func(payload interface{}) (interface{}, error) {
			body, ok := payload.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("index body must be an object")
			}
			body[r.cfg.SharedIndex.TenantField] = route.Tenant
			return body, nil
		})
	case ActionUpdate:
		req.URL.Path = replaceIndex(route.Path, r.cfg.SharedIndex.Name)
		return nil
	case ActionMapping:
		req.URL.Path = replaceIndex(route.Path, r.cfg.SharedIndex.Name)
		return nil
	case ActionBulk:
		if r.cfg.SharedIndex.Name == "" {
			return fmt.Errorf("shared index name is required")
		}
		if route.Index != "" {
			req.URL.Path = replaceIndex(route.Path, r.cfg.SharedIndex.Name)
		}
		return rewriteBulkRequest(req, route, bulkRewriteConfig{
			tenant:      route.Tenant,
			tenantField: r.cfg.SharedIndex.TenantField,
			targetIndex: r.cfg.SharedIndex.Name,
			mode:        "shared",
		})
	case ActionCatIndices:
		if r.cfg.SharedIndex.Name == "" {
			return fmt.Errorf("shared index name is required")
		}
		req.URL.Path = buildCatIndicesPath(r.cfg.SharedIndex.Name)
		return nil
	default:
		return fmt.Errorf("unsupported action %q", route.Action)
	}
}

func (r *Router) rewriteIndexPerTenant(req *http.Request, route RouteResult) error {
	targetIndex := formatTemplate(r.cfg.IndexPerTenant.IndexTemplate, route.Index, route.Tenant)
	if targetIndex == "" {
		return fmt.Errorf("index-per-tenant template produced empty index")
	}
	if route.Index != "" {
		req.URL.Path = replaceIndex(route.Path, targetIndex)
	} else if route.Action == ActionSearch {
		req.URL.Path = "/" + targetIndex + "/_search"
	} else if route.Action == ActionBulk {
		req.URL.Path = "/" + targetIndex + "/_bulk"
	}

	switch route.Action {
	case ActionSearch:
		if route.Index == "" {
			return nil
		}
		return rewriteRequestBody(req, func(payload interface{}) (interface{}, error) {
			if payload == nil {
				return payload, nil
			}
			return rewriteQueryFields(payload, route.Index), nil
		})
	case ActionMapping:
		if route.Index == "" {
			return nil
		}
		return rewriteRequestBody(req, func(payload interface{}) (interface{}, error) {
			if payload == nil {
				return payload, nil
			}
			return rewriteMappingFields(payload, route.Index), nil
		})
	case ActionIndex:
		return rewriteRequestBody(req, func(payload interface{}) (interface{}, error) {
			if payload == nil {
				return payload, nil
			}
			return wrapSourceUnderIndex(payload, route.Index)
		})
	case ActionUpdate:
		return rewriteRequestBody(req, func(payload interface{}) (interface{}, error) {
			body, ok := payload.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("update body must be an object")
			}
			return rewriteUpdateBody(body, route.Index), nil
		})
	case ActionBulk:
		return rewriteBulkRequest(req, route, bulkRewriteConfig{
			tenant:        route.Tenant,
			indexTemplate: r.cfg.IndexPerTenant.IndexTemplate,
			mode:          "index-per-tenant",
		})
	case ActionCatIndices:
		req.URL.Path = buildCatIndicesPath(targetIndex)
		return nil
	default:
		return fmt.Errorf("unsupported action %q", route.Action)
	}
}

func rewriteRequestBody(req *http.Request, mutate func(interface{}) (interface{}, error)) error {
	if req.Body == nil {
		return nil
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	_ = req.Body.Close()
	if len(raw) == 0 {
		return nil
	}

	var payload interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("parse body: %w", err)
	}

	updated, err := mutate(payload)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(updated)
	if err != nil {
		return fmt.Errorf("encode body: %w", err)
	}

	req.Body = io.NopCloser(bytes.NewReader(encoded))
	req.ContentLength = int64(len(encoded))
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(encoded)))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(encoded)), nil
	}
	return nil
}

func rewriteQueryFields(payload interface{}, index string) interface{} {
	switch value := payload.(type) {
	case map[string]interface{}:
		updated := make(map[string]interface{}, len(value))
		for key, val := range value {
			switch key {
			case "match", "match_phrase", "match_phrase_prefix", "term", "terms", "range", "prefix", "wildcard", "regexp", "fuzzy":
				if fieldMap, ok := val.(map[string]interface{}); ok {
					updated[key] = rewriteFieldKeyMap(fieldMap, index)
					continue
				}
			case "exists":
				if fieldMap, ok := val.(map[string]interface{}); ok {
					updated[key] = rewriteFieldValueMap(fieldMap, index)
					continue
				}
			case "sort":
				updated[key] = rewriteSort(val, index)
				continue
			case "aggs", "aggregations":
				updated[key] = rewriteQueryFields(val, index)
				continue
			}
			updated[key] = rewriteQueryFields(val, index)
		}
		return updated
	case []interface{}:
		for i, item := range value {
			value[i] = rewriteQueryFields(item, index)
		}
		return value
	default:
		return payload
	}
}

func rewriteSort(value interface{}, index string) interface{} {
	switch v := value.(type) {
	case []interface{}:
		for i, item := range v {
			v[i] = rewriteSort(item, index)
		}
		return v
	case map[string]interface{}:
		updated := make(map[string]interface{}, len(v))
		for key, val := range v {
			updated[prefixField(index, key)] = rewriteQueryFields(val, index)
		}
		return updated
	case string:
		return prefixField(index, v)
	default:
		return value
	}
}

func rewriteFieldKeyMap(fields map[string]interface{}, index string) map[string]interface{} {
	updated := make(map[string]interface{}, len(fields))
	for key, val := range fields {
		updated[prefixField(index, key)] = rewriteQueryFields(val, index)
	}
	return updated
}

func rewriteFieldValueMap(fields map[string]interface{}, index string) map[string]interface{} {
	updated := make(map[string]interface{}, len(fields))
	for key, val := range fields {
		switch key {
		case "field":
			if field, ok := val.(string); ok {
				updated[key] = prefixField(index, field)
				continue
			}
		case "fields":
			if list, ok := val.([]interface{}); ok {
				updatedFields := make([]interface{}, 0, len(list))
				for _, field := range list {
					if fieldName, ok := field.(string); ok {
						updatedFields = append(updatedFields, prefixField(index, fieldName))
					} else {
						updatedFields = append(updatedFields, field)
					}
				}
				updated[key] = updatedFields
				continue
			}
		}
		updated[key] = rewriteQueryFields(val, index)
	}
	return updated
}

func rewriteMappingFields(payload interface{}, index string) interface{} {
	return rewriteMappingFieldsDepth(payload, index, 0)
}

func rewriteMappingFieldsDepth(payload interface{}, index string, depth int) interface{} {
	switch value := payload.(type) {
	case map[string]interface{}:
		updated := make(map[string]interface{}, len(value))
		for key, val := range value {
			if key == "properties" {
				if props, ok := val.(map[string]interface{}); ok {
					updatedProps := make(map[string]interface{}, len(props))
					for propKey, propVal := range props {
						targetKey := propKey
						if depth == 0 {
							targetKey = prefixField(index, propKey)
						}
						updatedProps[targetKey] = rewriteMappingFieldsDepth(propVal, index, depth+1)
					}
					updated[key] = updatedProps
					continue
				}
			}
			updated[key] = rewriteMappingFieldsDepth(val, index, depth+1)
		}
		return updated
	case []interface{}:
		for i, item := range value {
			value[i] = rewriteMappingFieldsDepth(item, index, depth+1)
		}
		return value
	default:
		return payload
	}
}

func wrapSourceUnderIndex(payload interface{}, index string) (interface{}, error) {
	body, ok := payload.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("index body must be an object")
	}
	return map[string]interface{}{
		index: body,
	}, nil
}

func rewriteUpdateBody(body map[string]interface{}, index string) map[string]interface{} {
	if doc, ok := body["doc"]; ok {
		if wrapped, err := wrapSourceUnderIndex(doc, index); err == nil {
			body["doc"] = wrapped
		}
	}
	if upsert, ok := body["upsert"]; ok {
		if wrapped, err := wrapSourceUnderIndex(upsert, index); err == nil {
			body["upsert"] = wrapped
		}
	}
	if script, ok := body["script"]; ok {
		body["script"] = rewriteScript(script, index)
	}
	return body
}

func rewriteScript(script interface{}, index string) interface{} {
	scriptMap, ok := script.(map[string]interface{})
	if !ok {
		return script
	}
	if source, ok := scriptMap["source"].(string); ok {
		scriptMap["source"] = strings.ReplaceAll(source, "ctx._source.", "ctx._source."+index+".")
	}
	return scriptMap
}

func prefixField(index, field string) string {
	if index == "" || field == "" || strings.HasPrefix(field, index+".") {
		return field
	}
	return index + "." + field
}

func replaceIndex(path string, index string) string {
	segments := splitPath(path)
	if len(segments) == 0 {
		return path
	}
	segments[0] = index
	return "/" + strings.Join(segments, "/")
}

func splitPath(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return []string{}
	}
	return strings.Split(trimmed, "/")
}

func formatTemplate(template, index, tenant string) string {
	if template == "" {
		return ""
	}
	result := strings.ReplaceAll(template, "{index}", index)
	return strings.ReplaceAll(result, "{tenant}", tenant)
}

func (r *Router) isPassthrough(path string) bool {
	for _, prefix := range r.cfg.PassthroughPaths {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func addTenantFilter(payload interface{}, tenantField, tenant string) (interface{}, error) {
	filterClause := map[string]interface{}{
		"term": map[string]interface{}{
			tenantField: tenant,
		},
	}
	if payload == nil {
		return map[string]interface{}{
			"query": map[string]interface{}{
				"bool": map[string]interface{}{
					"filter": []interface{}{filterClause},
				},
			},
		}, nil
	}
	body, ok := payload.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("search body must be an object")
	}
	query, ok := body["query"]
	if !ok {
		body["query"] = map[string]interface{}{
			"bool": map[string]interface{}{
				"filter": []interface{}{filterClause},
			},
		}
		return body, nil
	}
	body["query"] = addTenantFilterToQuery(query, filterClause)
	return body, nil
}

func addTenantFilterToQuery(query interface{}, filterClause map[string]interface{}) interface{} {
	queryMap, ok := query.(map[string]interface{})
	if !ok {
		return map[string]interface{}{
			"bool": map[string]interface{}{
				"must":   query,
				"filter": []interface{}{filterClause},
			},
		}
	}
	if boolQuery, ok := queryMap["bool"].(map[string]interface{}); ok {
		boolQuery["filter"] = appendFilterClause(boolQuery["filter"], filterClause)
		queryMap["bool"] = boolQuery
		return queryMap
	}
	return map[string]interface{}{
		"bool": map[string]interface{}{
			"must":   queryMap,
			"filter": []interface{}{filterClause},
		},
	}
}

func appendFilterClause(existing interface{}, filterClause map[string]interface{}) []interface{} {
	switch value := existing.(type) {
	case nil:
		return []interface{}{filterClause}
	case []interface{}:
		return append(value, filterClause)
	default:
		return []interface{}{value, filterClause}
	}
}

type bulkRewriteConfig struct {
	tenant        string
	tenantField   string
	targetIndex   string
	indexTemplate string
	mode          string
}

func rewriteBulkRequest(req *http.Request, route RouteResult, cfg bulkRewriteConfig) error {
	if req.Body == nil {
		return nil
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return fmt.Errorf("read bulk body: %w", err)
	}
	_ = req.Body.Close()
	if len(raw) == 0 {
		return nil
	}
	hadTrailingNewline := raw[len(raw)-1] == '\n'
	lines := bytes.Split(raw, []byte("\n"))

	var buf bytes.Buffer
	for i := 0; i < len(lines); i++ {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			if i == len(lines)-1 {
				continue
			}
			return fmt.Errorf("bulk request contains empty line at %d", i)
		}
		action, meta, err := parseBulkAction(line)
		if err != nil {
			return err
		}
		originalIndex := route.Index
		if metaIndex, ok := meta["_index"].(string); ok && metaIndex != "" {
			originalIndex = metaIndex
		}
		if originalIndex == "" {
			return fmt.Errorf("bulk action missing index")
		}
		switch cfg.mode {
		case "shared":
			meta["_index"] = cfg.targetIndex
		case "index-per-tenant":
			targetIndex := formatTemplate(cfg.indexTemplate, originalIndex, route.Tenant)
			if targetIndex == "" {
				return fmt.Errorf("index-per-tenant template produced empty index")
			}
			meta["_index"] = targetIndex
		default:
			return fmt.Errorf("unsupported bulk mode %q", cfg.mode)
		}
		actionLine, err := json.Marshal(map[string]interface{}{action: meta})
		if err != nil {
			return fmt.Errorf("encode bulk action: %w", err)
		}
		buf.Write(actionLine)
		buf.WriteByte('\n')

		if action == "index" || action == "create" || action == "update" {
			if i+1 >= len(lines) {
				return fmt.Errorf("bulk action %q missing source line", action)
			}
			i++
			sourceLine := bytes.TrimSpace(lines[i])
			if len(sourceLine) == 0 {
				return fmt.Errorf("bulk action %q has empty source line", action)
			}
			var sourcePayload interface{}
			if err := json.Unmarshal(sourceLine, &sourcePayload); err != nil {
				return fmt.Errorf("parse bulk source: %w", err)
			}
			rewritten, err := rewriteBulkSource(action, sourcePayload, originalIndex, cfg)
			if err != nil {
				return err
			}
			encoded, err := json.Marshal(rewritten)
			if err != nil {
				return fmt.Errorf("encode bulk source: %w", err)
			}
			buf.Write(encoded)
			buf.WriteByte('\n')
		}
	}

	updated := buf.Bytes()
	if !hadTrailingNewline && len(updated) > 0 && updated[len(updated)-1] == '\n' {
		updated = updated[:len(updated)-1]
	}

	req.Body = io.NopCloser(bytes.NewReader(updated))
	req.ContentLength = int64(len(updated))
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(updated)))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(updated)), nil
	}
	return nil
}

func parseBulkAction(line []byte) (string, map[string]interface{}, error) {
	var payload map[string]map[string]interface{}
	if err := json.Unmarshal(line, &payload); err != nil {
		return "", nil, fmt.Errorf("parse bulk action: %w", err)
	}
	if len(payload) != 1 {
		return "", nil, fmt.Errorf("bulk action must contain single action")
	}
	for action, meta := range payload {
		return action, meta, nil
	}
	return "", nil, fmt.Errorf("bulk action missing")
}

func rewriteBulkSource(action string, payload interface{}, originalIndex string, cfg bulkRewriteConfig) (interface{}, error) {
	switch cfg.mode {
	case "shared":
		switch action {
		case "index", "create":
			body, ok := payload.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("bulk index body must be an object")
			}
			body[cfg.tenantField] = cfg.tenant
			return body, nil
		case "update":
			body, ok := payload.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("bulk update body must be an object")
			}
			return rewriteSharedIndexUpdateBody(body, cfg.tenantField, cfg.tenant), nil
		}
	case "index-per-tenant":
		switch action {
		case "index", "create":
			return wrapSourceUnderIndex(payload, originalIndex)
		case "update":
			body, ok := payload.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("bulk update body must be an object")
			}
			return rewriteUpdateBody(body, originalIndex), nil
		}
	}
	return payload, nil
}

func rewriteSharedIndexUpdateBody(body map[string]interface{}, tenantField, tenant string) map[string]interface{} {
	if doc, ok := body["doc"].(map[string]interface{}); ok {
		doc[tenantField] = tenant
		body["doc"] = doc
	}
	if upsert, ok := body["upsert"].(map[string]interface{}); ok {
		upsert[tenantField] = tenant
		body["upsert"] = upsert
	}
	return body
}

func buildCatIndicesPath(index string) string {
	if strings.TrimSpace(index) == "" {
		return "/_cat/indices"
	}
	return "/_cat/indices/" + strings.TrimPrefix(index, "/")
}
