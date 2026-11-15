// SPDX-FileCopyrightText: 2026 Christian Semmelmann
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/extra-code/container-ci/internal/container"
	"github.com/extra-code/container-ci/internal/fakecli"
)

var now = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

func newExecutor(t *testing.T, o fakecli.Options, env Env) (*Executor, fakecli.Fake, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	f := fakecli.New(t, o)
	var stdout, stderr bytes.Buffer
	base := Env{
		"CUSTOM_ENV_CI_JOB_ID":             "42",
		"CUSTOM_ENV_CI_RUNNER_SHORT_TOKEN": "Ab_Cd",
		"BUILD_FAILURE_EXIT_CODE":          "11",
		"SYSTEM_FAILURE_EXIT_CODE":         "12",
	}
	for k, v := range env {
		base[k] = v
	}
	return &Executor{
		CLI:       container.CLI{Path: f.Path, Stderr: &stderr},
		Env:       base,
		Version:   "1.2.3",
		Prefix:    "gitlab",
		CPUs:      4,
		Memory:    "8G",
		OrphanAge: 24 * time.Hour,
		TmpDir:    t.TempDir(),
		Stdout:    &stdout,
		Stderr:    &stderr,
		Now:       func() time.Time { return now },
	}, f, &stdout, &stderr
}

// call returns invocation i of the fake CLI, failing the test when there are fewer.
func call(t *testing.T, f fakecli.Fake, i int) []string {
	t.Helper()
	calls := f.Calls(t)
	if i >= len(calls) {
		t.Fatalf("the CLI was invoked %d times, want at least %d: %q", len(calls), i+1, calls)
	}
	return calls[i]
}

func TestEnvFrom(t *testing.T) {
	e := EnvFrom([]string{"A=1", "CUSTOM_ENV_X=a=b", "BROKEN"})
	if e["A"] != "1" || e.Job("X") != "a=b" || len(e) != 2 {
		t.Errorf("EnvFrom = %v", e)
	}
}

func TestName(t *testing.T) {
	x, _, _, _ := newExecutor(t, fakecli.Options{}, nil)
	if name, err := x.Name(); err != nil || name != "gitlab-ab-cd-42" {
		t.Errorf("Name = %q, %v; want gitlab-ab-cd-42", name, err)
	}
	delete(x.Env, "CUSTOM_ENV_CI_RUNNER_SHORT_TOKEN")
	if name, _ := x.Name(); name != "gitlab-42" {
		t.Errorf("Name without a runner token = %q, want gitlab-42", name)
	}
	delete(x.Env, "CUSTOM_ENV_CI_JOB_ID")
	if _, err := x.Name(); err == nil {
		t.Error("Name without a job ID returned no error")
	}
}

func TestConfig(t *testing.T) {
	x, _, _, _ := newExecutor(t, fakecli.Options{}, nil)
	var out bytes.Buffer
	if err := x.Config(&out); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"builds_dir":           "/builds",
		"cache_dir":            "/cache",
		"builds_dir_is_shared": false,
		"hostname":             "gitlab-ab-cd-42",
		"driver":               map[string]any{"name": Driver, "version": "1.2.3"},
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Errorf("Config = %s, want %s", gotJSON, wantJSON)
	}
}

