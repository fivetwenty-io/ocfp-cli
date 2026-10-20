#!/usr/bin/env bash
# Runs CI's Security Scan job: govulncheck as the gate, gosec as a report.
# Shared by ci.yml and `make preflight`. Tools install into $GOBIN (default
# $(go env GOPATH)/bin) and run from there, so PATH does not matter.
set -euo pipefail

gobin="${GOBIN:-$(go env GOBIN)}"
if [ -z "$gobin" ]; then
  gobin="$(go env GOPATH)/bin"
fi
export GOBIN="$gobin"

# The real gate: govulncheck reports only advisories on code paths this
# binary reaches, so a finding here is always actionable.
go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
"$gobin/govulncheck" ./...

# Use gosec v2.29.0 through the pinned golangci-lint distribution. Its
# golang.org/x/tools loader supports Go 1.27.3; standalone gosec bundles an
# older loader and silently skips SSA analysis after package-load errors.
# Findings remain advisory, but tool or package-loading failures must fail.
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"
lint_toolchain="$(tr -d '[:space:]' <"$repo_root/.lint-gotoolchain")"
bash "$script_dir/install-golangci-lint.sh" "$repo_root/.golangci-version" "$gobin"
GOTOOLCHAIN="$lint_toolchain" "$gobin/golangci-lint" run --enable-only gosec \
  --issues-exit-code 0 --output.sarif.path gosec-results.sarif ./...
