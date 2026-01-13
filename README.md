# es-tmnt
Multi-tenancy for Elasticsearch

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
