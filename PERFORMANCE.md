# Performance Review: Elasticsearch Multi-Tenancy Proxy

**Date**: 2026-01-19
**Goal**: Minimize proxy overhead for production Elasticsearch traffic

## Executive Summary

The proxy adds **4-18 µs overhead per request** depending on operation type and tenancy mode. Key findings:

- **Shared mode** is significantly faster (~9 ns query overhead vs 17 µs in per-tenant mode)
- **Per-tenant mode** requires extensive JSON rewriting with 157 allocations per query
- **Bulk operations** scale linearly but accumulate overhead (79 µs per 10 operations)
- **Critical path**: JSON marshal/unmarshal dominates latency (70-80% of processing time)

### Performance at Scale

| Load Level | Operations/sec | Est. CPU Cost | Recommendation |
|------------|---------------|---------------|----------------|
| Light (100 req/s) | 100 | ~0.2% single core | ✅ No concerns |
| Medium (1K req/s) | 1,000 | ~2% single core | ✅ Acceptable |
| High (10K req/s) | 10,000 | ~20% single core | ⚠️ Monitor GC pressure |
| Very High (50K req/s) | 50,000 | ~100% single core | 🔴 Implement optimizations |

## Benchmark Results

Test environment: AMD64 XEON Platinum 8581C @ 2.10GHz, 16 cores

### Core Operations

```
Operation                                      Time/op    Bytes/op   Allocs/op
================================================================================
Index Parsing (regex + extraction)              526 ns      193 B         6
Template Rendering (alias)                      873 ns      584 B        11
Template Rendering (shared index)               611 ns      536 B         8
Template Rendering (per-tenant index)           916 ns      584 B        11
```

### Document Operations

```
Operation                                      Time/op    Bytes/op   Allocs/op
================================================================================
Document Rewrite (shared mode)                 4.33 µs    1,377 B       41
Document Rewrite (per-tenant mode)             4.40 µs    1,713 B       43
```

**Analysis**: Document rewriting is fast and mode-independent. Overhead is acceptable.

### Query Operations

```
Operation                                      Time/op    Bytes/op   Allocs/op
================================================================================
Query Rewrite (shared mode - passthrough)      9.05 ns        0 B        0  ✅
Query Rewrite (per-tenant mode)               17.24 µs   10,154 B      157  ⚠️
```

**Analysis**: **CRITICAL BOTTLENECK**. Per-tenant mode adds 1,900x overhead vs shared mode:
- 157 allocations per query (GC pressure)
- 10 KB temporary allocation
- Deep JSON tree traversal for field rewriting

### Bulk Operations

```
Operation                                      Time/op    Bytes/op   Allocs/op
================================================================================
Bulk 10 ops (shared mode)                     79.22 µs   36,885 B      737
Bulk 10 ops (per-tenant mode)                 84.12 µs   40,519 B      777
Bulk 100 ops (shared mode)                   779.27 µs  358,517 B    7,249
Bulk 100 ops (per-tenant mode)               835.10 µs  395,343 B    7,650
```

**Analysis**: Bulk operations scale linearly. ~8 µs and ~400 bytes per operation. Mode difference is minimal (~5% overhead for per-tenant).

### Foundation Operations

```
Operation                                      Time/op    Bytes/op   Allocs/op
================================================================================
JSON Marshal                                   1.16 µs      496 B        12
JSON Unmarshal                                 2.16 µs      424 B        23
JSON Marshal + Unmarshal                       3.61 µs      920 B        35
io.ReadAll                                      324 ns      560 B         2
```

## Bottleneck Analysis

### 1. Per-Tenant Query Rewriting (CRITICAL - 17.24 µs)

**Impact**: Affects every search, count, delete_by_query, update_by_query operation.

**Root causes**:
- Recursive JSON tree traversal (`rewriteQueryValue`)
- Field prefixing for every leaf field (`match`, `term`, `range`, `sort`, `_source`)
- Multiple intermediate map allocations
- String concatenation for field names

**Code location**: `internal/proxy/rewrite.go:386-414` (rewriteQueryValue)

### 2. JSON Marshal/Unmarshal (3-4 µs per operation)

**Impact**: Every request with a body requires marshal/unmarshal cycle.

**Root causes**:
- Go's `encoding/json` uses reflection
- Creates intermediate `map[string]interface{}` structures
- Memory allocations for every object/array

**Code locations**:
- `internal/proxy/rewrite.go:11-21` (rewriteDocumentBody)
- `internal/proxy/rewrite.go:226-236` (rewriteQueryBody)
- `internal/proxy/rewrite.go:45-122` (rewriteBulkBody)

### 3. Template Rendering (611-916 ns)

**Impact**: Every request that needs index/alias name transformation.

**Root causes**:
- Template parsing/execution overhead
- String buffer allocations
- No caching for common tenant/index pairs

**Code locations**:
- `internal/proxy/proxy.go:1129-1145` (renderAlias, renderIndex)

### 4. Regex Matching (526 ns, 6 allocs)

**Impact**: Every request requires tenant extraction from index name.