func TestImage(t *testing.T) {
	for _, tc := range []struct {
		name, job, def string
		allow          []string
		want           string
		jobErr         bool
		errHas         string
	}{
		{name: "job image", job: "alpine:3", def: "ubuntu", want: "alpine:3"},
		{name: "default", def: "ubuntu", want: "ubuntu"},
		{name: "none", jobErr: true, errHas: "-default-image"},
		{name: "allowed", job: "reg.example/team/img:1", allow: []string{"other/*", "reg.example/team/*"}, want: "reg.example/team/img:1"},
		{name: "denied", job: "evil/img", allow: []string{"reg.example/*"}, jobErr: true, errHas: "allowed: reg.example/*"},
		{name: "option", job: "--privileged", jobErr: true, errHas: "must not start with '-'"},
		{name: "option default", def: "-v", jobErr: true, errHas: "must not start with '-'"},
		{name: "space", job: "alpine --rm", jobErr: true, errHas: "whitespace"},
		{name: "tab", job: "alpine\tx", jobErr: true, errHas: "whitespace"},
		{name: "newline", job: "alpine\nx", jobErr: true, errHas: "whitespace"},
		{name: "control", job: "alpine\x00", jobErr: true, errHas: "control characters"},
		{name: "escape", job: "alpine\x1b[0m", jobErr: true, errHas: "control characters"},
		{name: "allow all still validates", job: "-x", allow: []string{"*"}, jobErr: true, errHas: "must not start with '-'"},
		{name: "literal dot", job: "regXexample/img", allow: []string{"reg.example/*"}, jobErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, _, _, _ := newExecutor(t, fakecli.Options{}, Env{"CUSTOM_ENV_CI_JOB_IMAGE": tc.job})
			x.DefaultImage, x.AllowImages = tc.def, tc.allow
			got, err := x.image()
			var je *JobError
			if tc.jobErr {
				if !errors.As(err, &je) {
					t.Fatalf("image = %q, %v; want a JobError", got, err)
				}
				if !strings.Contains(err.Error(), tc.errHas) {
					t.Errorf("error = %q, want it to contain %q", err, tc.errHas)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("image = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestPrepare(t *testing.T) {
	x, f, _, _ := newExecutor(t, fakecli.Options{}, Env{"CUSTOM_ENV_CI_JOB_IMAGE": "ubuntu:26.04"})
	x.CacheDir = filepath.Join(t.TempDir(), "cache")
	x.BinDir = "/opt/runner"
	if err := x.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"list", "delete", "run", "exec", "exec"}; !slices.Equal(f.Verbs(t), want) {
		t.Fatalf("Prepare called %q, want %q", f.Verbs(t), want)
	}
	builds := strings.Join(call(t, f, 4), " ")
	if want := "exec --user 0 gitlab-ab-cd-42 /bin/sh -c " + buildsScript + " container-ci " + BuildsDir; builds != want {
		t.Errorf("builds directory exec = %q, want %q", builds, want)
	}
	run := strings.Join(call(t, f, 2), " ")
	if strings.Contains(run, LabelContract) {
		t.Errorf("a Linux run sets %s:\n%s", LabelContract, run)
	}
	for _, want := range []string{
		"--name gitlab-ab-cd-42",
		"--label " + LabelJob + "=42",
		"--label " + LabelOwner + "=" + Owner,
		"--label " + LabelPrefix + "=gitlab",
		"--label " + LabelRunner + "=Ab_Cd",
		"--volume " + x.CacheDir + ":/cache",
		"--volume /opt/runner:/opt/container-ci/bin:ro",
		"--entrypoint /bin/sh -- ubuntu:26.04 -c " + idleScript,
	} {
		if !strings.Contains(run, want) {
			t.Errorf("run lacks %q:\n%s", want, run)
		}
	}
	if _, err := os.Stat(x.CacheDir); err != nil {
		t.Errorf("cache directory not created: %v", err)
	}
}

func macOSExecutor(t *testing.T, env Env) (*Executor, fakecli.Fake, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	x, f, stdout, stderr := newExecutor(t, fakecli.Options{}, env)
	x.Guest = GuestMacOS
	x.Runtime = "guest-runtime"
	x.Labels = map[string]string{"guest.image": "ci-26"}
	x.Home = t.TempDir()
	return x, f, stdout, stderr
}

func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Executor)
		ok   bool
	}{
		{name: "linux default", edit: func(x *Executor) { x.Guest = "" }, ok: true},
		{name: "linux with mounts", edit: func(x *Executor) { x.Guest = GuestLinux; x.CacheDir = "/c"; x.Runtime = "" }, ok: true},
		{name: "macos", edit: func(*Executor) {}, ok: true},
		{name: "unknown guest", edit: func(x *Executor) { x.Guest = "windows" }},
		{name: "macos without runtime", edit: func(x *Executor) { x.Runtime = "" }},
		{name: "macos without home", edit: func(x *Executor) { x.Home = "" }},
		{name: "macos with relative home", edit: func(x *Executor) { x.Home = "ci" }},
		{name: "macos with cache mount", edit: func(x *Executor) { x.CacheDir = "/c" }},
		{name: "macos with bin mount", edit: func(x *Executor) { x.BinDir = "/b" }},
		{name: "reserved label", edit: func(x *Executor) { x.Labels = map[string]string{LabelOwner: "me"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, _, _, _ := macOSExecutor(t, nil)
			tc.edit(x)
			if err := x.Validate(); (err == nil) != tc.ok {
				t.Errorf("Validate = %v, want ok %v", err, tc.ok)
			}
		})
	}
}

func TestValidateFields(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Executor)
		want string // error text; empty when valid
	}{
		{name: "defaults valid", edit: func(*Executor) {}},
		{name: "prefix empty", edit: func(x *Executor) { x.Prefix = "" }, want: "-prefix"},
		{name: "prefix uppercase", edit: func(x *Executor) { x.Prefix = "Gitlab" }, want: "-prefix"},
		{name: "prefix leading dash", edit: func(x *Executor) { x.Prefix = "-a" }, want: "-prefix"},
		{name: "prefix slash", edit: func(x *Executor) { x.Prefix = "a/b" }, want: "-prefix"},
		{name: "prefix digits and dashes", edit: func(x *Executor) { x.Prefix = "9ci-mac-1" }},
		{name: "cpus negative", edit: func(x *Executor) { x.CPUs = -1 }, want: "-cpus"},
		{name: "cpus zero", edit: func(x *Executor) { x.CPUs = 0 }},
		{name: "memory empty", edit: func(x *Executor) { x.Memory = "" }},
		{name: "memory bytes", edit: func(x *Executor) { x.Memory = "1048576" }},
		{name: "memory megabytes", edit: func(x *Executor) { x.Memory = "512M" }},
		{name: "memory gigabytes with B", edit: func(x *Executor) { x.Memory = "8GB" }},
		{name: "memory lowercase", edit: func(x *Executor) { x.Memory = "2g" }},
		{name: "memory petabytes", edit: func(x *Executor) { x.Memory = "1P" }},
		{name: "memory zero", edit: func(x *Executor) { x.Memory = "0G" }, want: "-memory"},
		{name: "memory unknown unit", edit: func(x *Executor) { x.Memory = "8X" }, want: "-memory"},
		{name: "memory unit only", edit: func(x *Executor) { x.Memory = "G" }, want: "-memory"},
		{name: "memory negative", edit: func(x *Executor) { x.Memory = "-8G" }, want: "-memory"},
		{name: "memory option", edit: func(x *Executor) { x.Memory = "8G --privileged" }, want: "-memory"},
		{name: "orphan age zero", edit: func(x *Executor) { x.OrphanAge = 0 }, want: "-orphan-age"},
		{name: "orphan age negative", edit: func(x *Executor) { x.OrphanAge = -time.Hour }, want: "-orphan-age"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, _, _, _ := newExecutor(t, fakecli.Options{}, nil)
			tc.edit(x)
			err := x.Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("Validate = %v, want nil", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("Validate = %v, want an error naming %s", err, tc.want)
			}
		})
	}
}

