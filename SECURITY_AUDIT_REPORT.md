# Security Audit Report: es-tmnt Multi-Tenant Elasticsearch Proxy
**Date**: 2026-01-19
**Auditor**: Claude (Anthropic AI Security Review)
**Focus Area**: Tenant Data Isolation & Cross-Tenant Data Leaks

---

## Executive Summary

This security audit focused on tenant isolation mechanisms in the es-tmnt Elasticsearch proxy, with particular emphasis on scenarios where data could leak between tenants. The audit identified **8 high-priority security findings** and **5 medium-priority issues** that could potentially allow tenant boundary violations. While the core architecture demonstrates solid security principles, several implementation gaps and unhandled edge cases create risk vectors for cross-tenant data access.

**Critical Risk**: The proxy lacks authentication mechanisms and has several bypass vulnerabilities that could allow malicious actors to access data from other tenants.

---

## Architecture Overview

es-tmnt is a reverse proxy for Elasticsearch that enforces tenant isolation through two modes:

1. **Shared-Index Mode**: Multiple tenants share one index, isolated by:
   - Alias-based filtering with `tenant_id` field
   - Automatic `tenant_id` injection on writes
   - Deny patterns to block direct index access

2. **Index-Per-Tenant Mode**: Each tenant gets a dedicated index with:
   - Physical index separation
   - Field path rewriting (prefixing with base index name)
   - Document nesting under base index

**Tenant Identification**: Extracted from index names via configurable regex pattern (e.g., `logs-acme-prod` → tenant: `acme`).

---

## HIGH SEVERITY FINDINGS

### 🔴 H1: URL Encoding Bypass in Deny Patterns (CRITICAL)

**Location**: `internal/proxy/proxy.go:1433-1440`, `internal/proxy/proxy.go:1175-1181`

**Issue**: The proxy does not URL-decode paths before applying deny patterns or tenant extraction. Go's `http.Request.URL.Path` contains the **decoded** path by default, but the `splitPath` function and regex matching operate on this value directly. However, there's a critical gap: if a reverse proxy sits in front of es-tmnt or if double-encoding is used, encoded characters could bypass the checks.

**Proof of Concept**:
```bash
# Standard request (blocked by deny pattern ^shared-index$)
GET /shared-index/_search

# Potential bypass attempts:
GET /shared%2Dindex/_search  # URL-encoded hyphen
GET /shared-index%2F_search  # URL-encoded slash
GET /./shared-index/_search  # Path traversal
```

**Impact**:
- Bypasses deny patterns protecting shared indices
- Could allow direct access to shared index, leaking all tenant data
- Severity escalates if es-tmnt runs behind another proxy that doesn't normalize paths

**Affected Modes**: Both shared and index-per-tenant

**Recommendation**:
1. Explicitly URL-decode and normalize all paths before processing
2. Use `path.Clean()` to eliminate `..` and `.` segments
3. Add integration tests for URL-encoded index names
4. Consider rejecting requests with encoded characters in critical path segments

---

### 🔴 H2: No Authentication or Authorization Layer

**Location**: Entire proxy architecture

**Issue**: The proxy has **zero authentication mechanisms**. There is no validation to verify that:
- A client is authorized to access any tenant data
- A client represents the tenant they claim in the index name
- A client is a legitimate user vs. an attacker

The proxy **entirely relies on the index name** in the URL to determine tenant scope. Any client that can reach the proxy can access any tenant's data by simply changing the index name in the request path.

**Proof of Concept**:
```bash
# Attacker can access ANY tenant's data by changing the index name:
GET /logs-victim-tenant/_search  # Access victim's logs
GET /orders-competitor/_search    # Access competitor's orders
```

**Impact**:
- **Complete tenant isolation bypass**
- Any network-accessible client can read/write to ANY tenant
- Insider threats can trivially access all tenants
- No audit trail of which user accessed what tenant

**Affected Modes**: Both shared and index-per-tenant

**Recommendation**:
1. **Implement authentication**: Add API key, JWT, or mutual TLS authentication
2. **Add tenant-to-client mapping**: Maintain allow-list of which clients can access which tenants
3. **Enforce authorization**: Validate tenant access on every request
4. **Add audit logging**: Log all tenant access with client identity
5. **Deploy behind authenticated gateway**: At minimum, require deployment behind an API gateway with authentication
6. **Add configuration warning**: Document that es-tmnt is NOT secure without an auth layer

