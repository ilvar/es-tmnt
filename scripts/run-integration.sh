#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

run_mode() {
  local mode=$1
  local env_file=$2

  echo "Running integration tests for ${mode} mode"
  docker compose -f "${ROOT_DIR}/docker-compose.yml" --env-file "${ROOT_DIR}/${env_file}" up -d --build

  echo "Waiting for Elasticsearch"
  until curl -s http://localhost:9200 > /dev/null; do
    sleep 2
  done

  echo "Waiting for proxy"
  until curl -s http://localhost:8080/_cat/nodes > /dev/null; do
    sleep 2
  done

  local test_name=""
  if [[ "${mode}" == "shared" ]]; then
    test_name="TestSharedMode"
  else
    test_name="TestPerTenantMode"
  fi

  (cd "${ROOT_DIR}" && \
    mkdir -p coverage && \
    TEST_MODE=${mode} \
    PROXY_URL=http://localhost:8080 \
    ES_URL=http://localhost:9200 \
    ALIAS_TEMPLATE=$(grep '^ES_TMNT_SHARED_INDEX_ALIAS_TEMPLATE=' "${ROOT_DIR}/${env_file}" | cut -d'=' -f2-) \
    TENANT_FIELD=$(grep '^ES_TMNT_SHARED_INDEX_TENANT_FIELD=' "${ROOT_DIR}/${env_file}" | cut -d'=' -f2-) \
    REAL_INDEX=$(grep '^ES_TMNT_INDEX_PER_TENANT_TEMPLATE=' "${ROOT_DIR}/${env_file}" | cut -d'=' -f2-) \
    go test -v ./tests -run "${test_name}" -coverprofile "coverage/integration-${mode}.out" -coverpkg=./... && \
    go tool cover -func "coverage/integration-${mode}.out")

  docker compose -f "${ROOT_DIR}/docker-compose.yml" --env-file "${ROOT_DIR}/${env_file}" down -v
}

run_mode shared config/shared.env
run_mode per-tenant config/per-tenant.env
