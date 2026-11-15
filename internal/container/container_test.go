// SPDX-FileCopyrightText: 2026 Christian Semmelmann
// SPDX-License-Identifier: Apache-2.0

package container

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/extra-code/container-ci/internal/fakecli"
)

func TestRunArgs(t *testing.T) {
	s := Spec{
		Name:       "gitlab-1",
		Image:      "ubuntu:26.04",
		Runtime:    "container-runtime-linux",
		Platform:   "linux/amd64",
		CPUs:       4,
		Memory:     "8G",
		Labels:     map[string]string{"b": "2", "a": "1"},
		Mounts:     []Mount{{Source: "/c", Target: "/cache"}, {Source: "/b", Target: "/opt/bin", ReadOnly: true}},
		Entrypoint: "/bin/sh",
		Args:       []string{"-c", "true"},
	}
	got, err := s.runArgs()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"run", "--detach", "--progress", "none", "--name", "gitlab-1",
		"--runtime", "container-runtime-linux", "--platform", "linux/amd64",
		"--cpus", "4", "--memory", "8G",
		"--label", "a=1", "--label", "b=2",
		"--volume", "/c:/cache", "--volume", "/b:/opt/bin:ro",
		"--entrypoint", "/bin/sh", "--", "ubuntu:26.04", "-c", "true",
	}
	if !slices.Equal(got, want) {
		t.Errorf("runArgs:\n got %q\nwant %q", got, want)
	}
}

func TestRunArgsMinimal(t *testing.T) {
	got, err := Spec{Name: "n", Image: "i"}.runArgs()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"run", "--detach", "--progress", "none", "--name", "n", "--", "i"}
	if !slices.Equal(got, want) {
		t.Errorf("runArgs: got %q, want %q", got, want)
	}
}

func TestRunArgsRejects(t *testing.T) {
	for name, s := range map[string]Spec{
		"no name":        {Image: "i"},
		"no image":       {Name: "n"},
		"colon in mount": {Name: "n", Image: "i", Mounts: []Mount{{Source: "/a:b", Target: "/c"}}},
	} {
		if _, err := s.runArgs(); err == nil {
			t.Errorf("%s: runArgs accepted %+v", name, s)
		}
	}
}

func TestList(t *testing.T) {
	fixture, err := os.ReadFile("testdata/list.json")
	if err != nil {
		t.Fatal(err)
	}
	f := fakecli.New(t, fakecli.Options{List: string(fixture)})
	cs, err := CLI{Path: f.Path}.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 {
		t.Fatalf("List: %d containers, want 1", len(cs))
	}
	c := cs[0]
	if c.ID != "cce-probe-1" || c.Status.State != "running" ||
		c.Configuration.RuntimeHandler != "container-runtime-linux" ||
		c.Configuration.Labels["ci.probe"] != "1" ||
		!c.Configuration.CreationDate.Equal(time.Date(2026, 10, 2, 14, 26, 36, 0, time.UTC)) {
		t.Errorf("List decoded %+v", c)
	}
	if want := []string{"list", "--all", "--format", "json"}; !slices.Equal(f.Calls(t)[0], want) {
		t.Errorf("List called %q, want %q", f.Calls(t)[0], want)
	}
}

func TestExec(t *testing.T) {
	f := fakecli.New(t, fakecli.Options{})
	var out bytes.Buffer
	code, err := CLI{Path: f.Path}.Exec(context.Background(), "n", Process{
		Args:   []string{"/bin/sh", "-c", "cat; exit 7"},
		Env:    []string{"A=1"},
		Stdin:  strings.NewReader("hello"),
		Stdout: &out,
	})
	if err != nil || code != 7 {
		t.Fatalf("Exec = %d, %v; want 7, nil", code, err)
	}
	if out.String() != "hello" {
		t.Errorf("stdout = %q, want %q", out.String(), "hello")
	}
	want := []string{"exec", "--interactive", "--env", "A=1", "n", "/bin/sh", "-c", "cat; exit 7"}
	if got := f.Calls(t)[0]; !slices.Equal(got, want) {
		t.Errorf("Exec called %q, want %q", got, want)
	}
}

