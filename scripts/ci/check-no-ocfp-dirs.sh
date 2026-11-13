#!/usr/bin/env bash
# Fails when any ocfp directory exists in the user's home. CI runs it after
# the tests, in a container that started with none, so a test that wrote
# the real ocfp config, state, data, or cache directories fails the job.
# Usage: check-no-ocfp-dirs.sh [home]
#
# The home defaults to the one in the passwd entry, which a test that moves
# HOME can't hide. The script also checks the directories that HOME, the
# XDG variables, and OCFP_HOME name. It only reads, and it lists file names,
# never their contents. `make preflight` doesn't run it, because a
# developer's machine has real ocfp directories.
set -euo pipefail

if [ $# -gt 1 ]; then
  echo "usage: $0 [home]" >&2
  exit 2
fi

passwd_home() {
  local user
  user="$(id -un)"
  if command -v getent >/dev/null 2>&1; then
    getent passwd "$user" | cut -d: -f6
  elif command -v dscl >/dev/null 2>&1; then
    dscl . -read "/Users/$user" NFSHomeDirectory | awk '{print $2}'
  else
    perl -e 'print((getpwuid($<))[7])'
  fi
}

home="${1:-$(passwd_home)}"
if [ -z "$home" ]; then
  echo "cannot find the home directory in the passwd entry" >&2
  exit 2
fi

dirs=()
for base in "$home" ${HOME:+"$HOME"}; do
  dirs+=("$base/.ocfp" "$base/.config/ocfp" "$base/.local/state/ocfp" "$base/.local/share/ocfp" "$base/.cache/ocfp")
done
for var in XDG_CONFIG_HOME XDG_STATE_HOME XDG_DATA_HOME XDG_CACHE_HOME; do
  if [ -n "${!var:-}" ]; then
    dirs+=("${!var}/ocfp")
  fi
done
if [ -n "${OCFP_HOME:-}" ]; then
  dirs+=("$OCFP_HOME")
fi

found=0
checked=" "
for dir in "${dirs[@]}"; do
  case "$checked" in *" $dir "*) continue ;; esac
  checked="$checked$dir "
  echo "checking $dir"
  if [ -e "$dir" ] || [ -L "$dir" ]; then
    echo "found $dir, which holds:" >&2
    find "$dir" -maxdepth 4 >&2 || true
    found=1
  fi
done

if [ "$found" -ne 0 ]; then
  echo "A test wrote an ocfp directory in the home. Every test package must run its tests in a temporary home with testenv." >&2
  exit 1
fi
echo "No ocfp directories exist."
