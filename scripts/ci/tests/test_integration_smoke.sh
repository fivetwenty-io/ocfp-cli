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

# Every directory ocfp resolves must sit in the temporary home, whatever
# the caller's environment named. OCFP_HOME is unset rather than moved, as
# in CI's clean container, so ocfp keeps its XDG layout.
printf '#!/usr/bin/env bash\necho "home=$HOME state=$XDG_STATE_HOME data=$XDG_DATA_HOME cache=$XDG_CACHE_HOME ocfp_home=${OCFP_HOME-unset}" >>"%s"\nexit 0\n' "$CALLS" >"$BIN"
: >"$CALLS"
run_cmd env XDG_STATE_HOME=/real/state XDG_DATA_HOME=/real/data XDG_CACHE_HOME=/real/cache OCFP_HOME=/real/ocfp \
  bash "$SCRIPT" "$BIN"
assert_status 0 "passes when the caller names real ocfp directories"
assert_calls_lack "/real/" "never passes on a directory the caller named"
assert_calls_contain "ocfp_home=unset" "unsets OCFP_HOME"
seen_home="$(sed -n 's/^home=\([^ ]*\) .*/\1/p' "$CALLS" | head -n 1)"
for var in state data cache; do
  if [ -n "$seen_home" ] && grep -qF "$var=$seen_home/" "$CALLS"; then pass "XDG $var home sits under the temporary home"; else fail "XDG $var home sits under the temporary home" "$(cat "$CALLS")"; fi
done

printf '#!/usr/bin/env bash\ncase "$1" in version) exit 2;; esac\nexit 0\n' >"$BIN"
run_cmd bash "$SCRIPT" "$BIN"
assert_status 2 "a failing version command fails the script"

run_cmd bash "$SCRIPT"
assert_status 2 "missing argument is a usage error"

finish
