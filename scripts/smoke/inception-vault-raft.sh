#!/usr/bin/env bash
# inception-vault-raft.sh — workstation smoke test for raft-backed inception
# vaults.
#
# It runs only on an operator workstation, against throwaway blocs, a scratch
# HOME, scratch ocfp homes, a scratch tmux server, and scratch ports. It never
# touches a real bloc, the real ~/.saferc, or a lab, and CI does not run it.
#
# It builds safe from the safe repository and ocfp from this branch, and it
# builds the previous ocfp release to create file-backed vaults exactly the way
# that release did. The OpenBao 2.6.4 binary those vaults need is used strictly
# read-only, which the script proves by hashing it before and after.
#
# It never prints a token, an unseal key, or a secret value. It compares
# hashes instead, and it writes each ocfp run's output to a log under the
# scratch directory rather than to the terminal.
#
# Usage, from the ocfp repository:
#
#   SAFE_REPO=../safe scripts/smoke/inception-vault-raft.sh
#
# Variables:
#
#   SAFE_REPO   the safe repository to build safe from (default ../safe)
#   SAFE_BIN    a prebuilt safe v1.25.0 or later, used instead of building
#   OLD_REF     the ocfp release that creates file vaults (default v0.3.7)
#   BAO26       the OpenBao 2.6.4 binary, used read-only
#               (default ~/.local/share/ocfp/bin/openbao-2.6.4/bao)
#   BAO_NEW     the current OpenBao binary (default: bao on PATH)
#   PORT_BASE   the first scratch API port (default 28234); each case adds
#               a small offset, and each cluster port is its API port + 1000
#
# The script prints PASS or FAIL per case, then the scratch directory, which
# it keeps for inspection. Remove it by hand when done.

set -euo pipefail

# --- 0. Isolation and preflight ---------------------------------------------

OCFP_REPO=$(cd "$(dirname "$0")/../.." && pwd -P)
REAL_HOME=$HOME
SAFE_REPO=${SAFE_REPO:-$OCFP_REPO/../safe}
OLD_REF=${OLD_REF:-v0.3.7}
BAO26=${BAO26:-$REAL_HOME/.local/share/ocfp/bin/openbao-2.6.4/bao}
BAO_NEW=${BAO_NEW:-$(command -v bao || true)}
PORT_BASE=${PORT_BASE:-28234}

die() {
  printf 'FATAL: %s\n' "$*" >&2
  exit 1
}

[[ -x $BAO26 ]] || die "OpenBao 2.6.4 not found at $BAO26 (set BAO26)"
[[ -n $BAO_NEW && -x $BAO_NEW ]] || die "no current bao on PATH (set BAO_NEW)"
[[ $(cd "$(dirname "$BAO_NEW")" && pwd -P) != $(cd "$(dirname "$BAO26")" && pwd -P) ]] ||
  die "BAO_NEW and BAO26 sit in one directory; the cases need them apart"

for tool in go git tmux lsof curl python3 shasum; do
  command -v "$tool" >/dev/null 2>&1 || die "required tool not found on PATH: $tool"
done

BAO26_SHA=$(shasum -a 256 "$BAO26" | awk '{print $1}')
BAO26_LS=$(ls -la "$(dirname "$BAO26")")

# The tmux socket lives under TMUX_TMPDIR, and macOS caps a socket path at
# 104 bytes, so the scratch root goes under /tmp rather than a long $TMPDIR.
S=$(mktemp -d /tmp/ocfp-raft-smoke.XXXXXX)
S=$(cd "$S" && pwd -P)