func TestConfigMacOS(t *testing.T) {
	x, _, _, _ := macOSExecutor(t, nil)
	x.Home = "/Users/ci"
	var out bytes.Buffer
	if err := x.Config(&out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"builds_dir":"/Users/ci/builds"`, `"cache_dir":"/Users/ci/cache"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("Config = %s, lacks %s", out.String(), want)
		}
	}
}

func TestPrepareMacOS(t *testing.T) {
	x, f, _, stderr := macOSExecutor(t, Env{"CUSTOM_ENV_CI_JOB_IMAGE": "ubuntu:26.04"})
	if err := x.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"list", "delete", "run", "exec"}; !slices.Equal(f.Verbs(t), want) {
		t.Fatalf("Prepare called %q, want %q", f.Verbs(t), want)
	}
	run := strings.Join(call(t, f, 2), " ")
	for _, want := range []string{"--runtime guest-runtime", "--label guest.image=ci-26", "--label " + LabelJob + "=42", "--label " + LabelContract + "=" + Contract} {
		if !strings.Contains(run, want) {
			t.Errorf("run lacks %q:\n%s", want, run)
		}
	}
	if !strings.HasSuffix(run, " none") || strings.Contains(run, "--entrypoint") || strings.Contains(run, "--volume") {
		t.Errorf("run = %q, want image none without entrypoint or mounts", run)
	}
	if !strings.Contains(stderr.String(), "image ubuntu:26.04 is ignored") {
		t.Errorf("stderr = %q, want the job's image reported as ignored", stderr)
	}
}

