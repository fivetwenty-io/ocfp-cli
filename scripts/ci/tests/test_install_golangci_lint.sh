#!/usr/bin/env bash
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
SCRIPT="$REPO_ROOT/scripts/ci/install-golangci-lint.sh"

new_sandbox
printf 'v2.13.1\n' >"$SANDBOX/version"
BINDIR="$SANDBOX/.bin"

# The fake curl serves a fake install.sh. Like golangci-lint's real one, it
# takes -b <dir> <version> and drops a golangci-lint that reports <version>.
fake_cmd curl 'cat <<'"'"'INSTALLER'"'"'
echo "install.sh $*" >>"$CALLS_FILE"
dir="$2"; ver="${3#v}"
mkdir -p "$dir"
printf "#!/usr/bin/env bash\necho \"golangci-lint has version %s built with go1.27.0\"\n" "$ver" >"$dir/golangci-lint"
chmod +x "$dir/golangci-lint"
INSTALLER'

run_cmd bash "$SCRIPT" "$SANDBOX/version" "$BINDIR"
assert_status 0 "installs when the bin directory is empty"
assert_calls_contain "curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh" "downloads golangci's install script"
assert_calls_contain "install.sh -b $BINDIR v2.13.1" "installs the pinned version into the bin directory"

: >"$CALLS"
run_cmd bash "$SCRIPT" "$SANDBOX/version" "$BINDIR"
assert_status 0 "an up-to-date binary passes"
assert_calls_lack "install.sh" "skips the install when the version matches"

printf 'v2.14.0\n' >"$SANDBOX/version"
: >"$CALLS"
run_cmd bash "$SCRIPT" "$SANDBOX/version" "$BINDIR"
assert_status 0 "a version bump reinstalls"
assert_calls_contain "install.sh -b $BINDIR v2.14.0" "reinstalls at the new version"

# 2.1.1 must not be mistaken for an installed 2.1.
printf 'v2.1\n' >"$SANDBOX/version"
printf '#!/usr/bin/env bash\necho "golangci-lint has version 2.13.1 built with go1.27.0"\n' >"$BINDIR/golangci-lint"
: >"$CALLS"
run_cmd bash "$SCRIPT" "$SANDBOX/version" "$BINDIR"
assert_calls_contain "install.sh -b $BINDIR v2.1" "compares the whole version, not a prefix"

fake_cmd curl 'exit 22'
rm "$BINDIR/golangci-lint"
run_cmd bash "$SCRIPT" "$SANDBOX/version" "$BINDIR"
assert_status 22 "a failed download fails the install"

run_cmd bash "$SCRIPT" "$SANDBOX/missing" "$BINDIR"
assert_status 2 "a missing version file is a usage error"

finish
