#!/bin/sh

# SPDX-FileCopyrightText: 2026 Christian Semmelmann
# SPDX-License-Identifier: Apache-2.0

# Check that no tracked file names private infrastructure.
#
# Two kinds of match are findings. The first is a non-public address: the private IPv4 ranges of
# RFC 1918, IPv4 link-local, carrier-grade NAT, IPv6 unique local and IPv6 link-local. The second
# is any line of a local pattern file. The pattern file is never tracked, so internal host names
# stay out of the repository. It is $CONTAINER_CI_PRIVATE_PATTERNS when set, else
# info/private-patterns inside the git directory when that file exists. It holds one
# case-insensitive fixed string per line, and blank lines and lines starting with # are ignored.
# Lock files are not searched.
#
# Usage:  check-private.sh
# Exit:   0 when nothing matches, 1 when a line matches (printed as file:line:text), 2 when the
#         pattern file is set but unreadable or git cannot search.

set -eu

cd "$(dirname "$0")/.."

log() { printf 'check-private: %s\n' "$*" >&2; }

OCTET='[0-9]{1,3}'
PRIVATE_IPV4="(^|[^0-9.])(10(\\.$OCTET){3}|172\\.(1[6-9]|2[0-9]|3[01])(\\.$OCTET){2}"
PRIVATE_IPV4="$PRIVATE_IPV4|192\\.168(\\.$OCTET){2}|169\\.254(\\.$OCTET){2}"
PRIVATE_IPV4="$PRIVATE_IPV4|100\\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])(\\.$OCTET){2})([^0-9]|\$)"
HEX4='[0-9A-Fa-f]'
PRIVATE_IPV6="(^|[^0-9A-Fa-f:])(f[cd]$HEX4{2}|fe[89abAB]$HEX4)(:$HEX4{0,4}){1,7}"

found=0

# Run git grep with the given flags over the tracked files, minus lock files. It exits 0 on a
# match, 1 on none and 128 or more on an error.
search() {
  status=0
  git grep "$@" -- . ':!*.lock' || status=$?
  case "$status" in
  0) found=1 ;;
  1) ;;
  *)
    log "git grep failed with status $status"
    exit 2
    ;;
  esac
}

search -InE -e "$PRIVATE_IPV4"
search -InEi -e "$PRIVATE_IPV6"

patterns="${CONTAINER_CI_PRIVATE_PATTERNS:-}"
if [ -z "$patterns" ]; then
  gitdir=$(git rev-parse --git-dir) || {
    log "not inside a git work tree"
    exit 2
  }
  [ ! -f "$gitdir/info/private-patterns" ] || patterns="$gitdir/info/private-patterns"
fi

if [ -n "$patterns" ]; then
  [ -r "$patterns" ] || {
    log "cannot read the pattern file $patterns"
    exit 2
  }
  filtered=$(mktemp)
  trap 'rm -f "$filtered"' EXIT
  grep -v -e '^[[:space:]]*$' -e '^[[:space:]]*#' "$patterns" >"$filtered" || true
  if [ -s "$filtered" ]; then
    search -IniF -f "$filtered"
  fi
fi

if [ "$found" -ne 0 ]; then
  log "private infrastructure named in tracked files, see the matches above"
  exit 1
fi

log "no private infrastructure in tracked files"
