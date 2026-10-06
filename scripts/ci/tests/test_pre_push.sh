#!/usr/bin/env bash
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
ZERO=0000000000000000000000000000000000000000
ZERO256=0000000000000000000000000000000000000000000000000000000000000000
OTHER=1111111111111111111111111111111111111111

new_sandbox
# The hook runs inside a real scratch repository, because it compares the
# pushed sha with HEAD and checks the tree for tracked changes.
REPO="$SANDBOX/repo"
mkdir -p "$REPO/.githooks" "$REPO/sub"
cp "$REPO_ROOT/.githooks/pre-push" "$REPO/.githooks/pre-push"
git -C "$REPO" init -q
git -C "$REPO" config user.email test@example.com
git -C "$REPO" config user.name test
echo one >"$REPO/tracked.txt"
git -C "$REPO" add tracked.txt
git -C "$REPO" commit -q -m first
HEAD_SHA="$(git -C "$REPO" rev-parse HEAD)"
ROOT_PHYSICAL="$(cd "$REPO" && pwd -P)"

# The fake make records the directory it ran in.
fake_cmd make 'echo "cwd=$(pwd -P)" >>"$CALLS_FILE"; exit "${FAKE_MAKE_EXIT:-0}"'

# push feeds the hook the lines git sends on stdin (local ref, local sha,
# remote ref, remote sha), running it from the directory in $HOOK_CWD.
# shellcheck disable=SC2034 # OUT and STATUS are read by the assert helpers
push() {
  local input="$1"
  set +e
  OUT="$(cd "${HOOK_CWD:-$REPO}" && printf '%b' "$input" | PATH="$FAKE_BIN:$PATH" CALLS_FILE="$CALLS" bash "$REPO/.githooks/pre-push" origin git@example.com:x/y.git 2>&1)"
  STATUS=$?
  set -e
}

push "refs/heads/main $HEAD_SHA refs/heads/main $OTHER\n"
assert_status 0 "push of HEAD to main passes when preflight passes"
assert_call_count "make preflight" 1 "push to main runs preflight"
assert_calls_contain "cwd=$ROOT_PHYSICAL" "runs make from the repo root"

: >"$CALLS"
HOOK_CWD="$REPO/sub" push "refs/heads/main $HEAD_SHA refs/heads/main $OTHER\n"
assert_status 0 "push from a subdirectory passes"
assert_calls_contain "cwd=$ROOT_PHYSICAL" "cds to the repo root from a subdirectory"
assert_calls_lack "cwd=$ROOT_PHYSICAL/sub" "does not run make in the subdirectory"

: >"$CALLS"
push "refs/tags/v1.2.3 $HEAD_SHA refs/tags/v1.2.3 $ZERO\n"
assert_status 0 "tag push of HEAD passes when preflight passes"
assert_call_count "make preflight" 1 "tag push runs preflight"

: >"$CALLS"
push "refs/heads/feature $OTHER refs/heads/feature $ZERO\n"
assert_status 0 "feature push passes"
assert_call_count "make preflight" 0 "feature push skips preflight"

: >"$CALLS"
push "refs/heads/feature $HEAD_SHA refs/heads/main $OTHER\n"
assert_status 0 "HEAD pushed to remote main passes"
assert_call_count "make preflight" 1 "a local branch at HEAD pushed to remote main runs preflight"

: >"$CALLS"
push "(delete) $ZERO refs/heads/main $OTHER\n"
assert_status 0 "deleting main passes"
assert_call_count "make preflight" 0 "deleting main skips preflight"

: >"$CALLS"
push "(delete) $ZERO refs/tags/v1.2.3 $OTHER\n"
assert_call_count "make preflight" 0 "deleting a tag skips preflight"

: >"$CALLS"
push "(delete) $ZERO256 refs/tags/v1.2.3 $OTHER\n"
assert_status 0 "deleting a tag in a SHA-256 repository passes"
assert_call_count "make preflight" 0 "a 64-zero sha is a deletion too"

: >"$CALLS"
push "refs/heads/feature $OTHER refs/heads/feature $ZERO\nrefs/heads/main $HEAD_SHA refs/heads/main $OTHER\n"
assert_call_count "make preflight" 1 "a mixed push runs preflight once"

: >"$CALLS"
FAKE_MAKE_EXIT=1 push "refs/heads/main $HEAD_SHA refs/heads/main $OTHER\n"
assert_status 1 "a failing preflight blocks the push"
assert_out_contains "git push --no-verify" "failure message names the escape hatch"

: >"$CALLS"
push ""
assert_status 0 "an empty push passes"
assert_call_count "make preflight" 0 "an empty push skips preflight"

# Preflight tests the checkout, so it can only vouch for HEAD with a clean tree.
: >"$CALLS"
push "refs/heads/main $OTHER refs/heads/main $HEAD_SHA\n"
assert_status 1 "a gated ref whose sha is not HEAD is refused"
assert_call_count "make preflight" 0 "does not run preflight on the wrong commit"
assert_out_contains "preflight can only vouch for HEAD with a clean tree" "explains the refusal"
assert_out_contains "check out the commit" "tells the user to check out the commit"
assert_out_contains "--no-verify" "mentions --no-verify"

: >"$CALLS"
push "refs/tags/v9.9.9 $OTHER refs/tags/v9.9.9 $ZERO\n"
assert_status 1 "a tag that is not HEAD is refused"

echo two >"$REPO/tracked.txt"
: >"$CALLS"
push "refs/heads/main $HEAD_SHA refs/heads/main $OTHER\n"
assert_status 1 "a dirty tracked file is refused"
assert_call_count "make preflight" 0 "does not run preflight on a dirty tree"
assert_out_contains "preflight can only vouch for HEAD with a clean tree" "explains the dirty-tree refusal"

: >"$CALLS"
push "refs/heads/feature $OTHER refs/heads/feature $ZERO\n"
assert_status 0 "a feature push is fine even with a dirty tree"

git -C "$REPO" checkout -q -- tracked.txt
mkdir -p "$REPO/plans"
echo draft >"$REPO/plans/notes.md"
: >"$CALLS"
push "refs/heads/main $HEAD_SHA refs/heads/main $OTHER\n"
assert_status 0 "an untracked-only change is allowed"
assert_call_count "make preflight" 1 "runs preflight with only untracked files"

# make hooks points core.hooksPath at the checked-in hooks.
out="$(make -n -C "$REPO_ROOT" hooks 2>&1)"
if [[ "$out" == *"git config core.hooksPath .githooks"* ]]; then pass "make hooks sets core.hooksPath"; else fail "make hooks sets core.hooksPath" "$out"; fi

finish