func TestRunMacOSScratchInHome(t *testing.T) {
	x, _, stdout, _ := macOSExecutor(t, nil)
	if err := x.Run(context.Background(), writeScript(t, `echo "$0"`), "build_script"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stdout.String(), x.Home+"/.container-ci-") {
		t.Errorf("script ran from %q, want a scratch directory in %s", stdout.String(), x.Home)
	}
	if left, _ := filepath.Glob(x.Home + "/.container-ci-*"); len(left) > 0 {
		t.Errorf("Run left %q", left)
	}
}

func TestRunInterruptedMacOSSignalsAsRoot(t *testing.T) {
	x, f, _, _ := newExecutor(t, fakecli.Options{RealExec: true}, nil)
	x.Guest, x.Runtime, x.Home = GuestMacOS, "guest-runtime", t.TempDir()
	x.StopGrace = 200 * time.Millisecond
	pidFile := filepath.Join(t.TempDir(), "job.pid")
	interrupt(t, x, "echo $$ > "+pidFile+"\nexec sleep 30 >/dev/null 2>&1", 300*time.Millisecond)
	if c := call(t, f, 1); len(c) < 3 || !slices.Equal(c[:3], []string{"exec", "--user", "root"}) {
		t.Errorf("signal exec = %q, want it run as root", c)
	}
	assertGone(t, pidFile)
}

func TestPrepareFailures(t *testing.T) {
	var je *JobError
	x, _, _, _ := newExecutor(t, fakecli.Options{RunRC: 1}, Env{"CUSTOM_ENV_CI_JOB_IMAGE": "i"})
	if err := x.Prepare(context.Background()); err == nil || errors.As(err, &je) {
		t.Errorf("failed run: Prepare = %v, want a system error", err)
	}
	x, _, _, _ = newExecutor(t, fakecli.Options{ExecFail: true}, Env{"CUSTOM_ENV_CI_JOB_IMAGE": "i"})
	if err := x.Prepare(context.Background()); err == nil || errors.As(err, &je) {
		t.Errorf("failed readiness: Prepare = %v, want a system error", err)
	}
	x, _, _, _ = newExecutor(t, fakecli.Options{ExecUserRC: 1}, Env{"CUSTOM_ENV_CI_JOB_IMAGE": "i"})
	if err := x.Prepare(context.Background()); err == nil || errors.As(err, &je) {
		t.Errorf("failed builds directory: Prepare = %v, want a system error", err)
	}
	x, f, _, _ := newExecutor(t, fakecli.Options{}, nil)
	if err := x.Prepare(context.Background()); !errors.As(err, &je) {
		t.Errorf("no image: Prepare = %v, want a JobError", err)
	}
	if calls := f.Calls(t); calls != nil {
		t.Errorf("no image: Prepare invoked the CLI: %q", calls)
	}
}

const containerListed = `[{"id":"gitlab-ab-cd-42","configuration":{"creationDate":"2026-10-02T11:59:00Z","labels":{}}}]`

func TestPrepareSweepFailureOnlyWarns(t *testing.T) {
	x, f, _, stderr := newExecutor(t, fakecli.Options{List: "not json"}, Env{"CUSTOM_ENV_CI_JOB_IMAGE": "i"})
	if err := x.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare = %v, want the job to start despite the failed sweep", err)
	}
	if want := []string{"list", "delete", "run", "exec", "exec"}; !slices.Equal(f.Verbs(t), want) {
		t.Errorf("Prepare called %q, want %q", f.Verbs(t), want)
	}
	if !strings.Contains(stderr.String(), "WARNING: orphan sweep:") {
		t.Errorf("stderr = %q, want the sweep failure as a warning", stderr)
	}
}

func TestPrepareRemoveFailureFails(t *testing.T) {
	x, f, _, _ := newExecutor(t, fakecli.Options{DeleteRC: 1, List: containerListed}, Env{"CUSTOM_ENV_CI_JOB_IMAGE": "i"})
	err := x.Prepare(context.Background())
	var je *JobError
	if err == nil || errors.As(err, &je) {
		t.Fatalf("Prepare = %v, want a system error", err)
	}
	if slices.Contains(f.Verbs(t), "run") {
		t.Errorf("Prepare started a container after the failed removal: %q", f.Verbs(t))
	}
}