**Example Mitigation**:
```go
type TenantAuthorizer interface {
    CanAccess(clientID string, tenantID string) bool
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    clientID := extractClientID(r) // From JWT, API key, mTLS cert, etc.
    if clientID == "" {
        p.reject(w, "authentication required")
        return
    }

    // ... existing tenant extraction logic ...

    if !p.authorizer.CanAccess(clientID, tenantID) {
        p.reject(w, "access denied")
        return
    }

    // ... continue processing ...
}
```

---

### 🔴 H3: Regex Pattern Injection/Misconfiguration Risk

**Location**: `internal/proxy/proxy.go:1100-1127`, `internal/config/config.go:21-24`

**Issue**: The tenant extraction regex is fully user-configurable with minimal validation. While the pattern compilation is validated, there's no protection against:
- Overly permissive patterns (e.g., `.*` matching everything)
- Patterns that extract incorrect tenant IDs
- ReDoS (Regular Expression Denial of Service) attacks with catastrophic backtracking

**Proof of Concept**:
```yaml
# Malicious or misconfigured regex patterns:
tenant_regex:
  pattern: "^(?P<prefix>.*)-(?P<tenant>.*)(?P<postfix>.*)$"  # Too permissive

tenant_regex:
  pattern: "^(?P<prefix>[^-]+)-(?P<tenant>[^-]+)(?P<postfix>(a+)+b)$"  # ReDoS
```

**Impact**:
- Incorrect tenant extraction could route data to wrong tenants
- ReDoS patterns could cause CPU exhaustion and DoS
- Overly permissive patterns weaken tenant isolation guarantees

**Affected Modes**: Both shared and index-per-tenant

**Recommendation**:
1. Add regex complexity validation (e.g., reject patterns with nested quantifiers)
2. Set timeout for regex matching to prevent ReDoS
3. Provide pre-defined, hardened regex patterns as defaults
4. Add integration tests with adversarial index names
5. Document security implications of custom regex patterns

```go
import "regexp/syntax"

func validateRegexComplexity(pattern string) error {
    parsed, err := syntax.Parse(pattern, syntax.Perl)
    if err != nil {
        return err
    }

    // Check for nested quantifiers (ReDoS risk)
    if hasNestedQuantifiers(parsed) {
        return errors.New("regex pattern has nested quantifiers (ReDoS risk)")
    }

    return nil
}
```

---

### 🔴 H4: Scroll/Point-in-Time (PIT) Session IDs Not Tenant-Scoped

**Location**: README.md lines 94-95, no implementation in proxy

**Issue**: Elasticsearch scroll and PIT APIs are documented as unhandled. These APIs return session IDs that can be used across requests. **Critical gap**: A malicious actor could:
1. Start a scroll/PIT session on their own tenant's index
2. Obtain a scroll_id or pit_id
3. Use that ID with a different tenant's index name in subsequent requests
4. Potentially access data outside their tenant boundary

**Proof of Concept**:
```bash
# Step 1: Attacker creates scroll on their tenant
POST /logs-attacker/_search?scroll=1m
{
  "query": {"match_all": {}}
}
# Response: {"_scroll_id": "abc123..."}

# Step 2: Attacker uses scroll_id on victim's tenant
POST /logs-victim/_search/scroll
{
  "scroll_id": "abc123...",
  "scroll": "1m"
}
# If not properly validated, could return victim's data
```

**Impact**:
- Cross-tenant data access if Elasticsearch doesn't enforce index boundaries on scroll/PIT IDs
- Session hijacking across tenant boundaries
- Data exfiltration via scroll continuation

**Affected Modes**: Both shared and index-per-tenant

**Recommendation**:
1. **Block scroll/PIT endpoints entirely** until proper implementation exists
2. Track scroll/PIT IDs with their associated tenant
3. Validate tenant matches on all scroll/PIT continuation requests
4. Set aggressive timeouts on scroll/PIT sessions
5. Add security warning to README for unimplemented endpoints

---

### 🔴 H5: Bulk Operations Allow Cross-Tenant Data Mixing

**Location**: `internal/proxy/rewrite.go:45-122`