**Root causes**:
- Regex compilation result not fully optimized
- Submatch extraction creates string slices

**Code location**: `internal/proxy/proxy.go:1100-1127` (parseIndex)

## Mitigation Plan

### Priority 1: HIGH IMPACT (Immediate - Targets 50-70% reduction)

#### 1.1 Cache Template Renderings
**Estimated savings**: 600-900 ns per request
**Implementation effort**: Low (2-4 hours)

```go
type renderCache struct {
    mu    sync.RWMutex
    cache map[string]string // key: "index:tenant:template", value: rendered
}

func (p *Proxy) renderIndexCached(tmpl *template.Template, index, tenant string) (string, error) {
    key := index + ":" + tenant + ":" + tmpl.Name()

    // Fast path: read lock
    p.renderCache.mu.RLock()
    if cached, ok := p.renderCache.cache[key]; ok {
        p.renderCache.mu.RUnlock()
        return cached, nil
    }
    p.renderCache.mu.RUnlock()

    // Slow path: render and cache
    result, err := p.renderIndex(tmpl, index, tenant)
    if err != nil {
        return "", err
    }

    p.renderCache.mu.Lock()
    p.renderCache.cache[key] = result
    p.renderCache.mu.Unlock()

    return result, nil
}
```

**Trade-off**: Memory usage grows with unique index/tenant combinations. Add LRU eviction if needed.

#### 1.2 Optimize Per-Tenant Query Rewriting
**Estimated savings**: 8-12 µs per query (50-70% reduction)
**Implementation effort**: Medium (1-2 days)

**Strategy A: Stream-based JSON rewriting (recommended)**
Use `encoding/json.Decoder` + `encoding/json.Encoder` with streaming:

```go
func (p *Proxy) rewriteQueryBodyStream(body []byte, baseIndex string) ([]byte, error) {
    var buf bytes.Buffer
    dec := json.NewDecoder(bytes.NewReader(body))
    enc := json.NewEncoder(&buf)

    // Use token-based streaming to rewrite field names on the fly
    // Avoid building full in-memory tree
    return buf.Bytes(), rewriteTokenStream(dec, enc, baseIndex)
}
```

**Strategy B: Pre-compile field rewrite rules**
Build a state machine for common query patterns:

```go
type fieldRewriter struct {
    prefix string
    // Compiled patterns for fast path matching
    commonFields map[string]string // "message" -> "logs.message"
}

func (r *fieldRewriter) rewriteField(field string) string {
    if rewritten, ok := r.commonFields[field]; ok {
        return rewritten
    }
    return r.prefix + "." + field
}
```

**Strategy C: JSON parser optimization**
Use `github.com/valyala/fastjson` or `github.com/json-iterator/go` for faster parsing.

**Expected outcome**: Reduce from 17 µs to 5-9 µs, reduce allocations from 157 to 30-50.

#### 1.3 Add Fast Path Detection
**Estimated savings**: 100% for passthrough requests
**Implementation effort**: Low (1-2 hours)

```go
func (p *Proxy) rewriteQueryBody(body []byte, baseIndex string) ([]byte, error) {
    if isSharedMode(p.cfg.Mode) {
        return body, nil  // Already has fast path
    }

    // Fast path: empty or minimal queries
    if len(body) < 10 || bytes.Equal(body, []byte("{}")) {
        return body, nil
    }

    // Fast path: match_all query only
    if bytes.Contains(body, []byte("match_all")) && !bytes.Contains(body, []byte("match")) {
        return body, nil
    }

    // Slow path: full rewrite
    return p.rewriteQueryBodyFull(body, baseIndex)
}
```

### Priority 2: MEDIUM IMPACT (Next iteration - Targets 20-30% reduction)

#### 2.1 Pool Reusable Buffers
**Estimated savings**: 200-400 ns per request, reduces GC pressure
**Implementation effort**: Low (2-3 hours)

```go
var bufferPool = sync.Pool{
    New: func() interface{} {
        return new(bytes.Buffer)
    },
}

func (p *Proxy) rewriteBulkBody(body []byte, pathIndex string) ([]byte, error) {
    buf := bufferPool.Get().(*bytes.Buffer)
    buf.Reset()
    defer bufferPool.Put(buf)

    // Use buf instead of creating new bytes.Buffer
    // ...
    return buf.Bytes(), nil
}
```

#### 2.2 Optimize Regex Matching
**Estimated savings**: 200-300 ns per request
**Implementation effort**: Low (1-2 hours)

```go
// Pre-compute group indices once at startup
type indexParser struct {
    re           *regexp.Regexp
    indexGroup   int
    tenantGroup  int
    prefixGroup  int
    postfixGroup int

    // Add string pooling for common patterns
    tenantCache map[string]string
}

func (ip *indexParser) parse(index string) (base, tenant string, err error) {
    matches := ip.re.FindStringSubmatch(index)
    if matches == nil {
        return "", "", fmt.Errorf("no match")
    }

    // Avoid repeated string allocations for common tenants
    tenantID := matches[ip.tenantGroup]
    if cached, ok := ip.tenantCache[tenantID]; ok {
        tenantID = cached
    } else {
        ip.tenantCache[tenantID] = tenantID
    }

    // ... rest of parsing
}
```