for real in "$REAL_HOME/.config/ocfp" "$REAL_HOME/.local/share/ocfp" "$REAL_HOME/.local/state/ocfp" "$REAL_HOME"; do
  case "$S/" in
    "$real"/*) die "scratch root $S sits under $real" ;;
  esac
done

mkdir -p "$S/home" "$S/ocfp" "$S/tmux" "$S/bin" "$S/bin-old" "$S/logs" "$S/aside"
chmod 700 "$S" "$S/home" "$S/ocfp" "$S/tmux" "$S/aside"

BASE_PATH=/usr/bin:/bin:/usr/sbin:/sbin
for dir in /opt/homebrew/bin /usr/local/bin; do
  [[ -d $dir ]] && BASE_PATH=$BASE_PATH:$dir
done

printf 'Building into %s\n' "$S"

if [[ -n ${SAFE_BIN:-} ]]; then
  cp "$SAFE_BIN" "$S/bin/safe"
else
  (cd "$SAFE_REPO" && go build -o "$S/bin/safe" ./cmd/safe) || die "safe build failed in $SAFE_REPO"
fi

(cd "$OCFP_REPO" && go build -o "$S/bin/ocfp" ./cmd/ocfp) || die "ocfp build failed"

mkdir -p "$S/src-old"
(cd "$OCFP_REPO" && git archive "$OLD_REF" | tar -x -C "$S/src-old") || die "git archive $OLD_REF failed"
(cd "$S/src-old" && go build -o "$S/bin-old/ocfp" ./cmd/ocfp) || die "ocfp $OLD_REF build failed"

export HOME=$S/home OCFP_HOME=$S/ocfp TMUX_TMPDIR=$S/tmux
unset TMUX VAULT_ADDR VAULT_TOKEN SAFE_TARGET XDG_CONFIG_HOME XDG_DATA_HOME XDG_STATE_HOME

for check in "$HOME" "$OCFP_HOME"; do
  case "$check/" in
    "$REAL_HOME/.config/ocfp"/* | "$REAL_HOME/.local/share/ocfp"/* | "$REAL_HOME/.local/state/ocfp"/*)
      die "$check resolves under a real ocfp home"
      ;;
  esac
  [[ $check != "$REAL_HOME" ]] || die "$check is the real HOME"
done

# api_port BLOC prints the scratch API port of a test bloc.
api_port() {
  case "$1" in
    smoke-a) echo $((PORT_BASE + 0)) ;;
    smoke-c) echo $((PORT_BASE + 2)) ;;
    smoke-d) echo $((PORT_BASE + 3)) ;;
    smoke-e) echo $((PORT_BASE + 4)) ;;
    smoke-f) echo $((PORT_BASE + 5)) ;;
    smoke-g) echo $((PORT_BASE + 6)) ;;
    smoke-a-moved) echo $((PORT_BASE + 7)) ;;
    smoke-h) echo $((PORT_BASE + 8)) ;;
    *) die "no port for bloc $1" ;;
  esac
}

cluster_port() { echo $(($(api_port "$1") + 1000)); }

ALL_PORTS=""
for bloc in smoke-a smoke-c smoke-d smoke-e smoke-f smoke-g smoke-a-moved smoke-h; do
  ALL_PORTS="$ALL_PORTS $(api_port "$bloc") $(cluster_port "$bloc")"
done

listeners() { lsof -nP -t -iTCP:"$1" -sTCP:LISTEN 2>/dev/null || true; }

for port in $ALL_PORTS; do
  [[ -z $(listeners "$port") ]] || die "scratch port $port is already in use; set PORT_BASE"
done

# cleanup stops only what this script started: the scratch tmux server and
# listeners on the scratch ports, which were free when the script began.
cleanup() {
  tmux kill-server >/dev/null 2>&1 || true
  for port in $ALL_PORTS; do
    for pid in $(listeners "$port"); do
      kill -9 "$pid" 2>/dev/null || true
    done
  done
  printf 'Scratch directory kept for inspection: %s\n' "$S"
}
trap cleanup EXIT

# --- Helpers ------------------------------------------------------------------

RESULTS=""
FAILED=0
CASE=""

begin() {
  CASE=$1
  printf '\n=== %s ===\n' "$CASE"
}

fail() {
  printf 'FAIL [%s]: %s\n' "$CASE" "$*"
  RESULTS="$RESULTS
FAIL  $CASE: $*"
  FAILED=1
  return 1
}

pass() {
  printf 'PASS [%s]\n' "$CASE"
  RESULTS="$RESULTS
PASS  $CASE"
}

# run_case NAME FUNCTION runs one case and records a failure without
# stopping the others.
run_case() {
  begin "$1"
  if "$2"; then
    pass
  fi
}

vault_root() { echo "$OCFP_HOME/$1/vault"; }
data_dir() { echo "$(vault_root "$1")/data"; }
vault_log() { echo "$OCFP_HOME/$1/logs/vault/vault-inception.log"; }

# new_ocfp BLOC runs this branch's ocfp with the current engine. Its output
# goes to a log in the scratch directory, never to the terminal.
new_ocfp() {
  local bloc=$1 port=${2:-}
  [[ -n $port ]] || port=$(api_port "$bloc")
  OCFP_VAULT_INCEPTION_PORT=$port SAFE_ENGINE=bao \
    PATH="$S/bin:$(dirname "$BAO_NEW"):$BASE_PATH" \
    "$S/bin/ocfp" vault inception --bloc "$bloc" >>"$S/logs/$CASE.log" 2>&1
}

# new_ocfp_vault BLOC SUBCOMMAND [ARGS...] runs another vault subcommand of
# this branch's ocfp the way new_ocfp runs inception.
new_ocfp_vault() {
  local bloc=$1
  shift
  OCFP_VAULT_INCEPTION_PORT=$(api_port "$bloc") SAFE_ENGINE=bao \
    PATH="$S/bin:$(dirname "$BAO_NEW"):$BASE_PATH" \
    "$S/bin/ocfp" vault "$@" --bloc "$bloc" >>"$S/logs/$CASE.log" 2>&1
}

# old_ocfp BLOC creates a file-backed vault with the previous release and the
# 2.6.4 engine.
#
# That release types a bare `safe local` into a new tmux session, so the
# pane's shell, not ocfp's environment, decides which safe and which engine
# run. The pane inherits the environment of whichever run started the
# scratch tmux server, and a macOS login shell then runs path_helper, which
# puts Homebrew's safe and bao ahead of everything else. Homebrew's bao no
# longer serves file storage, so the old release's vault never started.
#
# While the old release runs, new sessions therefore start a plain shell with
# the PATH that release was used with. That release also knew nothing of
# SAFE_ENGINE and required a `vault` command, which the 2.6.4 directory
# provides as a link to bao, so SAFE_ENGINE is unset and safe picks `vault`
# as it did then.
old_ocfp() {
  local bloc=$1 old_path rc=0
  old_path="$S/bin:$(dirname "$BAO26"):$BASE_PATH"
  tmux has-session -t smoke-hold 2>/dev/null || tmux new-session -d -s smoke-hold 'sleep 86400'
  tmux set-option -g default-command "exec env -u SAFE_ENGINE PATH=$(printf '%q' "$old_path") /bin/sh"
  env -u SAFE_ENGINE OCFP_VAULT_INCEPTION_PORT="$(api_port "$bloc")" PATH="$old_path" \
    "$S/bin-old/ocfp" vault inception --bloc "$bloc" >>"$S/logs/$CASE.log" 2>&1 || rc=$?
  tmux set-option -gu default-command
  return "$rc"
}

# scratch_safe runs safe against the scratch HOME's .saferc.
scratch_safe() { PATH="$S/bin:$(dirname "$BAO_NEW"):$BASE_PATH" "$S/bin/safe" "$@"; }

storage_type() {
  curl -s --max-time 5 "http://127.0.0.1:$1/v1/sys/seal-status" |
    python3 -c 'import json, sys; print(json.load(sys.stdin).get("storage_type", ""))' 2>/dev/null || true
}

key_sha() {
  local root
  root=$(vault_root "$1")
  cat "$root/root.key" "$root/unseal.keys" 2>/dev/null | shasum -a 256 | awk '{print $1}'
}

# put_canary BLOC stores a random value through safe, which reads it from a
# file, so the value never reaches a command line.
put_canary() {
  local bloc=$1
  head -c 24 /dev/urandom | base64 >"$S/canary.$bloc"
  chmod 600 "$S/canary.$bloc"
  scratch_safe -T "$bloc-inception" set secret/smoke "canary@$S/canary.$bloc" >/dev/null 2>&1 ||
    fail "could not write the canary to $bloc" || return 1
}

# check_canary BLOC compares hashes of the stored and the read-back value and
# prints only whether they match.
check_canary() {
  local bloc=$1 want got
  want=$(tr -d '\n' <"$S/canary.$bloc" | shasum -a 256 | awk '{print $1}')
  got=$(scratch_safe -T "$bloc-inception" get secret/smoke:canary 2>/dev/null | tr -d '\n' | shasum -a 256 | awk '{print $1}')
  [[ $want == "$got" ]] || fail "canary mismatch in $bloc"
}

# no_token_in_argv BLOC checks that no process has the root token on its
# command line. grep reads the token from root.key, so it is not on grep's.
no_token_in_argv() {
  local root
  root=$(vault_root "$1")/root.key
  [[ -s $root ]] || return 0
  if ps -axww -o args= | grep -qF -f "$root"; then
    fail "the root token of $1 appears in a process's arguments"
  fi
}

assert_no_archive() {
  if ls -d "$OCFP_HOME/$1/vault.superseded-"* >/dev/null 2>&1; then
    fail "$1 was archived"
  fi
}

archive_of() { ls -d "$OCFP_HOME/$1/vault.superseded-"* 2>/dev/null | head -n 1; }

# expect_raft BLOC checks a running, raft-backed, untouched-key vault.
expect_raft() {
  local bloc=$1 port=${2:-}
  [[ -n $port ]] || port=$(api_port "$bloc")
  [[ -f $(data_dir "$bloc")/vault.db ]] || fail "$bloc has no data/vault.db" || return 1
  [[ -d $(data_dir "$bloc")/raft ]] || fail "$bloc has no data/raft" || return 1
  [[ $(storage_type "$port") == raft ]] || fail "$bloc does not report raft storage"
}

# wait_port_closed PORT SECONDS
wait_port_closed() {
  local i
  for ((i = 0; i < $2; i++)); do
    [[ -z $(listeners "$1") ]] && return 0
    sleep 1
  done
  return 1
}

# stop_vault BLOC stops a vault the way an operator would, and waits for it.
stop_vault() {
  local port
  port=$(api_port "$1")
  tmux kill-session -t "$1-inception-vault" >/dev/null 2>&1 || true
  if ! wait_port_closed "$port" 15; then
    for pid in $(listeners "$port"); do kill "$pid" 2>/dev/null || true; done
    wait_port_closed "$port" 15 || fail "$1 would not stop"
  fi
}

tree_sha() { (cd "$1" && find . -type f -exec shasum -a 256 {} + | sort) | shasum -a 256 | awk '{print $1}'; }

# --- A. Fresh raft start --------------------------------------------------------

case_a() {
  new_ocfp smoke-a || fail "ocfp vault inception failed; see $S/logs/$CASE.log" || return 1
  expect_raft smoke-a || return 1
  [[ -n $(listeners "$(cluster_port smoke-a)") ]] || fail "nothing listens on the cluster port" || return 1
  for key in root.key unseal.keys; do
    [[ $(stat -f %Lp "$(vault_root smoke-a)/$key") == 600 ]] || fail "$key is not mode 0600" || return 1
  done
  no_token_in_argv smoke-a || return 1
  put_canary smoke-a || return 1
  check_canary smoke-a
}

# --- B. Kill and restart in place -----------------------------------------------

restart_and_check() {
  local keys=$1 inode=$2
  new_ocfp smoke-a || fail "restart failed; see $S/logs/$CASE.log" || return 1
  [[ $(key_sha smoke-a) == "$keys" ]] || fail "the keys changed" || return 1
  [[ $(stat -f %i "$(data_dir smoke-a)/vault.db") == "$inode" ]] || fail "vault.db was replaced" || return 1
  expect_raft smoke-a || return 1
  check_canary smoke-a || return 1
  assert_no_archive smoke-a || return 1
  no_token_in_argv smoke-a
}

case_b1() {
  local keys inode port
  keys=$(key_sha smoke-a)
  inode=$(stat -f %i "$(data_dir smoke-a)/vault.db")
  port=$(api_port smoke-a)
  for pid in $(pgrep -f "safe local.*--port $port" || true) $(listeners "$port"); do
    kill -9 "$pid" 2>/dev/null || true
  done
  wait_port_closed "$port" 10 || fail "the engine survived kill -9" || return 1
  restart_and_check "$keys" "$inode"
}

case_b2() {
  local keys inode port
  keys=$(key_sha smoke-a)
  inode=$(stat -f %i "$(data_dir smoke-a)/vault.db")
  port=$(api_port smoke-a)
  tmux kill-session -t smoke-a-inception-vault
  wait_port_closed "$port" 15 || fail "the engine outlived its tmux session, so safe ignored SIGHUP" || return 1
  restart_and_check "$keys" "$inode"
}

# B3 records whether a single raft node reopens with a new cluster address.
# A failure here means ocfp must record the cluster port per bloc.
case_b3() {
  local moved keys
  moved=$(api_port smoke-a-moved)
  keys=$(key_sha smoke-a)
  stop_vault smoke-a || return 1
  new_ocfp smoke-a "$moved" || fail "raft did not reopen after the cluster port moved; see $S/logs/$CASE.log" || return 1
  [[ $(key_sha smoke-a) == "$keys" ]] || fail "the keys changed, so the vault was not reopened" || return 1
  assert_no_archive smoke-a || return 1
  expect_raft smoke-a "$moved" || return 1
  check_canary smoke-a || return 1
  tmux kill-session -t smoke-a-inception-vault >/dev/null 2>&1 || true
  wait_port_closed "$moved" 15 || fail "smoke-a would not stop on the moved port"
}

# --- C. File-to-raft migration ----------------------------------------------------

# save_keys BLOC copies the key files the old release wrote into the scratch
# aside directory, so keys_kept can compare them later without printing them.
save_keys() {
  local root aside=$S/aside/$1.keys
  root=$(vault_root "$1")
  [[ -s $root/root.key && -s $root/unseal.keys ]] || fail "ocfp $OLD_REF did not save both keys of $1" || return 1
  mkdir -m 700 "$aside"
  cp -p "$root/root.key" "$root/unseal.keys" "$aside/"
}

# keys_kept BLOC checks that the root token is byte for byte the one the old
# release saved, and that the unseal key is either the same or the whole key
# that the old file held only the start of. The old release read the key
# from an 80-column tmux pane, which cut it short, and ocfp restores the
# whole key from the log before it migrates. It prints only which it was.
keys_kept() {
  local aside=$S/aside/$1.keys root
  root=$(vault_root "$1")
  cmp -s "$aside/root.key" "$root/root.key" || return 1
  cmp -s "$aside/unseal.keys" "$root/unseal.keys" && return 0
  python3 - "$aside/unseal.keys" "$root/unseal.keys" <<'PY' || return 1
import re, sys
old = open(sys.argv[1]).read().strip()
new = open(sys.argv[2]).read().strip()
sys.exit(0 if old and re.fullmatch(r"[0-9a-fA-F]{64}", new) and new.startswith(old) else 1)
PY
  printf '  note: %s had a cut-short unseal key from %s, and ocfp restored the whole key\n' "$1" "$OLD_REF"
}

make_file_vault() {
  local bloc=$1
  old_ocfp "$bloc" || fail "ocfp $OLD_REF could not create $bloc; see $S/logs/$CASE.log" || return 1
  [[ -d $(data_dir "$bloc")/core ]] || fail "$bloc is not file-backed" || return 1
  save_keys "$bloc" || return 1
  put_canary "$bloc"
}

expect_migrated() {
  local bloc=$1 backup
  backup=$(ls -d "$(data_dir "$bloc").file-backup-"* 2>/dev/null | head -n 1)
  [[ -n $backup && -d $backup/core ]] || fail "$bloc has no file backup with core/" || return 1
  expect_raft "$bloc" || return 1
  keys_kept "$bloc" || fail "the keys of $bloc changed" || return 1
  check_canary "$bloc" || return 1
  assert_no_archive "$bloc" || return 1
  [[ ! -e $(data_dir "$bloc").raft-migration.json ]] || fail "a journal was left behind for $bloc" || return 1
  no_token_in_argv "$bloc"
}

case_c1() {
  local old_pid
  make_file_vault smoke-c || return 1
  old_pid=$(listeners "$(api_port smoke-c)")
  [[ -n $old_pid ]] || fail "the old vault is not running" || return 1
  new_ocfp smoke-c || fail "migration of a running vault failed; see $S/logs/$CASE.log" || return 1
  for pid in $old_pid; do
    if kill -0 "$pid" 2>/dev/null; then
      fail "the 2.6.4 engine is still running"
      return 1
    fi
  done
  expect_migrated smoke-c
}

case_c2() {
  make_file_vault smoke-d || return 1
  stop_vault smoke-d || return 1
  new_ocfp smoke-d || fail "migration of a stopped vault failed; see $S/logs/$CASE.log" || return 1
  expect_migrated smoke-d
}

case_c3() {
  local data
  make_file_vault smoke-e || return 1
  stop_vault smoke-e || return 1
  data=$(data_dir smoke-e)
  mkdir -m 700 "$data.raft-migrating"
  echo junk >"$data.raft-migrating/junk"
  printf '{"phase":"migrating","started":"2026-01-01T00:00:00Z","backup":"%s"}\n' \
    "$data.file-backup-20260101-000000" >"$data.raft-migration.json"
  chmod 600 "$data.raft-migration.json"
  new_ocfp smoke-e || fail "resuming the migration failed; see $S/logs/$CASE.log" || return 1
  ls -d "$data.raft-partial-"* >/dev/null 2>&1 || fail "the partial copy was not moved aside" || return 1
  expect_migrated smoke-e
}

# --- D. Fallbacks and refusals ----------------------------------------------------

case_d1() {
  local archive
  new_ocfp smoke-f || fail "fresh start failed; see $S/logs/$CASE.log" || return 1
  put_canary smoke-f || return 1
  stop_vault smoke-f || return 1
  mv "$(vault_root smoke-f)/unseal.keys" "$S/aside/smoke-f.unseal.keys" ||
    fail "the fresh start left no unseal.keys to move aside" || return 1
  new_ocfp smoke-f || fail "the run after losing a key failed; see $S/logs/$CASE.log" || return 1
  [[ -z $(archive_of smoke-f) ]] || fail "a key the vault log still held led to an archive" || return 1
  [[ -s $(vault_root smoke-f)/unseal.keys ]] || fail "the unseal key was not restored from the vault log" || return 1
  check_canary smoke-f || return 1
  stop_vault smoke-f || return 1
  mv "$(vault_root smoke-f)/unseal.keys" "$S/aside/smoke-f.unseal.keys.restored" ||
    fail "the restored unseal.keys could not be moved aside" || return 1
  mv "$(dirname "$(vault_log smoke-f)")" "$S/aside/smoke-f.logs" ||
    fail "the vault logs could not be moved aside" || return 1
  new_ocfp smoke-f || fail "the run after losing the key and its logs failed; see $S/logs/$CASE.log" || return 1
  archive=$(archive_of smoke-f)
  [[ -n $archive && -f $archive/data/vault.db ]] || fail "the archive does not hold the old data" || return 1
  expect_raft smoke-f || return 1
  put_canary smoke-f || return 1
  check_canary smoke-f
}

case_d2() {
  local archive logs keys rc=0
  new_ocfp smoke-g || fail "fresh start failed; see $S/logs/$CASE.log" || return 1
  stop_vault smoke-g || return 1
  [[ -s $(vault_root smoke-g)/root.key && -s $(vault_root smoke-g)/unseal.keys ]] ||
    fail "the fresh start did not save both keys" || return 1
  head -c 24 /dev/urandom | base64 >"$(vault_root smoke-g)/root.key"
  # Without the target's token there is nothing to retry with, so the
  # engine's refusal of root.key is final.
  scratch_safe target delete smoke-g-inception >/dev/null 2>&1 || true
  keys=$(key_sha smoke-g)
  new_ocfp smoke-g || rc=$?
  [[ $rc != 0 ]] || fail "ocfp accepted a vault whose token the engine refused" || return 1
  logs="$(vault_log smoke-g) $(vault_log smoke-g).previous"
  # shellcheck disable=SC2086 # two log paths, split on purpose
  grep -q 'was rejected' $logs 2>/dev/null || fail "safe did not report the rejected token" || return 1
  # shellcheck disable=SC2086
  if grep -qi 'generate-root' $logs 2>/dev/null; then
    fail "safe tried generate-root with a wrong token"
    return 1
  fi
  grep -q "'ocfp vault teardown' and then" "$S/logs/$CASE.log" || fail "the refusal does not say how to replace the vault" || return 1
  assert_no_archive smoke-g || return 1
  # The engine ran on the data before it refused the token, so raft may have
  # written to it; what matters is that it is still there, in place.
  [[ -f $(data_dir smoke-g)/vault.db ]] || fail "the refusal moved the data" || return 1
  [[ $(key_sha smoke-g) == "$keys" ]] || fail "the refusal changed the keys" || return 1
  new_ocfp_vault smoke-g teardown || fail "teardown failed; see $S/logs/$CASE.log" || return 1
  new_ocfp smoke-g || fail "the run after teardown failed; see $S/logs/$CASE.log" || return 1
  archive=$(archive_of smoke-g)
  [[ -n $archive && -f $archive/data/vault.db ]] || fail "the archive does not hold the old data" || return 1
  expect_raft smoke-g
}

case_d3() {
  local before holder rc=0
  new_ocfp smoke-h || fail "fresh start failed; see $S/logs/$CASE.log" || return 1
  stop_vault smoke-h || return 1
  before=$(tree_sha "$(vault_root smoke-h)")
  python3 -m http.server --bind 127.0.0.1 "$(cluster_port smoke-h)" >/dev/null 2>&1 &
  holder=$!
  sleep 1
  new_ocfp smoke-h || rc=$?
  kill "$holder" 2>/dev/null || true
  wait "$holder" 2>/dev/null || true
  [[ $rc != 0 ]] || fail "ocfp started with the cluster port taken" || return 1
  grep -q "$(cluster_port smoke-h)" "$S/logs/$CASE.log" || fail "the error does not name the cluster port" || return 1
  [[ $(tree_sha "$(vault_root smoke-h)") == "$before" ]] || fail "the refusal changed the disk"
}

case_d4() {
  local before rc=0 fake=$S/fake-safe
  mkdir -p "$fake"
  printf '#!/bin/sh\necho "safe v1.24.0"\n' >"$fake/safe"
  chmod 755 "$fake/safe"
  before=$(tree_sha "$(vault_root smoke-h)")
  OCFP_VAULT_INCEPTION_PORT=$(api_port smoke-h) SAFE_ENGINE=bao \
    PATH="$fake:$S/bin:$(dirname "$BAO_NEW"):$BASE_PATH" \
    "$S/bin/ocfp" vault inception --bloc smoke-h >>"$S/logs/$CASE.log" 2>&1 || rc=$?
  [[ $rc != 0 ]] || fail "ocfp ran with safe v1.24.0" || return 1
  grep -q 'too old' "$S/logs/$CASE.log" || fail "no upgrade message" || return 1
  [[ $(tree_sha "$(vault_root smoke-h)") == "$before" ]] || fail "the refusal changed the disk"
}

# --- E. Teardown and read-only proof ------------------------------------------------

case_e() {
  tmux kill-server >/dev/null 2>&1 || true
  for port in $ALL_PORTS; do
    for pid in $(listeners "$port"); do kill "$pid" 2>/dev/null || true; done
  done
  [[ $(shasum -a 256 "$BAO26" | awk '{print $1}') == "$BAO26_SHA" ]] || fail "the 2.6.4 binary changed" || return 1
  [[ $(ls -la "$(dirname "$BAO26")") == "$BAO26_LS" ]] || fail "the 2.6.4 directory changed"
}

run_case "A fresh raft start" case_a
run_case "B1 restart after kill -9" case_b1
run_case "B2 restart after tmux hangup" case_b2
run_case "B3 restart with a moved cluster port" case_b3
run_case "C1 migrate a running file vault" case_c1
run_case "C2 migrate a stopped file vault" case_c2
run_case "C3 resume a partial migration" case_c3
run_case "D1 recover or archive on a missing key" case_d1
run_case "D2 refuse a wrong token, then replace by hand" case_d2
run_case "D3 refuse a taken cluster port" case_d3
run_case "D4 refuse an old safe" case_d4
run_case "E teardown and read-only proof" case_e

printf '\n=== Results ===%s\n' "$RESULTS"
exit "$FAILED"
