#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
SCRIPT="$REPO_ROOT/scripts/ci/build-all.sh"

new_sandbox
fake_cmd git 'case "$1" in describe) echo v9.9.9;; rev-parse) echo abc1234;; esac'
fake_cmd go 'if [ "$1" = version ]; then echo "go version go1.27.0 linux/amd64"; exit 0; fi
out=""; while [ $# -gt 0 ]; do [ "$1" = -o ] && out="$2"; shift; done
echo "env GOOS=$GOOS GOARCH=$GOARCH CGO=${CGO_ENABLED:-}" >>"$CALLS_FILE"
mkdir -p "$(dirname "$out")"; : >"$out"'
cd "$SANDBOX"
run_cmd bash "$SCRIPT"
assert_status 0 "builds succeed"
for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
  assert_calls_contain "GOOS=${t%/*} GOARCH=${t#*/}" "builds $t"
done
[ -f dist/ocfp-windows-amd64.exe ] && pass "windows output has .exe" || fail "windows output has .exe"
[ -f dist/ocfp-linux-amd64 ] && pass "linux output has no suffix" || fail "linux output has no suffix"
assert_calls_contain "-X github.com/ocfp/ocfp-cli-go/internal/version.Version=v9.9.9" "stamps the version"
assert_calls_contain "-X github.com/ocfp/ocfp-cli-go/internal/version.GitCommit=abc1234" "stamps the commit"
assert_calls_contain "./cmd/ocfp" "builds ./cmd/ocfp"
assert_calls_contain "CGO=0" "disables cgo"

fake_cmd go 'if [ "$1" = version ]; then echo "go version go1.27.0 linux/amd64"; exit 0; fi; exit 3'
run_cmd bash "$SCRIPT"
assert_status 3 "a failed build fails the script"

finish
