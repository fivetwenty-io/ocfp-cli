#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
SCRIPT="$REPO_ROOT/scripts/ci/check-no-ocfp-dirs.sh"

new_sandbox
HOME_DIR="$SANDBOX/home"
mkdir -p "$HOME_DIR"

# check runs the script against the sandbox home, with HOME, the XDG
# variables, and OCFP_HOME set as the caller passes them.
check() {
  run_cmd env -u XDG_CONFIG_HOME -u XDG_STATE_HOME -u XDG_DATA_HOME -u XDG_CACHE_HOME -u OCFP_HOME \
    HOME="$HOME_DIR" "$@" bash "$SCRIPT" "$HOME_DIR"
}

check
assert_status 0 "passes when no ocfp directory exists"
assert_out_contains "$HOME_DIR/.local/state/ocfp" "names the directories it checked"

for dir in .ocfp .config/ocfp .local/state/ocfp .local/share/ocfp .cache/ocfp; do
  mkdir -p "$HOME_DIR/$dir/mybloc"
  : >"$HOME_DIR/$dir/mybloc/state.yml"
  check
  assert_status 1 "fails when $dir exists"
  assert_out_contains "$HOME_DIR/$dir/mybloc/state.yml" "lists what $dir holds"
  rm -rf "${HOME_DIR:?}/$dir"
done

mkdir -p "$SANDBOX/xdg-state/ocfp"
check XDG_STATE_HOME="$SANDBOX/xdg-state"
assert_status 1 "fails when the XDG state directory holds ocfp"
rm -rf "$SANDBOX/xdg-state"

for var in XDG_CONFIG_HOME XDG_DATA_HOME XDG_CACHE_HOME; do
  mkdir -p "$SANDBOX/$var/ocfp"
  check "$var=$SANDBOX/$var"
  assert_status 1 "fails when $var holds ocfp"
  rm -rf "${SANDBOX:?}/$var"
done

mkdir -p "$SANDBOX/ocfp-home"
check OCFP_HOME="$SANDBOX/ocfp-home"
assert_status 1 "fails when OCFP_HOME exists"
rm -rf "$SANDBOX/ocfp-home"

mkdir -p "$SANDBOX/other-home/.ocfp"
check HOME="$SANDBOX/other-home"
assert_status 1 "checks HOME too when it differs from the home it was given"
rm -rf "$SANDBOX/other-home"

# A file that only contains "ocfp" in its name is not an ocfp directory.
mkdir -p "$HOME_DIR/.config/ocfpx" "$HOME_DIR/.ocfp-old"
check
assert_status 0 "ignores directories that only start with ocfp"

# Without an argument the script checks the home in the passwd entry, which
# a moved HOME can't hide. It only reads, so running it here is safe.
passwd_home="$(perl -e 'print((getpwuid($<))[7])')"
run_cmd env HOME="$HOME_DIR" bash "$SCRIPT"
assert_out_contains "$passwd_home/.local/state/ocfp" "defaults to the passwd home, not HOME"

run_cmd bash "$SCRIPT" "$HOME_DIR" extra
assert_status 2 "two arguments after the home are a usage error"

run_cmd bash "$SCRIPT" --before-tests "$HOME_DIR" extra
assert_status 2 "more arguments than the flag and a home are a usage error"

# Run before the tests, the script blames what it finds on the runner, not
# on a test.
mkdir -p "$HOME_DIR/.local/state/ocfp/leftover"
check
assert_out_contains "A test wrote an ocfp directory" "after the tests, blames a test"
run_cmd env -u XDG_CONFIG_HOME -u XDG_STATE_HOME -u XDG_DATA_HOME -u XDG_CACHE_HOME -u OCFP_HOME \
  HOME="$HOME_DIR" bash "$SCRIPT" --before-tests "$HOME_DIR"
assert_status 1 "fails before the tests when a directory is already there"
assert_out_contains "before the tests ran" "says the directory predates the tests"
assert_out_lacks "A test wrote" "does not blame a test"
assert_out_contains "$HOME_DIR/.local/state/ocfp/leftover" "lists what the leftover holds"
rm -rf "${HOME_DIR:?}/.local"

run_cmd env -u XDG_CONFIG_HOME -u XDG_STATE_HOME -u XDG_DATA_HOME -u XDG_CACHE_HOME -u OCFP_HOME \
  HOME="$HOME_DIR" bash "$SCRIPT" --before-tests "$HOME_DIR"
assert_status 0 "passes before the tests when nothing is there"

finish
