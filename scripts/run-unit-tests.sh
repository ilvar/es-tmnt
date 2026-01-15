#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

(
  cd "${ROOT_DIR}"
  mkdir -p coverage
  go test ./... -coverprofile "coverage/unit.out" -coverpkg=./...
  coverage_tmp=$(mktemp)
  awk '
    NR == 1 { next }
    {
      split($1, parts, ":")
      file = parts[1]
      statements = $2
      count = $3
      total[file] += statements
      if (count > 0) {
        covered[file] += statements
      }
      totalStatements += statements
      if (count > 0) {
        coveredStatements += statements
      }
    }
    END {
      for (file in total) {
        pct = 0
        if (total[file] > 0) {
          pct = (covered[file] / total[file]) * 100
        }
        printf "%s: %.1f%%\n", file, pct
      }
      printf "total: %.1f%%\n", (coveredStatements / totalStatements) * 100
    }
  ' "coverage/unit.out" > "${coverage_tmp}"
  grep -v '^total:' "${coverage_tmp}" | sort
  grep '^total:' "${coverage_tmp}"
  rm "${coverage_tmp}"
)
