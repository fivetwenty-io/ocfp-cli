#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
SCRIPT="$REPO_ROOT/scripts/ci/integration-smoke.sh"

new_sandbox
BIN="$SANDBOX/ocfp-fake"
printf '#!/usr/bin/env bash\necho "ocfp $*" >>"%s"\ncase "$1" in env) exit 1;; esac\nexit 0\n' "$CALLS" >"$BIN"
chmod -x "$BIN"
run_cmd bash "$SCRIPT" "$BIN"
assert_status 0 "passes; env failures are tolerated"
assert_calls_contain "ocfp --help" "runs --help"
assert_calls_contain "ocfp version" "runs version"
assert_calls_contain "ocfp env list" "runs env list"
assert_calls_contain "ocfp env show" "runs env show"

# The binary must see an empty home, as in CI's clean container, never the
# developer's real configuration.
printf '#!/usr/bin/env bash\necho "home=$HOME xdg=$XDG_CONFIG_HOME" >>"%s"\nexit 0\n' "$CALLS" >"$BIN"
: >"$CALLS"
REAL_HOME="$HOME"
run_cmd bash "$SCRIPT" "$BIN"
assert_status 0 "passes with an isolated home"
assert_calls_lack "home=$REAL_HOME " "never runs with the real HOME"
assert_calls_contain "xdg=" "sets XDG_CONFIG_HOME"
seen_home="$(sed -n 's/^home=\([^ ]*\) .*/\1/p' "$CALLS" | head -n 1)"
if [ -n "$seen_home" ] && [ ! -e "$seen_home" ]; then pass "removes the temporary home afterwards"; else fail "removes the temporary home afterwards" "home was '$seen_home'"; fi
if [ -n "$seen_home" ] && [ "$seen_home" != "$REAL_HOME" ] && [[ "$(grep -c "xdg=$seen_home" "$CALLS")" -ge 1 ]]; then pass "XDG_CONFIG_HOME sits under the temporary home"; else fail "XDG_CONFIG_HOME sits under the temporary home"; fi

printf '#!/usr/bin/env bash\ncase "$1" in version) exit 2;; esac\nexit 0\n' >"$BIN"
run_cmd bash "$SCRIPT" "$BIN"
assert_status 2 "a failing version command fails the script"

run_cmd bash "$SCRIPT"
assert_status 2 "missing argument is a usage error"

finish