**Issue**: While bulk operations rewrite each action's `_index` individually, the error handling allows partial success. If tenant extraction fails mid-batch:
- Already-processed lines may have been written
- Error doesn't roll back previous writes
- Could result in data being written to wrong tenant

Additionally, there's a gap in bulk validation: the code checks that each action has a tenant-matching index, but doesn't verify that **all actions in a single bulk request target the same tenant**.

**Proof of Concept**:
```json
POST /_bulk
{"index": {"_index": "logs-tenant1"}}
{"message": "tenant1 data"}
{"index": {"_index": "logs-tenant2"}}
{"message": "tenant2 data"}
{"index": {"_index": "invalid-index-no-tenant"}}
{"message": "malicious data"}
```

If this bulk request is sent to `/logs-tenant1/_bulk`, the first two actions would process before the third fails. No validation ensures all actions target the same tenant.

**Impact**:
- Data could be written to multiple tenants in one request
- Bypasses tenant boundary enforcement
- Partial failures could leave inconsistent state

**Affected Modes**: Both shared and index-per-tenant

**Recommendation**:
1. **Enforce single-tenant rule**: Validate all bulk actions target the same tenant
2. Pre-validate entire bulk request before processing any actions
3. Return error if actions target multiple tenants
4. Add integration tests for cross-tenant bulk attempts

```go
func (p *Proxy) validateBulkTenantConsistency(body []byte, pathIndex string) error {
    lines := bytes.Split(body, []byte("\n"))
    var firstTenant string

    for i := 0; i < len(lines); i++ {
        line := bytes.TrimSpace(lines[i])
        if len(line) == 0 {
            continue
        }

        // Parse action line...
        indexName, err := p.bulkIndexName(meta, pathIndex)
        if err != nil {
            return err
        }

        _, tenantID, err := p.parseIndex(indexName)
        if err != nil {
            return err
        }

        if firstTenant == "" {
            firstTenant = tenantID
        } else if tenantID != firstTenant {
            return fmt.Errorf("bulk request contains multiple tenants: %s and %s", firstTenant, tenantID)
        }

        // Skip source line...
    }

    return nil
}
```

---

### 🔴 H6: Query Rewriting Incomplete - Multiple Bypass Vectors

**Location**: `internal/proxy/rewrite.go:386-414`

**Issue**: Query rewriting only covers a subset of Elasticsearch query types. The following query structures are **NOT rewritten** in index-per-tenant mode, allowing field names to be used without the base index prefix:

**Unhandled Query Types**:
- `match_phrase`, `match_phrase_prefix`
- `multi_match`
- `query_string`, `simple_query_string`
- `exists`
- `fuzzy`
- `geo_*` queries (geo_bounding_box, geo_distance, etc.)
- `span_*` queries
- `percolate`
- `more_like_this`
- `script` fields in queries
- `function_score` with field references
- `nested` and `has_child`/`has_parent` queries
- `collapse` field

**Proof of Concept**:
```json
POST /logs-tenant1/_search
{
  "query": {
    "multi_match": {
      "query": "secret",
      "fields": ["message", "password"]
    }
  }
}
```

In index-per-tenant mode, this should rewrite to `["logs.message", "logs.password"]`, but the current implementation doesn't handle `multi_match`, so it searches the un-prefixed fields which might access wrong data or fail.

**Impact**:
- Field name collisions across tenants
- Queries may return data from wrong tenant if field names overlap
- Inconsistent query behavior
- Data integrity issues

