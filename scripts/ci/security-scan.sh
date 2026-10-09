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
go install golang.org/x/vuln/cmd/govulncheck@v1.7.0
"$gobin/govulncheck" ./...

# gosec reports zero findings: the tree suppresses it with #nosec, which this
# binary and golangci-lint both honour, so the two now agree. Still a report
# rather than a gate: the trailing || true keeps a newly introduced finding
# from blocking a merge, and it surfaces in the SARIF instead. Code scanning
# is unavailable on this private repository, so ci.yml ships the SARIF as an
# artifact instead of an upload.
#
# Pinned like govulncheck above: at @latest this step could change behaviour
# with no commit behind it.
#
# gosec type-checks every package the way golangci-lint does, and under a Go
# release newer than the loader it bundles, every package fails to load and
# the report comes back empty. It runs under golangci-lint's toolchain, from
# .lint-gotoolchain. govulncheck keeps the build toolchain, because the
# standard library it checks must be the one the binary ships with.
lint_toolchain="$(tr -d '[:space:]' <"$(dirname "${BASH_SOURCE[0]}")/../../.lint-gotoolchain")"
go install github.com/securego/gosec/v2/cmd/gosec@v2.29.0
GOTOOLCHAIN="$lint_toolchain" "$gobin/gosec" -fmt sarif -out gosec-results.sarif ./... || true
