#!/usr/bin/env bash

# SPDX-FileCopyrightText: 2026 Christian Semmelmann
# SPDX-License-Identifier: Apache-2.0

# Check that every source and packaged file carries the SPDX header.
#
# Scope is the delivery-plus-authorship boundary: a file the release package ships, or a file this
# repository authors as source. Tool configuration, generated manifests and prose carry no header
# and are not searched. The scope is a set of paths rather than a list of files, so a file added
# under one of them is checked without anyone remembering to add it here.
#
# The copyright year is the year of first publication and does not advance per file, so the
# expected line is literal - a drifting year is a finding like any other.
#
# Usage:  check-spdx.sh
# Exit:   0 when every file in scope carries both lines, 1 when one does not, 65 when the scope
#         itself resolves to nothing (a moved directory, rather than a clean repository).

set -euo pipefail

COPYRIGHT='SPDX-FileCopyrightText: 2026 Christian Semmelmann'
IDENTIFIER='SPDX-License-Identifier: Apache-2.0'

# How far into a file the header may sit. A shebang, an XML declaration and a DOCTYPE may precede
# it, which is three lines before the two the header itself needs.
HEAD_LINES=6

cd "$(dirname "${BASH_SOURCE[0]}")/.."

log() { printf 'check-spdx: %s\n' "$*" >&2; }

files=$(
  find cmd internal -type f -name '*.go'
  find scripts githooks -type f
)

[ -n "$files" ] || {
  log "no files in scope - the checked paths have moved"
  exit 65
}

missing=""
count=0
while IFS= read -r file; do
  count=$((count + 1))
  head=$(head -n "$HEAD_LINES" "$file")
  case "$head" in
  *"$COPYRIGHT"*) ;;
  *)
    missing="$missing  $file (no copyright line)"$'\n'
    continue
    ;;
  esac
  case "$head" in
  *"$IDENTIFIER"*) ;;
  *) missing="$missing  $file (no license identifier)"$'\n' ;;
  esac
done <<<"$files"

if [ -n "$missing" ]; then
  log "$(printf '%s' "$missing" | grep -c .) of $count files are missing the SPDX header:"
  printf '%s' "$missing" >&2
  log "add both lines at the top, in that file's comment syntax"
  exit 1
fi

log "$count files carry the SPDX header"
