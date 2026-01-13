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

	segments := splitPath(rewrittenPath)
	if len(segments) < 2 {
		return RouteResult{}, fmt.Errorf("unsupported path %q", rewrittenPath)
	}
	index := segments[0]
	if index == "" || strings.Contains(index, ",") {
		return RouteResult{}, fmt.Errorf("unsupported index %q", index)
	}

	action, err := actionForPath(req.Method, segments)
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
	case "shared-index":
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
		alias := formatTemplate(r.cfg.SharedIndex.AliasFormat, route.Index, route.Tenant)
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
	default:
		return fmt.Errorf("unsupported action %q", route.Action)
	}
}

func (r *Router) rewriteIndexPerTenant(req *http.Request, route RouteResult) error {
	targetIndex := formatTemplate(r.cfg.IndexPerTenant.IndexFormat, route.Index, route.Tenant)
	if targetIndex == "" {
		return fmt.Errorf("index-per-tenant format produced empty index")
	}
	req.URL.Path = replaceIndex(route.Path, targetIndex)

	switch route.Action {
	case ActionSearch:
		return rewriteRequestBody(req, func(payload interface{}) (interface{}, error) {
			if payload == nil {
				return payload, nil
			}
			return rewriteQueryFields(payload, route.Index), nil
		})
	case ActionMapping:
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
	if field == "" || strings.HasPrefix(field, index+".") {
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
	for _, prefix := range r.cfg.Passthrough {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func actionForPath(method string, segments []string) (RequestAction, error) {
	action := segments[1]
	switch action {
	case "_search", "_msearch", "_count":
		if method != http.MethodGet && method != http.MethodPost {
			return "", fmt.Errorf("unsupported method %q for search", method)
		}
		return ActionSearch, nil
	case "_doc", "_create":
		if method != http.MethodPost && method != http.MethodPut {
			return "", fmt.Errorf("unsupported method %q for indexing", method)
		}
		return ActionIndex, nil
	case "_update":
		if method != http.MethodPost {
			return "", fmt.Errorf("unsupported method %q for update", method)
		}
		return ActionUpdate, nil
	case "_mapping":
		if method != http.MethodPut && method != http.MethodPost {
			return "", fmt.Errorf("unsupported method %q for mapping", method)
		}
		return ActionMapping, nil
	default:
		return "", fmt.Errorf("unsupported action %q", action)
	}
}
