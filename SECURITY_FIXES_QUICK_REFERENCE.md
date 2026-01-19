# Security Fixes Quick Reference

This document provides immediate, actionable code changes to address the critical security findings from the audit.

## Priority 0 Fixes (Deploy Immediately)

### Fix 1: Block Unimplemented Vulnerable Endpoints

**File**: `internal/proxy/proxy.go`

Add after line 215 (before reject for unsupported endpoint):

```go
// Block known-vulnerable unimplemented endpoints
vulner
ableEndpoints := []string{
    "_scroll", "_clear/scroll", "_pit",
    "_async_search", "_knn_search", "_eql", "_sql",
}
for _, endpoint := range vulnerableEndpoints {
    if len(segments) > 0 && strings.Contains(r.URL.Path, endpoint) {
        p.setResponseMode(w, responseModeHandled)
        p.reject(w, "endpoint not supported - security restriction")
        return
    }
}
```

### Fix 2: Add URL Path Normalization

**File**: `internal/proxy/proxy.go`

Add new function:

```go
import "net/url"

func (p *Proxy) normalizePath(rawPath string) (string, error) {
    // Decode any URL encoding
    decoded, err := url.PathUnescape(rawPath)
    if err != nil {
        return "", fmt.Errorf("invalid URL encoding: %w", err)
    }

    // Clean path to remove ./ and ../ segments
    cleaned := path.Clean(decoded)

    // Reject if path contains suspicious characters
    if strings.Contains(cleaned, "..") || strings.Contains(cleaned, "\n") || strings.Contains(cleaned, "\r") {
        return "", errors.New("invalid path characters detected")
    }

    return cleaned, nil
}
```

Update `ServeHTTP` at line 84:

```go
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // Normalize path FIRST
    normalizedPath, err := p.normalizePath(r.URL.Path)
    if err != nil {
        p.setResponseMode(w, responseModeHandled)
        p.reject(w, "invalid request path")
        return
    }
    r.URL.Path = normalizedPath

    indexName, err := p.requestIndexCandidate(r)
    // ... rest of function
}
```

### Fix 3: Validate Bulk Tenant Consistency

**File**: `internal/proxy/rewrite.go`

Add before line 45:

```go
func (p *Proxy) validateBulkSingleTenant(body []byte, pathIndex string) error {
    lines := bytes.Split(body, []byte("\n"))
    var firstTenant string

    for i := 0; i < len(lines); i++ {
        line := bytes.TrimSpace(lines[i])
        if len(line) == 0 {
            continue
        }

        var action map[string]map[string]interface{}
        if err := json.Unmarshal(line, &action); err != nil {
            continue // Will be caught by main processing
        }

        if len(action) != 1 {
            continue
        }

        for op, meta := range action {
            indexName, err := p.bulkIndexName(meta, pathIndex)
            if err != nil {
                continue
            }

            _, tenantID, err := p.parseIndex(indexName)
            if err != nil {
                continue
            }

            if firstTenant == "" {
                firstTenant = tenantID
            } else if tenantID != firstTenant {
                return fmt.Errorf("bulk request contains multiple tenants: %s and %s", firstTenant, tenantID)
            }

            // Skip source lines for index/create/update
            if op == "index" || op == "create" || op == "update" {
                i++
            }
        }
    }

    return nil
}
```

Update `rewriteBulkBody` at line 45:

```go
func (p *Proxy) rewriteBulkBody(body []byte, pathIndex string) ([]byte, error) {
    // Validate single tenant FIRST
    if err := p.validateBulkSingleTenant(body, pathIndex); err != nil {
        return nil, err
    }

    lines := bytes.Split(body, []byte("\n"))
    // ... rest of function
}
```

---

## Priority 1 Fixes (Deploy Within 1 Week)

### Fix 4: Add Regex Timeout Protection

**File**: `internal/proxy/proxy.go`

Add timeout wrapper for regex matching:

