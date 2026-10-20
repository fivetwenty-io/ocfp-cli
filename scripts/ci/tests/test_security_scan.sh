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
# The shared installer recognizes the current binary and skips downloading.
export FAKE_LINT_VERSION="$(tr -d '[:space:]' <"$REPO_ROOT/.golangci-version")"
cat >"$GOBIN/golangci-lint" <<'STUB'
#!/usr/bin/env bash
if [ "$1" = --version ]; then
  echo "golangci-lint has version ${FAKE_LINT_VERSION#v} built with go1.27.3"
  exit 0
fi
echo "golangci-lint $*" >>"$CALLS_FILE"
echo "gosec toolchain=${GOTOOLCHAIN:-}" >>"$CALLS_FILE"
exit "${FAKE_SCAN_EXIT:-0}"
STUB
chmod +x "$GOBIN/golangci-lint"
cd "$SANDBOX"
run_cmd bash "$SCRIPT"
assert_status 0 "clean scan passes"
assert_calls_contain "golang.org/x/vuln/cmd/govulncheck@v1.9.0" "installs pinned govulncheck"
assert_calls_lack "github.com/securego/gosec" "does not install the incompatible standalone scanner"
assert_calls_contain "govulncheck ./..." "runs govulncheck"
assert_calls_contain "golangci-lint run --enable-only gosec" "runs gosec through the current package loader"
assert_calls_contain "--output.sarif.path gosec-results.sarif" "writes the security report"
assert_calls_contain "--issues-exit-code 0" "findings remain advisory"
lint_toolchain="$(tr -d '[:space:]' <"$REPO_ROOT/.lint-gotoolchain")"
assert_calls_contain "gosec toolchain=$lint_toolchain" "runs gosec under the lint toolchain"
assert_calls_lack "govulncheck toolchain=$lint_toolchain" "runs govulncheck under the build toolchain"

: >"$CALLS"
FAKE_EXIT='exit 1' run_cmd bash "$SCRIPT"
assert_status 1 "a govulncheck finding fails the scan"
assert_calls_lack "golangci-lint run" "gosec does not run after a govulncheck failure"

: >"$CALLS"
FAKE_SCAN_EXIT=3 run_cmd bash "$SCRIPT"
assert_status 3 "a scanner loading failure fails the scan"
assert_calls_contain "golangci-lint run" "attempts the advisory scan"

finish