func TestCleanupFailure(t *testing.T) {
	x, _, _, _ := newExecutor(t, fakecli.Options{DeleteRC: 1, List: containerListed}, nil)
	if err := x.Cleanup(context.Background()); err == nil {
		t.Error("Cleanup returned no error for a container that stays")
	}
	x, _, _, _ = newExecutor(t, fakecli.Options{}, nil)
	delete(x.Env, "CUSTOM_ENV_CI_JOB_ID")
	if err := x.Cleanup(context.Background()); err == nil {
		t.Error("Cleanup without a job ID returned no error")
	}
}

func TestExitCodeWriteFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no", "such", "dir", "exit")
	x, _, _, stderr := newExecutor(t, fakecli.Options{}, Env{"BUILD_EXIT_CODE_FILE": missing})
	if got := x.ExitCode(&JobError{Code: 7, Err: errors.New("x")}); got != 11 {
		t.Errorf("ExitCode = %d, want the build failure code 11", got)
	}
	if !strings.Contains(stderr.String(), "WARNING: write BUILD_EXIT_CODE_FILE") {
		t.Errorf("stderr = %q, want the write failure as a warning", stderr)
	}
}

func TestLockedWriter(t *testing.T) {
	var buf bytes.Buffer
	w := &lockedWriter{w: &buf}
	const writers, lines = 8, 200
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			for range lines {
				if _, err := w.Write([]byte("writer " + strconv.Itoa(i) + " line\n")); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	got := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(got) != writers*lines {
		t.Fatalf("got %d lines, want %d", len(got), writers*lines)
	}
	for _, l := range got {
		if !strings.HasPrefix(l, "writer ") || !strings.HasSuffix(l, " line") {
			t.Fatalf("interleaved line %q", l)
		}
	}
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "script")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRun(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		execFail     bool
		code         int    // ExitCode result
		exitFile     string // BUILD_EXIT_CODE_FILE content
		stdout       string
	}{
		{name: "success", script: "echo hello", code: 0, stdout: "hello\n"},
		{name: "script fails", script: "exit 7", code: 11, exitFile: "7"},
		{name: "script exits 1", script: "echo before; false\nexit $?", code: 11, exitFile: "1", stdout: "before\n"},
		{name: "stdin does not eat the script", script: "read x || true\necho after", code: 0, stdout: "after\n"},
		{name: "missing container", script: "true", execFail: true, code: 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exitFile := filepath.Join(t.TempDir(), "exit")
			x, _, stdout, _ := newExecutor(t, fakecli.Options{ExecFail: tc.execFail}, Env{"BUILD_EXIT_CODE_FILE": exitFile})
			err := x.Run(context.Background(), writeScript(t, tc.script), "build_script")
			if got := x.ExitCode(err); got != tc.code {
				t.Errorf("ExitCode = %d (err %v), want %d", got, err, tc.code)
			}
			if stdout.String() != tc.stdout {
				t.Errorf("stdout = %q, want %q", stdout.String(), tc.stdout)
			}
			b, _ := os.ReadFile(exitFile)
			if string(b) != tc.exitFile {
				t.Errorf("BUILD_EXIT_CODE_FILE = %q, want %q", b, tc.exitFile)
			}
		})
	}
}

func TestRunWithoutScriptResult(t *testing.T) {
	x, _, _, _ := newExecutor(t, fakecli.Options{}, nil)
	// The stage cannot create its scratch directory below a regular file, so
	// it exits non-zero before it leaves an exit status behind.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	x.TmpDir = file
	err := x.Run(context.Background(), writeScript(t, "true"), "build_script")
	var je *JobError
	if err == nil || errors.As(err, &je) {
		t.Fatalf("Run = %v, want a system error", err)
	}
	if want := "reported exit status 1 without a script result"; !strings.Contains(err.Error(), want) {
		t.Errorf("Run = %q, want it to contain %q", err, want)
	}
	if got := x.ExitCode(err); got != 12 {
		t.Errorf("ExitCode = %d, want the system failure code 12", got)
	}
}

