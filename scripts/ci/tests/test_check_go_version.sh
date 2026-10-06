#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
SCRIPT="$REPO_ROOT/scripts/ci/check-go-version.sh"

new_sandbox
printf "      - uses: actions/setup-go@x\n        with:\n          go-version: '"'"'1.27.0'"'"'\n" >"$SANDBOX/ci.yml"
fake_cmd go 'echo go1.27.0'
run_cmd bash "$SCRIPT" "$SANDBOX/ci.yml"
assert_status 0 "matching version passes"
assert_out_lacks "WARNING" "no warning when the version matches"

fake_cmd go 'echo go1.27.1'
run_cmd bash "$SCRIPT" "$SANDBOX/ci.yml"
assert_status 0 "differing version still exits 0"
assert_out_contains "WARNING" "warns when the version differs"
assert_out_contains "go1.27.0" "names CI's version"
assert_out_contains "go1.27.1" "names the local version"

finish
