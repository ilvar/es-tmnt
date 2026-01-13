# Agent Notes: Architecture & Logic Overview

This repository hosts a Go-based proxy that sits in front of Elasticsearch. The primary
logic lives under `internal/proxy`, with configuration and wiring in `internal/config`
and `cmd` for the binary entrypoint.

## High-level architecture
- **cmd/**: CLI entrypoint and wiring for configuration, logging, and server startup.
- **internal/config/**: Configuration structs and env/flag loading helpers used by the proxy.
- **internal/proxy/**: The HTTP proxy implementation, including request parsing, routing,
  and rewrite logic for multi-tenant behavior.

## Proxy flow (core logic)
1. **Parse & classify**: Requests are parsed to understand the Elasticsearch path,
   method, and operation type (search, index, update, mapping, etc.).
2. **Tenant extraction**: The proxy extracts a tenant from the request path using a
   configurable regex. Capture groups are used to preserve path prefix/postfix while
   pulling out the tenant identifier.
3. **Routing decision**: A router determines whether the request is supported and how
   it should be rewritten based on the operation and configured tenancy mode.
4. **Rewrite**: The proxy rewrites the path and/or body:
   - **Shared-index mode**: requests are routed to a shared index; search targets become
     tenant aliases and indexing injects a `tenant_id` field into `_source`.
   - **Index-per-tenant mode**: requests are routed to per-tenant indices and field paths
     in queries/mappings are prefixed with the originating index name. Indexing and update
     bodies are adjusted to match the rewritten field paths.
5. **Fallback/unsupported**: If a request does not match a supported pattern, the proxy
   returns a clear HTTP 4xx error unless the path is explicitly whitelisted.

## Testing
Proxy behavior is covered by unit tests under `internal/proxy`, which validate tenant
extraction, routing, and rewrite behavior across supported operations and modes.
