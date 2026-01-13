# es-tmnt

Multi-tenancy proxy for Elasticsearch.

## Build

```bash
go build ./cmd/es-tmnt
```

## Run

```bash
./es-tmnt
```

## Configuration

Configuration loads from defaults, then an optional JSON file, then environment variable overrides.

### Config file

Set `ES_TMNT_CONFIG=/path/to/config.json` to load a JSON config file.

```json
{
  "ports": {
    "http": 8080,
    "admin": 9090
  },
  "upstream_url": "http://localhost:9200",
  "mode": "shared-index",
  "passthrough": ["/_cluster", "/_security"],
  "tenant_regex": {
    "pattern": "^(?P<prefix>.*)/tenant/(?P<tenant>[^/]+)(?P<postfix>/.*)?$"
  },
  "shared_index": {
    "name": "shared-index",
    "alias_format": "{index}-{tenant}",
    "tenant_field": "tenant_id"
  },
  "index_per_tenant": {
    "index_format": "tenant-{tenant}"
  },
  "read_timeout": "10s",
  "write_timeout": "10s",
  "idle_timeout": "60s"
}
```

### Environment variables

- `ES_TMNT_CONFIG`: Optional path to a JSON configuration file.
- `ES_TMNT_HTTP_PORT`: Port for the proxy HTTP server.
- `ES_TMNT_ADMIN_PORT`: Port for the admin server (0 disables it).
- `ES_TMNT_UPSTREAM_URL`: Upstream Elasticsearch URL.
- `ES_TMNT_MODE`: Tenant mode (`shared-index` or `index-per-tenant`).
- `ES_TMNT_PASSTHROUGH`: Comma-separated list of path prefixes that bypass rewriting.
- `ES_TMNT_TENANT_REGEX_PATTERN`: Regex pattern with `prefix`, `tenant`, and `postfix` capture groups.
- `ES_TMNT_SHARED_INDEX_NAME`: Shared index name for shared-index mode.
- `ES_TMNT_SHARED_INDEX_ALIAS_FORMAT`: Alias template for shared-index mode (supports `{index}` and `{tenant}`).
- `ES_TMNT_SHARED_INDEX_TENANT_FIELD`: Tenant field injected into indexed documents.
- `ES_TMNT_INDEX_PER_TENANT_FORMAT`: Index template for index-per-tenant mode (supports `{tenant}`).

The proxy extracts a tenant name from paths using the configured regex and forwards it in the
`X-ES-Tenant` header.