func TestExecNoStdin(t *testing.T) {
	f := fakecli.New(t, fakecli.Options{})
	if _, err := (CLI{Path: f.Path}).Exec(context.Background(), "n", Process{Args: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.Calls(t)[0]; slices.Contains(got, "--interactive") {
		t.Errorf("Exec without stdin passed --interactive: %q", got)
	}
}

func TestExecUser(t *testing.T) {
	f := fakecli.New(t, fakecli.Options{})
	if _, err := (CLI{Path: f.Path}).Exec(context.Background(), "n", Process{Args: []string{"true"}, User: "0"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"exec", "--user", "0", "n", "true"}
	if got := f.Calls(t)[0]; !slices.Equal(got, want) {
		t.Errorf("Exec called %q, want %q", got, want)
	}
}

// The runner signals the executor's process group on cancel and timeout. The
// CLI must sit outside it, or it reports a failed signal forward in the job log.
func TestExecOwnProcessGroup(t *testing.T) {
	f := fakecli.New(t, fakecli.Options{})
	var out bytes.Buffer
	code, err := CLI{Path: f.Path}.Exec(context.Background(), "n", Process{
		Args:   []string{"/bin/sh", "-c", "ps -o pgid= -p $$"},
		Stdout: &out,
	})
	if err != nil || code != 0 {
		t.Fatalf("Exec = %d, %v; want 0, nil", code, err)
	}
	pgid, err := strconv.Atoi(strings.TrimSpace(out.String()))
	if err != nil {
		t.Fatalf("ps printed %q: %v", out.String(), err)
	}
	if pgid == syscall.Getpgrp() {
		t.Errorf("the CLI runs in the caller's process group %d", pgid)
	}
}

func TestExecMissingCLI(t *testing.T) {
	_, err := CLI{Path: "/nonexistent/container"}.Exec(context.Background(), "n", Process{Args: []string{"true"}})
	if err == nil {
		t.Fatal("Exec with a missing CLI returned no error")
	}
}

func TestRemoveIgnoresAbsent(t *testing.T) {
	f := fakecli.New(t, fakecli.Options{DeleteRC: 1, List: `[]`})
	if err := (CLI{Path: f.Path}).Remove(context.Background(), "gone"); err != nil {
		t.Fatalf("Remove of an absent container: %v", err)
	}
	if want := []string{"delete", "list"}; !slices.Equal(f.Verbs(t), want) {
		t.Errorf("Remove called %q, want %q", f.Verbs(t), want)
	}
}

func TestRemoveReportsLeftover(t *testing.T) {
	f := fakecli.New(t, fakecli.Options{DeleteRC: 1, List: `[{"id":"stuck"}]`})
	if err := (CLI{Path: f.Path}).Remove(context.Background(), "stuck"); err == nil {
		t.Fatal("Remove reported success for a container that is still listed")
	}
}

func TestRemoveNothing(t *testing.T) {
	f := fakecli.New(t, fakecli.Options{})
	if err := (CLI{Path: f.Path}).Remove(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := f.Calls(t); calls != nil {
		t.Errorf("Remove() invoked the CLI: %q", calls)
	}
}

func TestSelect(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	mk := func(id string, age time.Duration, labels map[string]string) Container {
		return Container{ID: id, Configuration: Configuration{CreationDate: now.Add(-age), Labels: labels}}
	}
	own := map[string]string{"owner": "x"}
	cs := []Container{
		mk("old-own", 48*time.Hour, own),
		mk("new-own", time.Hour, own),
		mk("old-other", 48*time.Hour, map[string]string{"owner": "y"}),
		mk("old-unlabelled", 48*time.Hour, nil),
	}
	got := Select(cs, own, now.Add(-24*time.Hour))
	if want := []string{"old-own"}; !slices.Equal(got, want) {
		t.Errorf("Select = %q, want %q", got, want)
	}
}

func TestRunArgsImageAfterSeparator(t *testing.T) {
	for _, image := range []string{"ubuntu:26.04", "--volume=/:/host"} {
		got, err := Spec{Name: "n", Image: image, Args: []string{"a"}}.runArgs()
		if err != nil {
			t.Fatal(err)
		}
		i := slices.Index(got, image)
		if i < 1 || got[i-1] != "--" {
			t.Errorf("runArgs(%q) = %q, want -- right before the image", image, got)
		}
		if i != slices.Index(got, "--")+1 {
			t.Errorf("runArgs(%q) = %q, image is not the first word after --", image, got)
		}
		if slices.Contains(got[:slices.Index(got, "--")], image) {
			t.Errorf("runArgs(%q) = %q, image appears among the options", image, got)
		}
	}
}

// failingCLI writes a CLI that exits with status 1.
func failingCLI(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "container")
	body := "#!/bin/sh\nexit 1\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestListRejectsNonJSON(t *testing.T) {
	f := fakecli.New(t, fakecli.Options{List: "not json"})
	_, err := CLI{Path: f.Path}.List(context.Background())
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("List of non-JSON output: err = %v, want a decode error", err)
	}
}

func TestListCLIFails(t *testing.T) {
	_, err := CLI{Path: failingCLI(t)}.List(context.Background())
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("List with a failing CLI: err = %v, want an *exec.ExitError", err)
	}
}

func TestRemoveListFails(t *testing.T) {
	err := CLI{Path: failingCLI(t)}.Remove(context.Background(), "n")
	var ee *exec.ExitError
	if !errors.As(err, &ee) || !strings.Contains(err.Error(), "list:") {
		t.Fatalf("Remove with delete and list failing: err = %v", err)
	}
}

func TestRemoveAbsentAmongOthers(t *testing.T) {
	f := fakecli.New(t, fakecli.Options{DeleteRC: 1, List: `[{"id":"other"}]`})
	if err := (CLI{Path: f.Path}).Remove(context.Background(), "gone"); err != nil {
		t.Fatalf("Remove of an absent container: %v", err)
	}
}
