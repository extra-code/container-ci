#!/bin/sh

# SPDX-FileCopyrightText: 2026 Christian Semmelmann
# SPDX-License-Identifier: Apache-2.0

# Test check-private.sh against a throwaway git repository.
#
# Each case commits one file into a fresh repository and asserts the exit code of the script.
# Addresses are passed as two halves and joined at run time, so this file never contains a
# complete address and passes the check it tests.
#
# Usage:  check-private.test.sh
# Exit:   0 when every case passes, 1 when one fails.

set -eu

script=$(cd "$(dirname "$0")" && pwd)/check-private.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

failures=0

# new_repo: an empty repository holding a copy of the script under scripts/.
new_repo() {
  repo="$tmp/repo"
  rm -rf "$repo"
  mkdir -p "$repo/scripts"
  cp "$script" "$repo/scripts/check-private.sh"
  git -C "$repo" init -q
}

# run_check: run the copied script, discard its output and print its exit code.
run_check() {
  status=0
  (cd "$repo" && sh scripts/check-private.sh >/dev/null 2>&1) || status=$?
  echo "$status"
}

# expect NAME WANT: compare the exit code of the check against WANT.
expect() {
  got=$(run_check)
  if [ "$got" = "$2" ]; then
    echo "ok   $1"
  else
    echo "FAIL $1: exit $got, want $2" >&2
    failures=$((failures + 1))
  fi
}

# file_case NAME WANT FILE FIRST SECOND: one tracked file whose content is FIRST then SECOND.
file_case() {
  new_repo
  tracked=$3
  printf 'host %s%s here\n' "$4" "$5" >"$repo/$3"
  git -C "$repo" add -- "$tracked"
  expect "$1" "$2"
}

file_case "10/8 address" 1 notes.txt 10.1.2 .3
file_case "192.168/16 address" 1 notes.txt 192.168.1 .1
file_case "172.15 is public" 0 notes.txt 172.15.0 .1
file_case "172.16 is private" 1 notes.txt 172.16.0 .1
file_case "172.31 is private" 1 notes.txt 172.31.255 .1
file_case "1010 is not 10/8" 0 notes.txt 1010.1.2 .3
file_case "link-local IPv4" 1 notes.txt 169.254.1 .1
file_case "CGNAT 100.64" 1 notes.txt 100.64.0 .1
file_case "100.128 is public" 0 notes.txt 100.128.0 .1
file_case "IPv6 unique local" 1 notes.txt fd00 ::1
file_case "IPv6 link-local" 1 notes.txt fe80 ::1
file_case "IPv6 documentation range" 0 notes.txt 2001 :db8::1
file_case "private address in a lock file" 0 deps.lock 10.1.2 .3

# pattern_case NAME WANT PATTERNS_PATH CONTENT: a clean tracked file, one pattern file.
pattern_case() {
  new_repo
  tracked=notes.txt
  printf '%s\n' "$4" >"$repo/notes.txt"
  git -C "$repo" add -- "$tracked"
  CONTAINER_CI_PRIVATE_PATTERNS="$3"
  export CONTAINER_CI_PRIVATE_PATTERNS
  expect "$1" "$2"
  unset CONTAINER_CI_PRIVATE_PATTERNS
}

printf '# comment\n\n  \nIntranet-Host\n' >"$tmp/patterns"
pattern_case "pattern matches case-insensitively" 1 "$tmp/patterns" "see intranet-host for details"
pattern_case "pattern without a match" 0 "$tmp/patterns" "nothing to see"
pattern_case "missing pattern file" 2 "$tmp/absent" "nothing to see"
printf '# only a comment\n\n   \n' >"$tmp/comments"
pattern_case "comment-only pattern file" 0 "$tmp/comments" "nothing to see"

if [ "$failures" -ne 0 ]; then
  echo "check-private.test: $failures case(s) failed" >&2
  exit 1
fi
echo "check-private.test: all cases passed"
