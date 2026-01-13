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
  "mode": "regex",
  "passthrough": ["/_cluster", "/_security"],
  "regex": {
    "enabled": true,
    "pattern": "^/tenant/([^/]+)/",
    "replacement": "/$1/"
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
- `ES_TMNT_MODE`: Rewrite mode (`regex` or `passthrough`).
- `ES_TMNT_PASSTHROUGH`: Comma-separated list of path prefixes that bypass rewriting.
- `ES_TMNT_REGEX_ENABLED`: `true`/`false` to enable regex rewriting.
- `ES_TMNT_REGEX_PATTERN`: Regex pattern applied to the request path.
- `ES_TMNT_REGEX_REPLACEMENT`: Replacement string for regex rewriting.

The proxy extracts a tenant name from paths like `/tenant/{name}/...` and forwards it in the
`X-ES-Tenant` header.
