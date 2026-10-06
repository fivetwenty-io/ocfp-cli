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
go install github.com/securego/gosec/v2/cmd/gosec@v2.29.0
"$gobin/gosec" -fmt sarif -out gosec-results.sarif ./... || true