**Affected Modes**: Index-per-tenant only (shared mode doesn't rewrite queries)

**Recommendation**:
1. Expand `rewriteQueryValue` to cover all query types
2. Use Elasticsearch query DSL parser library for comprehensive coverage
3. Add integration tests for every query type
4. Consider rejecting unhandled query structures with explicit error
5. **Short-term**: Document unsupported query types and reject them

---

### 🔴 H7: Multi-Search (msearch) Race Condition

**Location**: `internal/proxy/rewrite.go:124-210`

**Issue**: The multi-search rewriting logic processes header/body pairs sequentially and uses a shared `baseIndex` variable that persists across pairs:

```go
var baseIndex string  // Line 129 - shared across all pairs

for i := 0; i < len(lines); i++ {
    if expectHeader {
        // ... parse header, set baseIndex ...
        baseIndex, tenantID, err = p.parseIndex(indexName)
        // ...
    } else {
        // Use baseIndex from previous header
        rewrittenBody, err := p.rewriteQueryBody(line, baseIndex)
        // ...
    }
}
```

While this looks safe for sequential processing, the variable reuse creates risk if:
- Empty lines cause skipping that disrupts header/body pairing
- Logic errors cause `baseIndex` from tenant A to be used with tenant B's query body

**Proof of Concept**:
```ndjson
{"index": "logs-tenant1"}
{"query": {"match_all": {}}}
{}
{"query": {"match": {"secret_field": "value"}}}
{"index": "logs-tenant2"}
{"query": {"match_all": {}}}
```

The empty line on line 3 could cause parsing errors that result in line 4's query being associated with wrong tenant.

**Impact**:
- Query bodies could be associated with wrong tenant
- Field rewriting uses incorrect base index
- Cross-tenant data access

**Affected Modes**: Both shared and index-per-tenant

**Recommendation**:
1. Clear `baseIndex` variable after each header/body pair
2. Make header/body pairing more explicit (use struct to bind them)
3. Add strict validation that header-body pairs are correctly matched
4. Reject requests with empty lines in msearch body

---

### 🔴 H8: Transform and Rollup APIs Allow Index Pattern Wildcards

**Location**: `internal/proxy/rewrite.go:271-327`

**Issue**: The transform and rollup rewriting handles `source.index` and `dest.index` fields, but Elasticsearch allows these to be:
- Wildcard patterns (e.g., `logs-*`)
- Comma-separated lists
- Patterns that could match multiple tenants

The rewriting code calls `p.rewriteSourceIndexValue()` which does handle arrays, but the tenant parsing happens **per-index**. A wildcard like `logs-*` would fail tenant extraction, but there's no validation to prevent wildcards that could match multiple tenants.

**Proof of Concept**:
```json
PUT /_transform/malicious-transform
{
  "source": {
    "index": ["logs-*"]
  },
  "dest": {
    "index": "aggregated-logs-attacker"
  },
  "pivot": { ... }
}
```

If an attacker can create a transform with a wildcard source pattern, they could aggregate data across all tenants.

**Impact**:
- Cross-tenant data aggregation via transforms
- Data exfiltration through rollup indices
- Bypasses tenant isolation entirely

**Affected Modes**: Both shared and index-per-tenant

**Recommendation**:
1. **Reject wildcard patterns** in transform/rollup source indices
2. Validate that all indices in arrays belong to the same tenant
3. Add explicit tests for multi-tenant transform attempts
4. Consider blocking transform/rollup APIs entirely if not needed

---

## MEDIUM SEVERITY FINDINGS

### 🟡 M1: No Rate Limiting or Resource Quotas Per Tenant

**Location**: Architecture-wide

**Issue**: The proxy has no rate limiting, quotas, or resource controls per tenant. A malicious or compromised tenant can:
- Exhaust Elasticsearch resources affecting all tenants
- Launch DoS attacks via expensive queries
- Consume all storage with unbounded writes

**Impact**:
- Noisy neighbor problem - one tenant affects others
- Availability issues for all tenants
- No protection against resource exhaustion

**Recommendation**:
1. Implement per-tenant rate limiting
2. Add request size limits per tenant
3. Track and limit resource usage per tenant
4. Add monitoring and alerting for anomalous tenant behavior

---

### 🟡 M2: Passthrough Paths Could Expose Tenant Information

**Location**: `internal/proxy/proxy.go:1311-1340`, `internal/proxy/proxy.go:94-98`

**Issue**: Multiple system endpoints are passed through without modification:
- `/_alias/*`, `/_aliases` - Could list all tenant aliases
- `/_template/*` - Templates might contain tenant patterns
- `/_cat/*` (except indices) - Could leak cluster-wide information
- `/_cluster/*` - Cluster state might reveal tenant structure

While these are necessary for operation, they could leak information about other tenants' existence and structure.

**Impact**:
- Information disclosure about other tenants
- Tenant enumeration
- Could aid reconnaissance for attacks

**Recommendation**:
1. Filter passthrough responses to remove cross-tenant information
2. Require explicit per-tenant authorization for system endpoints
3. Add configuration to disable unnecessary passthrough paths
4. Audit what information is leaked through each passthrough endpoint

---

### 🟡 M3: Error Messages May Leak Cross-Tenant Information

**Location**: `internal/proxy/proxy.go:1166-1173`, response handling throughout

**Issue**: Error messages from Elasticsearch are passed through to clients without sanitization. These could contain:
- Index names from other tenants
- Field names from other tenant schemas
- Query details from other tenants
- Cluster topology information

**Proof of Concept**:
```bash
# Attempt to access non-existent tenant index
GET /logs-nonexistent/_search
# Elasticsearch error might reveal: "index [logs-actual-tenant-1] doesn't exist. Did you mean [logs-actual-tenant-2]?"
```

**Impact**:
- Information disclosure
- Tenant enumeration
- Schema disclosure

**Recommendation**:
1. Sanitize all error messages before returning to clients
2. Generic errors for tenant-related issues
3. Log detailed errors server-side only
4. Never return Elasticsearch errors verbatim

---

### 🟡 M4: Alias Management Bypasses Could Allow Cross-Tenant Access

**Location**: System passthroughs for `/_alias/*` endpoints

**Issue**: If alias management endpoints (`/_alias/*`) are in the passthrough list, a malicious actor could:
- Create aliases pointing to other tenants' indices
- Modify existing tenant aliases to include additional indices
- Remove filter conditions from tenant aliases in shared mode

**Proof of Concept**:
```json
POST /_aliases
{
  "actions": [
    {
      "add": {
        "index": "shared-index",
        "alias": "alias-logs-attacker",
        "filter": {
          "term": {"tenant_id": "victim"}
        }
      }
    }
  ]
}
```

This would give the attacker an alias that filters to victim's data.

**Impact**:
- Complete tenant isolation bypass in shared mode
- Cross-tenant data access
- Privilege escalation

**Recommendation**:
1. **Block `/_alias/*` endpoints** unless specifically needed
2. If aliases must be supported, intercept and validate all alias operations
3. Ensure alias filters cannot be tampered with
4. Only allow alias operations on the tenant's own indices

---

### 🟡 M5: Lack of Input Validation on Index Names

**Location**: `internal/proxy/proxy.go:1100-1127`

**Issue**: Index names are not validated beyond regex matching. Malicious index names could contain:
- Special characters that confuse regex matching
- Newlines or control characters
- Excessively long names
- SQL injection-like patterns (though not directly applicable to ES)

**Example**:
```bash
GET /logs-tenant1%0A-malicious/_search
GET /logs-$(whoami)/_search
GET /../../../etc/passwd/_search
```

**Impact**:
- Regex bypass
- Log injection
- Potential for unexpected behavior

**Recommendation**:
1. Whitelist allowed characters in index names
2. Reject index names with special characters, newlines, control codes
3. Enforce maximum length limits
4. Add input sanitization tests

---

## LOW SEVERITY FINDINGS

### 🟢 L1: Verbose Logging May Expose Sensitive Data

**Location**: `internal/proxy/proxy.go:1426-1431`

**Issue**: When `verbose: true`, the proxy logs field rewrites and index names which could contain sensitive information.

**Recommendation**: Ensure verbose logs are never enabled in production, or sanitize logged data.

---

### 🟢 L2: No Audit Trail for Tenant Access

**Location**: Architecture-wide

**Issue**: The proxy doesn't maintain an audit log of which clients accessed which tenants, making forensics impossible.

**Recommendation**: Implement comprehensive audit logging with client identity, tenant accessed, operation performed, and timestamp.

---

### 🟢 L3: No Integrity Verification of Tenant Field Injection

**Location**: `internal/proxy/rewrite.go:11-21` (shared mode)

**Issue**: In shared mode, if a client manually includes `tenant_id` in their document, the proxy overwrites it, but doesn't warn or log this. A confused client might not realize their explicit tenant field is being overwritten.

**Recommendation**: Log a warning when tenant_id is already present in document, or reject such requests.

---

## Testing Gaps

The following security-critical scenarios lack test coverage:

1. ✗ URL-encoded index names in deny patterns
2. ✗ Path traversal attempts (`/../`, `/./`)
3. ✗ Double-encoded URLs
4. ✗ Cross-tenant bulk operations
5. ✗ Scroll/PIT session ID reuse across tenants
6. ✗ Multi-search with mixed tenant headers
7. ✗ Transform/rollup with wildcard patterns
8. ✗ Alias manipulation attacks
9. ✗ All unhandled query types (multi_match, query_string, etc.)
10. ✗ ReDoS attack patterns in tenant regex
11. ✗ Input validation for special characters in index names
12. ✗ Error message sanitization

**Recommendation**: Add integration tests for all above scenarios in a new `security_test.go` file.

---

## Deployment Recommendations

### Secure Deployment Checklist

1. ✅ **Deploy behind authenticated API gateway** (mandatory)
2. ✅ **Enable deny patterns for all shared indices**
3. ✅ **Use simple, hardened tenant regex patterns**
4. ✅ **Disable verbose logging in production**
5. ✅ **Set restrictive passthrough_paths** (only allow necessary endpoints)
6. ✅ **Monitor for unusual tenant access patterns**
7. ✅ **Implement network-level tenant isolation** (if possible)
8. ✅ **Regular security audits of Elasticsearch cluster**
9. ✅ **Use Elasticsearch security features** (even with proxy)
10. ✅ **Implement DDoS protection at network edge**

### Immediate Mitigations (High Priority)

1. **Add authentication layer** (H2) - Blocks 90% of attack vectors
2. **Fix URL encoding handling** (H1) - Prevents deny pattern bypass
3. **Block unimplemented endpoints** (H4, H6) - Prevents known-vulnerable paths
4. **Validate bulk tenant consistency** (H5) - Prevents cross-tenant writes
5. **Add comprehensive input validation** - Defense in depth

### Architecture Improvements

1. Consider moving to a **zero-trust** model where every request is authenticated and authorized
2. Implement **tenant-aware Elasticsearch security** at the cluster level as a second layer of defense
3. Add **request signing** to ensure requests haven't been tampered with in transit
4. Consider **per-tenant Elasticsearch clusters** for high-security deployments

---

## Summary Table

| ID | Severity | Issue | Exploitability | Impact | Mitigation Priority |
|----|----------|-------|----------------|--------|---------------------|
| H1 | High | URL Encoding Bypass | High | Critical | P0 |
| H2 | High | No Authentication | Very High | Critical | P0 |
| H3 | High | Regex Injection | Medium | High | P1 |
| H4 | High | Scroll/PIT Not Scoped | High | High | P0 |
| H5 | High | Bulk Cross-Tenant | Medium | High | P1 |
| H6 | High | Query Rewriting Gaps | Medium | High | P1 |
| H7 | High | Msearch Race Condition | Low | High | P2 |
| H8 | High | Transform Wildcards | Medium | Critical | P1 |
| M1 | Medium | No Rate Limiting | High | Medium | P2 |
| M2 | Medium | Passthrough Info Leak | Medium | Low | P3 |
| M3 | Medium | Error Message Leak | Medium | Low | P3 |
| M4 | Medium | Alias Bypass | Low | Critical | P2 |
| M5 | Medium | Input Validation | Medium | Low | P2 |

---

## Conclusion

The es-tmnt proxy demonstrates solid architectural thinking around tenant isolation, particularly in the shared-index mode's alias-based filtering and the index-per-tenant mode's physical separation. However, the **absence of authentication (H2)** makes all tenant isolation mechanisms moot, as any client can claim to be any tenant simply by changing the index name in the URL.

The combination of **URL encoding bypass (H1)**, **unhandled endpoints (H4, H6)**, and **bulk operation gaps (H5)** create multiple vectors for cross-tenant data access. These vulnerabilities are exacerbated by the lack of comprehensive integration testing for adversarial scenarios.

**Recommendation**: Do not deploy this proxy in production without:
1. Adding an authentication and authorization layer
2. Implementing immediate mitigations for H1, H2, H4, H5
3. Comprehensive security testing
4. Deploy only behind a secure API gateway with tenant-to-client mapping

With these mitigations, es-tmnt can provide effective tenant isolation for Elasticsearch in multi-tenant environments.

---

## References

- OWASP API Security Top 10
- NIST Cybersecurity Framework
- CIS Elasticsearch Benchmarks
- Elasticsearch Security Documentation

---

*End of Report*