func TestRunLeavesNoScratch(t *testing.T) {
	x, _, _, _ := newExecutor(t, fakecli.Options{}, nil)
	_ = x.Run(context.Background(), writeScript(t, "true"), "build_script")
	if left, _ := filepath.Glob(x.TmpDir + "/.container-ci-*"); len(left) > 0 {
		t.Errorf("Run left %q", left)
	}
}

// A failed stage keeps its exit status in its scratch directory until the next
// exec collects it; the fake runs both execs, so Run itself leaves nothing.
func TestRunFailureLeavesExitStatusUntilCollected(t *testing.T) {
	tmp := t.TempDir()
	dir := tmp + "/.container-ci-x"
	cmd := exec.Command("/bin/sh", "-c", stageScript, "container-ci", dir)
	cmd.Stdin = strings.NewReader("exit 3\n")
	if err := cmd.Run(); err == nil {
		t.Fatal("stageScript of a failing script succeeded")
	}
	left, _ := filepath.Glob(tmp + "/.container-ci-*")
	if len(left) != 1 {
		t.Fatalf("stageScript left %q, want exactly one scratch directory", left)
	}
	if b, err := os.ReadFile(left[0] + "/exit"); err != nil || strings.TrimSpace(string(b)) != "3" {
		t.Fatalf("exit file = %q, %v; want 3", b, err)
	}
	out, err := exec.Command("/bin/sh", "-c", exitScript, dir).Output()
	if err != nil || strings.TrimSpace(string(out)) != "3" {
		t.Errorf("exitScript = %q, %v; want 3", out, err)
	}
	if left, _ := filepath.Glob(tmp + "/.container-ci-*"); len(left) != 0 {
		t.Errorf("exitScript left %q", left)
	}
}

func TestRunFailureLeavesNoScratchOnceCollected(t *testing.T) {
	x, _, _, _ := newExecutor(t, fakecli.Options{}, nil)
	_ = x.Run(context.Background(), writeScript(t, "exit 3"), "build_script")
	if left, _ := filepath.Glob(x.TmpDir + "/.container-ci-*"); len(left) > 0 {
		t.Errorf("Run left %q", left)
	}
}

// interrupt runs script in x with a fake CLI that does not forward signals,
// cancels the stage after delay, and returns Run's result.
func interrupt(t *testing.T, x *Executor, script string, delay time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(delay, cancel)
	start := time.Now()
	err := x.Run(ctx, writeScript(t, script), "build_script")
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Run returned after %v, want it to give up on the stage", elapsed)
	}
	var je *JobError
	if !errors.As(err, &je) || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("Run = %v, want an interrupted JobError", err)
	}
	return err
}

