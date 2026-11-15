// SPDX-FileCopyrightText: 2026 Christian Semmelmann
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/extra-code/container-ci/internal/fakecli"
	"github.com/extra-code/container-ci/internal/gitlab"
	"github.com/extra-code/container-ci/internal/version"
)

func run(t *testing.T, args, env []string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := dispatch(context.Background(), args, env, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestVersion(t *testing.T) {
	if code, out, _ := run(t, []string{"version"}, nil); code != 0 || out != "dev\n" {
		t.Errorf("version = %d, %q", code, out)
	}
	code, out, _ := run(t, []string{"version", "-json"}, nil)
	var v struct{ Version string }
	if code != 0 || json.Unmarshal([]byte(out), &v) != nil || v.Version != "dev" {
		t.Errorf("version -json = %d, %q", code, out)
	}
}

func TestVersionJSONKeys(t *testing.T) {
	old := version.Date
	version.Date = "2026-01-02"
	t.Cleanup(func() { version.Date = old })
	_, out, _ := run(t, []string{"version", "-json"}, nil)
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["built_at"]; ok {
		t.Errorf("version -json = %q, want no built_at key", out)
	}
	if m["commit_date"] != "2026-01-02" {
		t.Errorf("version -json = %q, want commit_date 2026-01-02", out)
	}
	if _, ok := m["version"]; !ok {
		t.Errorf("version -json = %q, want a version key", out)
	}
}

// jobEnv is the environment of a runner's stage process.
var jobEnv = []string{
	"CUSTOM_ENV_CI_JOB_ID=42",
	"CUSTOM_ENV_CI_RUNNER_SHORT_TOKEN=Ab_Cd",
	"CUSTOM_ENV_CI_JOB_IMAGE=ubuntu:26.04",
	"BUILD_FAILURE_EXIT_CODE=34",
	"SYSTEM_FAILURE_EXIT_CODE=33",
}

// listOf returns `container list` JSON with one entry per id, all created in 2020
// and carrying the executor's owner and prefix labels.
func listOf(prefix string, ids ...string) string {
	var entries []string
	for _, id := range ids {
		entries = append(entries, fmt.Sprintf(`{"id":%q,"configuration":{"creationDate":"2020-01-01T00:00:00Z","labels":{%q:%q,%q:%q}}}`,
			id, gitlab.LabelOwner, gitlab.Owner, gitlab.LabelPrefix, prefix))
	}
	return "[" + strings.Join(entries, ",") + "]"
}

func TestSweepVerb(t *testing.T) {
	f := fakecli.New(t, fakecli.Options{List: listOf("gitlab", "gitlab-old-1", "gitlab-old-2")})
	code, out, errOut := run(t, []string{"-container", f.Path, "sweep"}, nil)
	if code != 0 || out != "gitlab-old-1\ngitlab-old-2\n" {
		t.Errorf("sweep = %d, %q, %q; want the removed IDs on stdout and exit 0", code, out, errOut)
	}
	if want := []string{"list", "delete"}; !slices.Equal(f.Verbs(t), want) {
		t.Errorf("sweep called %q, want %q", f.Verbs(t), want)
	}
}

func TestSweepVerbSelectsByPrefix(t *testing.T) {
	f := fakecli.New(t, fakecli.Options{List: listOf("other", "other-old")})
	code, out, _ := run(t, []string{"-container", f.Path, "sweep"}, nil)
	if code != 0 || out != "" {
		t.Errorf("sweep = %d, %q; want nothing removed", code, out)
	}
}

func TestSweepVerbFailure(t *testing.T) {
	f := fakecli.New(t, fakecli.Options{List: listOf("gitlab", "gitlab-old"), DeleteRC: 1})
	code, _, errOut := run(t, []string{"-container", f.Path, "sweep"}, nil)
	if code != 1 || !strings.HasPrefix(errOut, "gitlab-container-executor: ") {
		t.Errorf("sweep with a failing delete = %d, %q; want exit 1 and a prefixed message", code, errOut)
	}
}

func TestPrepareVerb(t *testing.T) {
	f := fakecli.New(t, fakecli.Options{List: listOf("gitlab", "gitlab-old")})
	code, _, errOut := run(t, []string{"-container", f.Path, "prepare"}, jobEnv)
	if code != 0 {
		t.Fatalf("prepare = %d, %q", code, errOut)
	}
	if want := []string{"list", "delete", "delete", "run", "exec", "exec"}; !slices.Equal(f.Verbs(t), want) {
		t.Fatalf("prepare called %q, want %q", f.Verbs(t), want)
	}
	runCall := strings.Join(f.Calls(t)[3], " ")
	for _, want := range []string{"--name gitlab-ab-cd-42", "--label " + gitlab.LabelOwner + "=" + gitlab.Owner, "--label " + gitlab.LabelPrefix + "=gitlab", "ubuntu:26.04"} {
		if !strings.Contains(runCall, want) {
			t.Errorf("run call lacks %q:\n%s", want, runCall)
		}
	}
}

func TestPrepareVerbSystemFailure(t *testing.T) {
	f := fakecli.New(t, fakecli.Options{RunRC: 1})
	code, _, errOut := run(t, []string{"-container", f.Path, "prepare"}, jobEnv)
	if code != 33 || !strings.Contains(errOut, "ERROR:") {
		t.Errorf("prepare with a failing run = %d, %q; want 33", code, errOut)
	}
}

func TestContainerCLINotFound(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	for _, cli := range []string{filepath.Join(dir, "container"), plain, "container"} {
		for _, args := range [][]string{{"prepare"}, {"run", "/dev/null", "build_script"}, {"cleanup"}, {"sweep"}} {
			code, out, errOut := run(t, append([]string{"-container", cli}, args...), jobEnv)
			wantCode, wantPrefix := 33, "ERROR: "
			if args[0] == "sweep" {
				wantCode, wantPrefix = 1, "gitlab-container-executor: "
			}
			if code != wantCode || out != "" {
				t.Errorf("%s with -container %s: exit %d, stdout %q; want %d and no stdout", args[0], cli, code, out, wantCode)
			}
			for _, want := range []string{wantPrefix + "container CLI not found at " + cli, "-container", "launchd"} {
				if !strings.Contains(errOut, want) {
					t.Errorf("%s with -container %s: stderr %q lacks %q", args[0], cli, errOut, want)
				}
			}
		}
	}
}

func TestNeedsCLI(t *testing.T) {
	for verb, want := range map[string]bool{"prepare": true, "run": true, "cleanup": true, "sweep": true, "config": false, "version": false, "": false} {
		if got := needsCLI(verb); got != want {
			t.Errorf("needsCLI(%q) = %v, want %v", verb, got, want)
		}
	}
}

func TestConfigAndVersionNeedNoCLI(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "container")
	for _, args := range [][]string{{"config"}, {"version"}} {
		if code, _, errOut := run(t, append([]string{"-container", missing}, args...), jobEnv); code != 0 {
			t.Errorf("%s without the CLI = %d, %q; want 0", args[0], code, errOut)
		}
	}
}

func TestHelpListsExitCodes(t *testing.T) {
	_, _, errOut := run(t, []string{"-h"}, nil)
	for _, want := range []string{"Exit codes:", "BUILD_FAILURE_EXIT_CODE", "SYSTEM_FAILURE_EXIT_CODE", "sweep: 1 when it cannot remove containers or reach the container CLI"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("usage lacks %q:\n%s", want, errOut)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"bogus"},
		{"run", "script"},
		{"prepare", "extra"},
		{"--no-such-flag", "config"},
	} {
		if code, _, _ := run(t, args, nil); code != 2 {
			t.Errorf("%q: exit %d, want 2", args, code)
		}
	}
}