```go
import "context"
import "time"

func (p *Proxy) parseIndexWithTimeout(index string) (string, string, error) {
    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()

    resultChan := make(chan struct {
        baseIndex string
        tenantID  string
        err       error
    }, 1)

    go func() {
        baseIndex, tenantID, err := p.parseIndexUnsafe(index)
        resultChan <- struct {
            baseIndex string
            tenantID  string
            err       error
        }{baseIndex, tenantID, err}
    }()

    select {
    case result := <-resultChan:
        return result.baseIndex, result.tenantID, result.err
    case <-ctx.Done():
        return "", "", errors.New("tenant extraction timed out - possible ReDoS attack")
    }
}

func (p *Proxy) parseIndexUnsafe(index string) (string, string, error) {
    // Current parseIndex logic here
    if p.isBlockedSharedIndex(index) {
        return "", "", fmt.Errorf("direct access to shared indices is not allowed")
    }
    matches := p.cfg.TenantRegex.Compiled.FindStringSubmatch(index)
    // ... rest of current parseIndex
}
```

Replace all calls to `parseIndex` with `parseIndexWithTimeout`.

### Fix 5: Expand Query Rewriting Coverage

**File**: `internal/proxy/rewrite.go`

Update `rewriteQueryValue` at line 386 to add missing query types:

```go
func (p *Proxy) rewriteQueryValue(value interface{}, baseIndex string) interface{} {
    switch typed := value.(type) {
    case map[string]interface{}:
        output := make(map[string]interface{}, len(typed))
        for key, val := range typed {
            switch key {
            // EXISTING
            case "match", "term", "range", "prefix", "wildcard", "regexp":
                output[key] = p.rewriteFieldObject(val, baseIndex)

            // NEW - Add missing query types
            case "match_phrase", "match_phrase_prefix", "fuzzy":
                output[key] = p.rewriteFieldObject(val, baseIndex)

            case "multi_match":
                output[key] = p.rewriteMultiMatch(val, baseIndex)

            case "query_string", "simple_query_string":
                output[key] = p.rewriteQueryString(val, baseIndex)

            case "exists":
                output[key] = p.rewriteExists(val, baseIndex)

            // EXISTING
            case "fields":
                output[key] = p.rewriteFieldList(val, baseIndex)
            case "sort":
                output[key] = p.rewriteSortValue(val, baseIndex)
            case "_source":
                output[key] = p.rewriteSourceFilter(val, baseIndex)

            // NEW - Add aggregations
            case "aggs", "aggregations":
                output[key] = p.rewriteAggregations(val, baseIndex)

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

func (p *Proxy) rewriteMultiMatch(value interface{}, baseIndex string) interface{} {
    obj, ok := value.(map[string]interface{})
    if !ok {
        return value
    }
    if fieldsVal, ok := obj["fields"]; ok {
        obj["fields"] = p.rewriteFieldList(fieldsVal, baseIndex)
    }
    return obj
}

func (p *Proxy) rewriteQueryString(value interface{}, baseIndex string) interface{} {
    obj, ok := value.(map[string]interface{})
    if !ok {
        return value
    }
    if fieldsVal, ok := obj["fields"]; ok {
        obj["fields"] = p.rewriteFieldList(fieldsVal, baseIndex)
    }
    if defaultFieldVal, ok := obj["default_field"]; ok {
        if field, ok := defaultFieldVal.(string); ok {
            obj["default_field"] = p.prefixField(baseIndex, field)
        }
    }
    return obj
}

func (p *Proxy) rewriteExists(value interface{}, baseIndex string) interface{} {
    obj, ok := value.(map[string]interface{})
    if !ok {
        return value
    }
    if fieldVal, ok := obj["field"]; ok {
        if field, ok := fieldVal.(string); ok {
            obj["field"] = p.prefixField(baseIndex, field)
        }
    }
    return obj
}

func (p *Proxy) rewriteAggregations(value interface{}, baseIndex string) interface{} {
    obj, ok := value.(map[string]interface{})
    if !ok {
        return value
    }
    output := make(map[string]interface{}, len(obj))
    for key, val := range obj {
        if aggDef, ok := val.(map[string]interface{}); ok {
            if fieldVal, ok := aggDef["field"]; ok {
                if field, ok := fieldVal.(string); ok {
                    aggDef["field"] = p.prefixField(baseIndex, field)
                }
            }
            output[key] = p.rewriteQueryValue(aggDef, baseIndex)
        } else {
            output[key] = val
        }
    }
    return output
}
```

