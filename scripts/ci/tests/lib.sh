#!/usr/bin/env bash
# Shared helpers for the script tests. Each test file sources this, builds a
# scratch directory of fake commands, puts it first on PATH, and runs the
# script under test against it.

TESTS_RUN=0
TESTS_FAILED=0
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
export REPO_ROOT

# The tests can run under a git hook, where git exports GIT_DIR and friends.
# Clear them so each test's git commands act on its own scratch repository.
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_PREFIX GIT_COMMON_DIR

# new_sandbox creates a scratch dir with a bin/ for fakes, and sets SANDBOX.
new_sandbox() {
  SANDBOX="$(mktemp -d)"
  mkdir -p "$SANDBOX/bin"
  FAKE_BIN="$SANDBOX/bin"
  CALLS="$SANDBOX/calls.log"
  : >"$CALLS"
}

cleanup_sandbox() {
  [ -n "${SANDBOX:-}" ] && rm -rf "$SANDBOX"
}

# fake_cmd NAME BODY writes an executable fake into the sandbox bin/. The
# body runs under bash after a line that appends the command line to $CALLS.
fake_cmd() {
  local name="$1" body="$2"
  {
    echo '#!/usr/bin/env bash'
    # shellcheck disable=SC2016
    echo 'echo "'"$name"' $*" >>"$CALLS_FILE"'
    echo "$body"
  } >"$FAKE_BIN/$name"
  chmod +x "$FAKE_BIN/$name"
}

# run_cmd runs a command with the fakes first on PATH, capturing combined
# output in $OUT and the exit status in $STATUS.
run_cmd() {
  set +e
  OUT="$(PATH="$FAKE_BIN:$PATH" CALLS_FILE="$CALLS" SANDBOX="$SANDBOX" "$@" 2>&1)"
  STATUS=$?
  set -e
}

pass() { TESTS_RUN=$((TESTS_RUN + 1)); echo "  ok   $1"; }
fail() {
  TESTS_RUN=$((TESTS_RUN + 1))
  TESTS_FAILED=$((TESTS_FAILED + 1))
  echo "  FAIL $1" >&2
  [ -n "${2:-}" ] && echo "       $2" >&2
  return 0
}

assert_status() {
  local want="$1" name="$2"
  if [ "$STATUS" -eq "$want" ]; then pass "$name"; else fail "$name" "exit $STATUS, want $want; output: $OUT"; fi
}

assert_out_contains() {
  local needle="$1" name="$2"
  if [[ "$OUT" == *"$needle"* ]]; then pass "$name"; else fail "$name" "output lacks '$needle': $OUT"; fi
}

assert_out_lacks() {
  local needle="$1" name="$2"
  if [[ "$OUT" != *"$needle"* ]]; then pass "$name"; else fail "$name" "output has '$needle'"; fi
}

assert_calls_contain() {
  local needle="$1" name="$2"
  if grep -qF -- "$needle" "$CALLS"; then pass "$name"; else fail "$name" "calls lack '$needle': $(cat "$CALLS")"; fi
}

assert_calls_lack() {
  local needle="$1" name="$2"
  if ! grep -qF -- "$needle" "$CALLS"; then pass "$name"; else fail "$name" "calls have '$needle'"; fi
}

assert_call_count() {
  local needle="$1" want="$2" name="$3" got
  got="$(grep -cF -- "$needle" "$CALLS" || true)"
  if [ "$got" -eq "$want" ]; then pass "$name"; else fail "$name" "'$needle' ran $got times, want $want"; fi
}

finish() {
  cleanup_sandbox
  echo "$TESTS_RUN checks, $TESTS_FAILED failed"
  [ "$TESTS_FAILED" -eq 0 ]
}