func TestHelp(t *testing.T) {
	code, _, errOut := run(t, []string{"-h"}, nil)
	if code != 0 || !strings.Contains(errOut, "prepare_exec") {
		t.Errorf("-h = %d, %q", code, errOut)
	}
}

func TestConfigVerb(t *testing.T) {
	code, out, _ := run(t, []string{"config"}, []string{"CUSTOM_ENV_CI_JOB_ID=7"})
	if code != 0 || !strings.Contains(out, `"hostname":"gitlab-7"`) {
		t.Errorf("config = %d, %q", code, out)
	}
}

func TestMacOSFlags(t *testing.T) {
	env := []string{"CUSTOM_ENV_CI_JOB_ID=7"}
	code, out, _ := run(t, []string{"-guest", "macos", "-runtime", "r", "-home", "/Users/ci/", "-label", "a=b", "config"}, env)
	if code != 0 || !strings.Contains(out, `"builds_dir":"/Users/ci/builds"`) {
		t.Errorf("macOS config = %d, %q", code, out)
	}
	f := fakecli.New(t, fakecli.Options{})
	if code, _, errOut := run(t, []string{"-container", f.Path, "-guest", "macos", "-runtime", "r", "-home", "/Users/ci", "cleanup"}, env); code != 0 {
		t.Errorf("macOS cleanup = %d, %q", code, errOut)
	}
	for _, args := range [][]string{
		{"-guest", "macos", "-home", "/Users/ci", "config"},
		{"-guest", "macos", "-runtime", "r", "-home", "/Users/ci", "-cache-dir", "/c", "config"},
		{"-label", "novalue", "config"},
	} {
		if code, _, _ := run(t, args, env); code != 2 {
			t.Errorf("%q = %d, want usage error 2", args, code)
		}
	}
}

func TestSystemFailureCode(t *testing.T) {
	f := fakecli.New(t, fakecli.Options{ExecFail: true})
	env := []string{"CUSTOM_ENV_CI_JOB_ID=7", "SYSTEM_FAILURE_EXIT_CODE=33", "BUILD_FAILURE_EXIT_CODE=34"}
	code, _, errOut := run(t, []string{"-container", f.Path, "run", "/dev/null", "build_script"}, env)
	if code != 33 || !strings.Contains(errOut, "ERROR:") {
		t.Errorf("run against a missing container = %d, %q; want 33", code, errOut)
	}
}
