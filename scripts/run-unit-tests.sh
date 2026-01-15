#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

(
  cd "${ROOT_DIR}"
  mkdir -p coverage
  go test ./... -coverprofile "coverage/unit.out" -coverpkg=./...
  go tool cover -func "coverage/unit.out"
)
