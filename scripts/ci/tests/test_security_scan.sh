#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
SCRIPT="$REPO_ROOT/scripts/ci/security-scan.sh"

new_sandbox
export GOBIN="$SANDBOX/gobin"
mkdir -p "$GOBIN"
fake_cmd go 'case "$1" in
  install) echo "installed $2" >>"$CALLS_FILE"
    name="${2##*/}"; name="${name%@*}"
    printf "#!/usr/bin/env bash\necho \"%s \$*\" >>\"%s\"\necho \"%s toolchain=\${GOTOOLCHAIN:-}\" >>\"%s\"\n%s\n" "$name" "$CALLS_FILE" "$name" "$CALLS_FILE" "${FAKE_EXIT:-exit 0}" >"$GOBIN/$name"
    chmod +x "$GOBIN/$name";;
esac'
cd "$SANDBOX"
run_cmd bash "$SCRIPT"
assert_status 0 "clean scan passes"
assert_calls_contain "golang.org/x/vuln/cmd/govulncheck@v1.7.0" "installs pinned govulncheck"
assert_calls_contain "github.com/securego/gosec/v2/cmd/gosec@v2.29.0" "installs pinned gosec"
assert_calls_contain "govulncheck ./..." "runs govulncheck"
assert_calls_contain "gosec -fmt sarif -out gosec-results.sarif ./..." "runs gosec with sarif output"
lint_toolchain="$(tr -d '[:space:]' <"$REPO_ROOT/.lint-gotoolchain")"
assert_calls_contain "gosec toolchain=$lint_toolchain" "runs gosec under the lint toolchain"
assert_calls_lack "govulncheck toolchain=$lint_toolchain" "runs govulncheck under the build toolchain"

: >"$CALLS"
FAKE_EXIT='exit 1' run_cmd bash "$SCRIPT"
assert_status 1 "a govulncheck finding fails the scan"
assert_calls_lack "gosec -fmt" "gosec does not run after a govulncheck failure"

: >"$CALLS"
fake_cmd go 'case "$1" in
  install) name="${2##*/}"; name="${name%@*}"
    if [ "$name" = gosec ]; then body="exit 1"; else body="exit 0"; fi
    printf "#!/usr/bin/env bash\necho \"%s \$*\" >>\"%s\"\n%s\n" "$name" "$CALLS_FILE" "$body" >"$GOBIN/$name"
    chmod +x "$GOBIN/$name";;
esac'
run_cmd bash "$SCRIPT"
assert_status 0 "a gosec finding is a report, not a gate"

finish