// assertGone fails unless the process whose PID the job wrote to pidFile has ended.
func assertGone(t *testing.T, pidFile string) {
	t.Helper()
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the job never started: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("job PID %q: %v", b, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	for range 50 {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("job process %d is still running after Run returned", pid)
}

// signalCalls returns the signal of every signalScript invocation among calls
// after the stage's own exec, failing the test on any other call.
func signalCalls(t *testing.T, f fakecli.Fake) []string {
	t.Helper()
	calls := f.Calls(t)
	if len(calls) < 1 {
		t.Fatalf("no CLI calls, want the stage's own exec")
	}
	var signals []string
	for _, c := range calls[1:] {
		want := []string{"exec", "--user", "0", "gitlab-ab-cd-42", "/bin/sh", "-c", signalScript}
		if len(c) < 9 || !slices.Equal(c[:7], want) {
			t.Fatalf("call %q, want a signal exec %q", c, want)
		}
		signals = append(signals, c[8])
	}
	return signals
}

// The fake CLI does not forward signals, so only stageScript's pid file and
// signalScript can stop the job.
func TestRunInterruptedTerm(t *testing.T) {
	x, f, _, stderr := newExecutor(t, fakecli.Options{RealExec: true}, nil)
	x.StopGrace = 2 * time.Second
	pidFile := filepath.Join(t.TempDir(), "job.pid")
	// exec drops the script's pipes, which would otherwise hold Run until the CLI's WaitDelay.
	interrupt(t, x, "echo $$ > "+pidFile+"\nexec sleep 30 >/dev/null 2>&1", 500*time.Millisecond)
	if want := []string{"TERM"}; !slices.Equal(signalCalls(t, f), want) {
		t.Errorf("signals = %q, want %q", signalCalls(t, f), want)
	}
	assertGone(t, pidFile)
	if !strings.Contains(stderr.String(), "WARNING: Stage build_script interrupted: sending SIGTERM") {
		t.Errorf("stderr = %q, want the interruption logged", stderr)
	}
}

func TestRunInterruptedKill(t *testing.T) {
	x, f, _, stderr := newExecutor(t, fakecli.Options{RealExec: true}, nil)
	x.StopGrace = 500 * time.Millisecond
	pidFile := filepath.Join(t.TempDir(), "job.pid")
	// The job ignores SIGTERM and keeps stdout open, so only SIGKILL ends it.
	interrupt(t, x, "trap '' TERM\necho $$ > "+pidFile+"\nwhile :; do sleep 0.2; done", 500*time.Millisecond)
	if want := []string{"TERM", "KILL"}; !slices.Equal(signalCalls(t, f), want) {
		t.Errorf("signals = %q, want %q", signalCalls(t, f), want)
	}
	assertGone(t, pidFile)
	if !strings.Contains(stderr.String(), "sending SIGKILL") {
		t.Errorf("stderr = %q, want the SIGKILL logged", stderr)
	}
}

func TestRunMissingScript(t *testing.T) {
	x, _, _, _ := newExecutor(t, fakecli.Options{}, nil)
	if got := x.ExitCode(x.Run(context.Background(), "/nonexistent", "build_script")); got != 12 {
		t.Errorf("ExitCode = %d, want the system failure code 12", got)
	}
}

func TestCleanup(t *testing.T) {
	x, f, _, _ := newExecutor(t, fakecli.Options{}, nil)
	if err := x.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"delete", "--force", "gitlab-ab-cd-42"}; !slices.Equal(call(t, f, 0), want) {
		t.Errorf("Cleanup called %q, want %q", call(t, f, 0), want)
	}
}

func TestSweep(t *testing.T) {
	list := `[
{"id":"gitlab-old","configuration":{"creationDate":"2026-09-30T12:00:00Z","labels":{"io.github.extra-code.container-ci.owner":"container-ci","io.github.extra-code.container-ci.prefix":"gitlab"}}},
{"id":"gitlab-new","configuration":{"creationDate":"2026-10-02T11:00:00Z","labels":{"io.github.extra-code.container-ci.owner":"container-ci","io.github.extra-code.container-ci.prefix":"gitlab"}}},
{"id":"gitlab-x-old","configuration":{"creationDate":"2026-09-30T12:00:00Z","labels":{"io.github.extra-code.container-ci.owner":"container-ci","io.github.extra-code.container-ci.prefix":"gitlab-x"}}},
{"id":"gitlab-foreign","configuration":{"creationDate":"2026-09-30T12:00:00Z","labels":{}}},
{"id":"gitlab-noprefix","configuration":{"creationDate":"2026-09-30T12:00:00Z","labels":{"io.github.extra-code.container-ci.owner":"container-ci"}}},
{"id":"gitlab-otherowner","configuration":{"creationDate":"2026-09-30T12:00:00Z","labels":{"io.github.extra-code.container-ci.owner":"someone-else","io.github.extra-code.container-ci.prefix":"gitlab"}}}
]`
	x, f, _, _ := newExecutor(t, fakecli.Options{List: list}, nil)
	removed, err := x.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"gitlab-old"}; !slices.Equal(removed, want) {
		t.Errorf("Sweep removed %q, want %q", removed, want)
	}
	if want := []string{"delete", "--force", "gitlab-old"}; !slices.Equal(call(t, f, 1), want) {
		t.Errorf("Sweep called %q, want %q", call(t, f, 1), want)
	}
}

func TestExitCodeDefaults(t *testing.T) {
	x := &Executor{Env: Env{}, Stderr: &bytes.Buffer{}}
	if got := x.ExitCode(errors.New("boom")); got != 2 {
		t.Errorf("system failure without SYSTEM_FAILURE_EXIT_CODE = %d, want 2", got)
	}
	if got := x.ExitCode(&JobError{Code: 3, Err: errors.New("x")}); got != 1 {
		t.Errorf("job failure without BUILD_FAILURE_EXIT_CODE = %d, want 1", got)
	}
}
