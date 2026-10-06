#!/usr/bin/env bash
# Warns, without failing, when the local Go toolchain is not the version CI
# sets up. Usage: check-go-version.sh [workflow-file]
set -euo pipefail

workflow="${1:-$(dirname "${BASH_SOURCE[0]}")/../../.github/workflows/ci.yml}"
ci_version="go$(grep -m1 -E "^[[:space:]]*go-version:" "$workflow" | tr -d "'\"" | sed 's/.*go-version:[[:space:]]*//' | tr -d '[:space:]')"
local_version="$(go env GOVERSION)"

if [ "$ci_version" = "go" ]; then
  echo "WARNING: could not read CI's Go version from $workflow" >&2
elif [ "$local_version" != "$ci_version" ]; then
  echo "WARNING: local Go is ${local_version} but CI uses ${ci_version}; results may differ from CI" >&2
fi
