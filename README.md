# es-tmnt
Multi-tenancy for Elasticsearch

## Supported endpoints and behavior

The proxy only supports a small set of Elasticsearch endpoints. Requests outside this
list return a 4xx error unless they are explicitly configured as passthrough paths.

### Endpoint groups

| Endpoint | Methods | Notes |
| --- | --- | --- |
| `/{index}/_search`, `/_search` | `GET`, `POST` | Searches are routed to the tenant alias (shared mode) or per-tenant index (index-per-tenant mode). Root searches require an `index` query parameter. |
| `/{index}/_search/template`, `/_search/template` | `GET`, `POST` | Search templates are routed to the tenant alias (shared mode) or per-tenant index (index-per-tenant mode). Root templates require an `index` query parameter. |
| `/{index}/_doc` | `POST`, `PUT` | Indexing injects tenant fields (shared) or nests documents under the base index name (per-tenant). |
| `/{index}/_update/{id}` | `POST` | Update payloads are rewritten the same way as indexing bodies. |
| `/{index}/_bulk` | `POST` | Bulk actions are rewritten per tenancy mode, including `_index` target adjustments. |
| `/_bulk` | `POST` | Root bulk endpoint is supported with the same rewrite behavior. |
| `/{index}` | `PUT`, `DELETE` | Index create/delete requests target the shared or per-tenant index, and creation bodies can rewrite mappings. |
| `/{index}/_mapping` | `PUT`, `POST` | Mapping updates are rewritten in index-per-tenant mode to nest field mappings under the base index name. |
| `/{index}/_get/{id}` | `GET` | Rewritten into a tenant-scoped `_search` using an `ids` query. |
| `/{index}/_source/{id}` | `GET` | Rewritten into a tenant-scoped `_search` using an `ids` query. |
| `/{index}/_mget` | `POST` | Rewritten into a tenant-scoped `_search` using an `ids` query. |
| `/{index}/_delete/{id}` | `DELETE` | Rewritten into a tenant-scoped `_delete_by_query` using an `ids` query. |
| `/{index}/_delete_by_query` | `POST` | Query bodies are rewritten in index-per-tenant mode; shared mode uses tenant alias routing. |
| `/{index}/_update_by_query` | `POST` | Query bodies are rewritten in index-per-tenant mode; shared mode uses tenant alias routing. |
| `/{index}/_count` | `GET`, `POST` | Rewritten into a tenant-scoped `_search` with `size: 0`. |
| `/_delete_by_query`, `/_update_by_query` | `POST` | Supported when an `index` query parameter is supplied; behaves like the index-scoped variants. |
| `/{index}/_query`, `/{index}/_rank_eval`, `/_query`, `/_rank_eval` | `GET`, `POST` | Query and rank eval requests are rewritten per tenancy mode. Root endpoints require an `index` query parameter. |
| `/{index}/_explain` | `GET`, `POST` | Explain requests are rewritten per tenancy mode. |
| `/{index}/_search_shards`, `/{index}/_field_caps`, `/{index}/_terms_enum` | `GET`, `POST` | Routed to the shared or per-tenant index without body rewriting. |
| `/{index}/_settings`, `/{index}/_stats`, `/{index}/_segments`, `/{index}/_recovery`, `/{index}/_refresh` | varies | Routed to the shared or per-tenant index without body rewriting. |
| `/{index}/_flush`, `/{index}/_forcemerge`, `/{index}/_cache/clear`, `/{index}/_open`, `/{index}/_close` | varies | Routed to the shared or per-tenant index without body rewriting. |
| `/{index}/_shrink`, `/{index}/_split`, `/{index}/_rollover`, `/{index}/_clone`, `/{index}/_freeze` | varies | Routed to the shared or per-tenant index without body rewriting. |
| `/{index}/_unfreeze`, `/{index}/_upgrade`, `/{index}/_alias/*` | varies | Routed to the shared or per-tenant index without body rewriting. |
| `/{index}/_termvectors/*`, `/{index}/_mtermvectors` | varies | Forwarded to the shared or per-tenant index without body rewriting. |
| `/_cat/indices` | `GET` | Cat indices responses include `TENANT_ID` for indices matching the tenant regex. |
| `/_analyze`, `/{index}/_analyze` | `GET`, `POST` | Analyze requests are routed to the tenant index based on the `index` query parameter or path. |
| `/_msearch` | `POST` | Multi-search requests are rewritten per tenancy mode. |
| `/_msearch/template`, `/_render/template` | `GET`, `POST` | Template rendering endpoints are passed through. |
| `/_transform/*` | `GET`, `PUT`, `POST`, `DELETE` | Transform bodies rewrite source indices for search and destination indices for writes. |
| `/_rollup/*` | `GET`, `PUT`, `POST`, `DELETE` | Rollup bodies rewrite `index_pattern` for tenant-aware searches. |

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

Cluster-level system APIs are also forwarded by default, grouped as follows:

- Cluster and cat APIs: `/_cluster/*`, `/_cat/*` (except `/_cat/indices`), `/_nodes/*`
- Snapshot and storage APIs: `/_snapshot/*`, `/_searchable_snapshots/*`
- Alias and template APIs: `/_alias/*`, `/_aliases`, `/_template/*`,
  `/_index_template/*`, `/_component_template/*`
- Data stream and resolve APIs: `/_resolve/*`, `/_data_stream/*`, `/_dangling/*`
- Lifecycle and task APIs: `/_slm/*`, `/_ilm/*`, `/_tasks/*`, `/_scripts/*`
- Autoscaling and migration APIs: `/_autoscaling/*`, `/_migration/*`, `/_features/*`
- Security and licensing APIs: `/_security/*`, `/_license/*`
- ML, watcher, graph, and CCR APIs: `/_ml/*`, `/_watcher/*`, `/_graph/*`, `/_ccr/*`


### TODO: Unhandled Elasticsearch REST endpoints

The proxy does not currently modify or explicitly pass through the following
Elasticsearch REST API endpoints. These are grouped by namespace/pattern; every
endpoint under each pattern is currently unhandled unless listed in
“Supported endpoints and behavior” or “Passthrough paths” above.

#### Document APIs (other than `_doc` and `_update`)

- `/{index}/_validate/query`

#### Search, query, and analytics

- `/_explain`
- `/_search/scroll`, `/_scroll`, `/_clear/scroll`, `/_pit`
- `/_async_search/*`, `/_knn_search`, `/_eql/*`, `/_sql/*`
- `/{index}/_mvt/*`
- `/_application/*`, `/_query_rules/*`, `/_synonyms/*`

#### Ingest and pipelines

- `/_ingest/*`
- `/_enrich/*`


## Development

Build and run locally:

```bash
ES_TMNT_HTTP_PORT=8080 ES_TMNT_UPSTREAM_URL=http://localhost:9200 go run ./cmd/es-tmnt
```

Configuration can be supplied via environment variables or a JSON config file path in
`ES_TMNT_CONFIG`.

## Unit tests

Run the unit test suite with the helper script:

```bash
./test.sh
```

## Integration tests

Run the integration tests alongside Elasticsearch via Docker Compose:

```bash
./scripts/run-integration.sh
```

The script runs both shared-index and index-per-tenant modes with configuration from
`config/shared.env` and `config/per-tenant.env`. Coverage summaries are printed for
each mode and profiles are written to `coverage/integration-shared.out` and
`coverage/integration-per-tenant.out`.