### Fix 6: Block Transform/Rollup Wildcards

**File**: `internal/proxy/rewrite.go`

Add validation before processing:

```go
func (p *Proxy) validateNoWildcards(value interface{}) error {
    switch typed := value.(type) {
    case string:
        if strings.Contains(typed, "*") || strings.Contains(typed, "?") {
            return errors.New("wildcard patterns not allowed in transform/rollup indices")
        }
    case []interface{}:
        for _, item := range typed {
            if err := p.validateNoWildcards(item); err != nil {
                return err
            }
        }
    }
    return nil
}
```

Update `rewriteTransformBody` at line 271:

```go
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
            // NEW: Validate no wildcards
            if err := p.validateNoWildcards(indexValue); err != nil {
                return nil, err
            }

            rewritten, err := p.rewriteSourceIndexValue(indexValue)
            if err != nil {
                return nil, err
            }
            source["index"] = rewritten
            payload["source"] = source
        }
    }
    // ... rest of function
}
```

---

## Priority 2 Fixes (Deploy Within 1 Month)

### Fix 7: Add Input Validation for Index Names

**File**: `internal/proxy/proxy.go`

Add validation function:

```go
func (p *Proxy) validateIndexName(index string) error {
    // Check length
    if len(index) > 255 {
        return errors.New("index name too long")
    }

    // Check for invalid characters
    for _, char := range index {
        if char < 32 || char == 127 { // Control characters
            return errors.New("index name contains invalid characters")
        }
    }

    // Check for path traversal attempts
    if strings.Contains(index, "..") || strings.Contains(index, "./") {
        return errors.New("index name contains path traversal")
    }

    // Check for common injection patterns
    if strings.ContainsAny(index, "\n\r\t;|&$`") {
        return errors.New("index name contains potentially dangerous characters")
    }

    return nil
}
```

Call this in `parseIndex` at line 1100:

```go
func (p *Proxy) parseIndex(index string) (string, string, error) {
    if err := p.validateIndexName(index); err != nil {
        return "", "", err
    }

    if p.isBlockedSharedIndex(index) {
        return "", "", fmt.Errorf("direct access to shared indices is not allowed")
    }
    // ... rest of function
}
```

### Fix 8: Sanitize Error Messages

**File**: `internal/proxy/proxy.go`

Update the reject function at line 1166:

```go
func (p *Proxy) reject(w http.ResponseWriter, message string) {
    // Sanitize message to remove potentially sensitive information
    sanitized := p.sanitizeErrorMessage(message)

    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusBadRequest)
    _ = json.NewEncoder(w).Encode(map[string]string{
        "error":   "unsupported_request",
        "message": sanitized,
    })
}

