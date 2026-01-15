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
| `/{index}` | `PUT`, `DELETE` | Index create/delete requests target the shared or per-tenant index, and creation bodies can rewrite mappings. |
| `/{index}/_mapping` | `PUT`, `POST` | Mapping updates are rewritten in index-per-tenant mode to nest field mappings under the base index name. |
| `/{index}/_get/{id}` | `GET` | Rewritten into a tenant-scoped `_search` using an `ids` query. |
| `/{index}/_mget` | `POST` | Rewritten into a tenant-scoped `_search` using an `ids` query. |
| `/{index}/_delete/{id}` | `DELETE` | Rewritten into a tenant-scoped `_delete_by_query` using an `ids` query. |
| `/{index}/_delete_by_query` | `POST` | Query bodies are rewritten in index-per-tenant mode; shared mode uses tenant alias routing. |
| `/{index}/_update_by_query` | `POST` | Query bodies are rewritten in index-per-tenant mode; shared mode uses tenant alias routing. |
| `/{index}/_count` | `GET`, `POST` | Rewritten into a tenant-scoped `_search` with `size: 0`. |
| `/_delete_by_query`, `/_update_by_query` | `POST` | Supported when an `index` query parameter is supplied; behaves like the index-scoped variants. |
| Index management endpoints | varies | `/{index}/_settings`, `/{index}/_stats`, `/{index}/_segments`, `/{index}/_recovery`, `/{index}/_refresh`, `/{index}/_flush`, `/{index}/_forcemerge`, `/{index}/_cache/clear`, `/{index}/_open`, `/{index}/_close`, `/{index}/_shrink`, `/{index}/_split`, `/{index}/_rollover`, `/{index}/_clone`, `/{index}/_freeze`, `/{index}/_unfreeze`, `/{index}/_upgrade`, `/{index}/_alias/*` are routed to the shared or per-tenant index without body rewriting. |
| Document passthrough endpoints | varies | `/{index}/_termvectors/*`, `/{index}/_mtermvectors`, `/{index}/_explain/*` are forwarded to the shared or per-tenant index without body rewriting. |
| `/_cat/indices` | `GET` | Cat indices responses include `TENANT_ID` for indices matching the tenant regex. |

All other `/_*` system endpoints (outside the cluster passthrough list), index endpoints,
and unsupported methods return a 400 error unless configured as passthrough paths.

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

Cluster-level system APIs are also forwarded by default, including `/_cluster/*`,
`/_cat/*` (except `/_cat/indices`), and `/_nodes/*`. The proxy also forwards
`/_alias/*`, `/_aliases`, `/_template/*`, `/_index_template/*`, `/_component_template/*`,
`/_resolve/*`, `/_data_stream/*`, and `/_dangling/*` without modification.

### TODO: Unhandled Elasticsearch REST endpoints

The proxy does not currently modify or explicitly pass through the following
Elasticsearch REST API endpoints. These are grouped by namespace/pattern; every
endpoint under each pattern is currently unhandled unless listed in
“Supported endpoints and behavior” or “Passthrough paths” above.

#### Index and alias management

- `/_alias/*`, `/_aliases`
- `/_template/*`, `/_index_template/*`, `/_component_template/*`
- `/_resolve/*`, `/_data_stream/*`, `/_dangling/*`

#### Document APIs (other than `_doc` and `_update`)

- `/{index}/_source/*`
- `/{index}/_rank_eval`, `/{index}/_validate/query`
- `/{index}/_search_shards`, `/{index}/_field_caps`

#### Search, query, and analytics

- `/_analyze`
- `/_search`, `/{index}/_search/template`, `/_msearch`, `/_msearch/template`,
  `/_search/template`, `/_render/template`
- `/_search/scroll`, `/_scroll`, `/_clear/scroll`, `/_pit`
- `/_async_search/*`, `/_knn_search`, `/_eql/*`, `/_sql/*`
- `/_query`, `/_explain`, `/_rank_eval`
- `/_terms_enum`
- `/{index}/_mvt/*`
- `/_application/*`, `/_query_rules/*`, `/_synonyms/*`

#### Ingest and pipelines

- `/_ingest/*`
- `/_enrich/*`

#### Snapshot and lifecycle management

- `/_snapshot/*`
- `/_searchable_snapshots/*`
- `/_slm/*`
- `/_ilm/*`

#### Tasks, scripts, and cluster utilities

- `/_tasks/*`
- `/_scripts/*`
- `/_autoscaling/*`
- `/_migration/*`
- `/_features/*`

#### Security, licensing, and governance

- `/_security/*`
- `/_license/*`

#### Machine learning and advanced features

- `/_ml/*`
- `/_transform/*`
- `/_rollup/*`
- `/_watcher/*`
- `/_graph/*`
- `/_ccr/*`

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
