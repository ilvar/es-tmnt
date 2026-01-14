# es-tmnt
Multi-tenancy for Elasticsearch

## Supported endpoints and behavior

The proxy only supports a small set of Elasticsearch endpoints. Requests outside this
list return a 4xx error unless they are explicitly configured as passthrough paths.

### Endpoint groups

| Endpoint | Methods | Notes |
| --- | --- | --- |
| `/{index}/_search` | `GET`, `POST` | Searches are routed to the tenant alias (shared mode) or per-tenant index (index-per-tenant mode). |
| `/{index}/_doc` | `POST`, `PUT` | Indexing injects tenant fields (shared) or nests documents under the base index name (per-tenant). |
| `/{index}/_update/{id}` | `POST` | Update payloads are rewritten the same way as indexing bodies. |
| `/{index}/_bulk` | `POST` | Bulk actions are rewritten per tenancy mode, including `_index` target adjustments. |
| `/_bulk` | `POST` | Root bulk endpoint is supported with the same rewrite behavior. |

All other `/_*` system endpoints, index root endpoints (`/{index}`), and unsupported
methods return a 400 error unless configured as passthrough paths.

### Tenant extraction and rewrite behavior

- The tenant identifier is extracted from the index name using a configurable regex that
  must include named groups `prefix`, `tenant`, and `postfix`. The base index name is
  derived from the `index` group when present, or from `prefix + postfix` otherwise.
- **Shared-index mode**:
  - Search requests are routed to a tenant alias rendered from the alias template.
  - Indexing and update bodies inject the tenant field (configured via `tenant_field`).
- **Index-per-tenant mode**:
  - Requests are routed to a per-tenant index rendered from the index template.
  - Query bodies rewrite field paths (including `match`, `term`, `range`, `sort`,
    `_source`, and `fields`) by prefixing with the base index name.
  - Document and update bodies are nested under the base index name.
- **Bulk requests**:
  - Each action line rewrites `_index` to the shared or per-tenant index.
  - Source/update lines are rewritten using the same document and update rules above.

### Passthrough paths

Configured passthrough paths bypass all proxy logic and are forwarded directly to
Elasticsearch. A trailing `*` in the configuration acts as a prefix match.

## Development

Build and run locally:

```bash
ES_TMNT_HTTP_PORT=8080 ES_TMNT_UPSTREAM_URL=http://localhost:9200 go run ./cmd/es-tmnt
```

Configuration can be supplied via environment variables or a JSON config file path in
`ES_TMNT_CONFIG`.

## Integration tests

Run the integration tests alongside Elasticsearch via Docker Compose:

```bash
./scripts/run-integration.sh
```

The script runs both shared-index and index-per-tenant modes with configuration from
`config/shared.env` and `config/per-tenant.env`.