func (p *Proxy) sanitizeErrorMessage(message string) string {
    // Remove index names that might belong to other tenants
    tenantPattern := p.cfg.TenantRegex.Compiled
    sanitized := tenantPattern.ReplaceAllString(message, "[REDACTED]")

    // Remove field names
    sanitized = regexp.MustCompile(`field\s+['"]([^'"]+)['"]`).ReplaceAllString(sanitized, "field '[REDACTED]'")

    // Keep message generic
    if strings.Contains(sanitized, "does not match") {
        return "invalid index name format"
    }

    if len(sanitized) > 200 {
        return "request validation failed"
    }

    return sanitized
}
```

---

## Configuration Recommendations

### Harden Default Configuration

**File**: `internal/config/config.go`

Update Default() function at line 38:

```go
func Default() Config {
    return Config{
        Ports: Ports{
            HTTP:  8080,
            Admin: 8081,
        },
        UpstreamURL: "http://localhost:9200",
        Mode:        "shared",
        Verbose:     false, // Never true in production
        TenantRegex: TenantRegex{
            // More restrictive pattern - only alphanumeric tenants
            Pattern: `^(?P<prefix>[a-z0-9]+)-(?P<tenant>[a-z0-9]+)(?P<postfix>[a-z0-9-]*)$`,
        },
        SharedIndex: SharedIndex{
            Name:          "{{.index}}",
            AliasTemplate: "alias-{{.index}}-{{.tenant}}",
            TenantField:   "tenant_id",
            // Default deny patterns for common shared indices
            DenyPatterns: []string{
                "^shared-.*$",
                "^\\..*$", // Hidden indices
                "^_.*$",   // System indices
            },
        },
        IndexPerTenant: IndexPerTenant{
            IndexTemplate: "{{.index}}-{{.tenant}}",
        },
        // Minimal passthroughs by default
        PassthroughPaths: []string{
            "/_cluster/health",
            "/_cat/health",
        },
    }
}
```

---

## Testing Requirements

Create `internal/proxy/security_test.go`:

```go
package proxy

import (
    "testing"
    "es-tmnt/internal/config"
)

func TestURLEncodingBypass(t *testing.T) {
    cfg := config.Default()
    cfg.SharedIndex.DenyPatterns = []string{"^shared-index$"}
    proxy, _ := New(cfg)

    tests := []struct {
        name    string
        index   string
        wantErr bool
    }{
        {"normal blocked", "shared-index", true},
        {"url encoded hyphen", "shared%2Dindex", true},
        {"url encoded slash", "shared-index%2F", true},
        {"double encoded", "shared%252Dindex", true},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            _, _, err := proxy.parseIndex(tt.index)
            if (err != nil) != tt.wantErr {
                t.Errorf("parseIndex(%q) error = %v, wantErr %v", tt.index, err, tt.wantErr)
            }
        })
    }
}

func TestBulkCrossTenantRejection(t *testing.T) {
    cfg := config.Default()
    proxy, _ := New(cfg)

    bulkBody := `{"index":{"_index":"logs-tenant1"}}
{"message":"data1"}
{"index":{"_index":"logs-tenant2"}}
{"message":"data2"}
`

    _, err := proxy.rewriteBulkBody([]byte(bulkBody), "")
    if err == nil {
        t.Fatal("expected error for cross-tenant bulk, got nil")
    }
    if !strings.Contains(err.Error(), "multiple tenants") {
        t.Errorf("expected 'multiple tenants' error, got %v", err)
    }
}

func TestReDoSProtection(t *testing.T) {
    cfg := config.Default()
    // Catastrophic backtracking pattern
    cfg.TenantRegex.Pattern = `^(?P<prefix>[^-]+)-(?P<tenant>(a+)+b)(?P<postfix>.*)$`
    cfg.TenantRegex.Compiled = regexp.MustCompile(cfg.TenantRegex.Pattern)

    proxy, _ := New(cfg)

    // This would cause ReDoS without timeout
    maliciousIndex := "logs-" + strings.Repeat("a", 1000)

    _, _, err := proxy.parseIndexWithTimeout(maliciousIndex)
    if err == nil {
        t.Fatal("expected timeout error for ReDoS pattern")
    }
}
```

---

## Deployment Checklist

Before deploying to production:

- [ ] All P0 fixes implemented and tested
- [ ] Deployed behind authenticated API gateway
- [ ] Deny patterns configured for all shared indices
- [ ] Verbose logging disabled
- [ ] Security tests passing
- [ ] Monitoring and alerting configured
- [ ] Incident response plan documented
- [ ] Regular security audit schedule established

---

*This document should be used in conjunction with the full SECURITY_AUDIT_REPORT.md*
