// SPDX-FileCopyrightText: 2026 Christian Semmelmann
// SPDX-License-Identifier: Apache-2.0

// Package fakecli writes a stand-in for the container CLI for tests. The fake
// records every invocation, answers `list` from a fixture, and runs the
// process of an `exec` on the host.
package fakecli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Options selects the fake's behaviour.
type Options struct {
	List       string // JSON that `list` prints; "[]" when empty
	ExecFail   bool   // `exec` fails as for a missing container
	ExecUserRC int    // exit status of an `exec --user`, which the fake records but does not run
	DeleteRC   int    // exit status of `delete`
	RunRC      int    // exit status of `run`
	// RealExec runs an `exec --user` process too (the user is ignored), in its
	// own process group that fake SIGTERM or SIGINT does not reach; it relies
	// on /bin/sh job control (`set -m`) as on macOS.
	RealExec bool
}

// Fake is an installed fake CLI.
type Fake struct {
	Path string
	log  string
}

const script = `#!/bin/sh
real=%[7]d
{ printf '%%s\037' "$@"; printf '\036'; } >> '%[1]s'
case "$1" in
list) cat '%[2]s' ;;
delete) exit %[3]d ;;
run) exit %[4]d ;;
exec)
  shift
  asuser=
  while :; do
    case "$1" in
    --interactive) shift ;;
    --env) shift 2 ;;
    --user) asuser=1; shift 2 ;;
    *) break ;;
    esac
  done
  if [ %[5]d -eq 1 ]; then echo "Error: get failed: container $1 not found" >&2; exit 1; fi
  if [ -n "$asuser" ] && [ "$real" -ne 1 ]; then exit %[6]d; fi
  shift
  if [ "$real" -eq 1 ]; then
    trap : TERM INT
    set -m
    "$@"
    exit $?
  fi
  exec "$@"
  ;;
*) echo "fake: unexpected $1" >&2; exit 64 ;;
esac
`

// New installs a fake CLI in a temporary directory.
func New(t *testing.T, o Options) Fake {
	t.Helper()
	dir := t.TempDir()
	if o.List == "" {
		o.List = "[]"
	}
	list := filepath.Join(dir, "list.json")
	if err := os.WriteFile(list, []byte(o.List), 0o644); err != nil {
		t.Fatal(err)
	}
	execFail := 0
	if o.ExecFail {
		execFail = 1
	}
	realExec := 0
	if o.RealExec {
		realExec = 1
	}
	f := Fake{Path: filepath.Join(dir, "container"), log: filepath.Join(dir, "calls")}
	body := fmt.Sprintf(script, f.log, list, o.DeleteRC, o.RunRC, execFail, o.ExecUserRC, realExec)
	if err := os.WriteFile(f.Path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

// Calls returns the argument lists of every invocation so far.
func (f Fake) Calls(t *testing.T) [][]string {
	t.Helper()
	b, err := os.ReadFile(f.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	for _, rec := range strings.Split(strings.TrimSuffix(string(b), "\x1e"), "\x1e") {
		calls = append(calls, strings.Split(strings.TrimSuffix(rec, "\x1f"), "\x1f"))
	}
	return calls
}

// Verbs returns the first argument of every invocation so far.
func (f Fake) Verbs(t *testing.T) []string {
	t.Helper()
	var verbs []string
	for _, c := range f.Calls(t) {
		verbs = append(verbs, c[0])
	}
	return verbs
}