#### 2.3 Batch Operations Optimization
**Estimated savings**: 10-15% for bulk operations
**Implementation effort**: Medium (4-6 hours)

For bulk operations, parse action and source lines together to avoid redundant string operations:

```go
func (p *Proxy) rewriteBulkBody(body []byte, pathIndex string) ([]byte, error) {
    // Pre-allocate output buffer based on input size
    output := make([]byte, 0, len(body)+len(body)/10) // 10% overhead estimate

    // Parse all lines first, then process in batch
    lines := bytes.Split(body, []byte("\n"))

    // Process in batches of 100 action+source pairs
    for i := 0; i < len(lines); i += batchSize {
        // ... batch processing
    }
}
```

### Priority 3: LOW IMPACT (Future optimization - Targets 5-10% reduction)

#### 3.1 Request Path Optimization
- Pre-compile path segment patterns
- Use tries for endpoint routing instead of string comparison chains

#### 3.2 Instrumentation Optimization
- Make logging fully conditional (zero-cost when disabled)
- Use structured logging with pre-allocated fields

#### 3.3 Connection Pooling Tuning
- Optimize httputil.ReverseProxy transport settings
- Tune MaxIdleConns, IdleConnTimeout

## Testing Plan

### Performance Regression Tests

Add to CI/CD pipeline:

```bash
# Run benchmarks on every PR
go test -bench=. -benchmem ./internal/proxy/ > bench-new.txt

# Compare against baseline (using benchstat)
benchstat bench-baseline.txt bench-new.txt

# Fail if critical paths regress >10%
```

### Load Testing

Use `wrk` or `k6` for realistic load testing:

```bash
# Baseline: Direct Elasticsearch
wrk -t4 -c100 -d30s --latency http://localhost:9200/logs-acme-prod/_search

# With proxy
wrk -t4 -c100 -d30s --latency http://localhost:8080/logs-acme-prod/_search

# Target: <5% p50 latency overhead, <10% p99 latency overhead
```

## Monitoring Recommendations

### Key Metrics

1. **Request latency**: Track p50, p95, p99 for each endpoint type
2. **Throughput**: Requests per second by operation type
3. **Memory allocation rate**: MB/s allocated (GC pressure indicator)
4. **GC pause time**: Should be <1ms for p99
5. **CPU utilization**: Per-core usage

### Alerting Thresholds

- **p99 latency > 50ms**: Investigate proxy overhead
- **GC pause > 5ms**: Memory allocation rate too high
- **CPU > 80%**: Scale horizontally or optimize hot paths
- **Allocation rate > 100 MB/s**: Check for allocation leaks

## Expected Outcomes

### After Priority 1 Optimizations

| Metric | Current | Target | Improvement |
|--------|---------|--------|-------------|
| Query (per-tenant) | 17.24 µs | 5-7 µs | 60-70% |
| Template rendering | 600-900 ns | 50-100 ns | 85-90% |
| Total request overhead | 18-25 µs | 7-10 µs | 60% |
| Memory allocations (query) | 157 | 30-50 | 68-80% |

### After All Optimizations

| Metric | Current | Target | Improvement |
|--------|---------|--------|-------------|
| Query (per-tenant) | 17.24 µs | 3-5 µs | 70-82% |
| Bulk 10 ops | 79-84 µs | 45-55 µs | 35-45% |
| Total request overhead | 18-25 µs | 5-8 µs | 68% |

## Implementation Roadmap

### Week 1-2: Critical Path (Priority 1)
- [ ] Implement template rendering cache
- [ ] Add fast path detection for queries
- [ ] Start stream-based query rewriting (POC)

### Week 3-4: Core Optimization (Priority 1 continued)
- [ ] Complete stream-based query rewriting
- [ ] Benchmark and validate improvements
- [ ] Add performance regression tests to CI

### Week 5-6: Polish (Priority 2)
- [ ] Implement buffer pooling
- [ ] Optimize regex matching with caching
- [ ] Load testing and tuning

### Week 7+: Future Improvements (Priority 3)
- [ ] Advanced routing optimizations
- [ ] Instrumentation improvements
- [ ] Connection pooling tuning

## Conclusion

The proxy currently adds **18-25 µs overhead** for per-tenant mode queries, primarily due to JSON rewriting. This is acceptable for most workloads (<10K req/s), but optimization is recommended for high-throughput scenarios.

**Recommended approach**: Implement Priority 1 optimizations (template caching + query rewrite optimization) to achieve **60-70% reduction** in overhead, bringing total overhead to **7-10 µs per request**.

For context:
- Network RTT within datacenter: 200-500 µs
- Elasticsearch query processing: 1-100+ ms
- **Current proxy overhead: 0.02-0.25% of typical query time**
- **After optimization: 0.007-0.1% of typical query time**

The proxy overhead is already minimal relative to Elasticsearch query processing, but optimizations will improve scalability and reduce CPU/memory costs at high request rates.
