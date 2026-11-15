// SPDX-FileCopyrightText: 2026 Christian Semmelmann
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package main

// The end-to-end test drives the built binary through the runner's stage
// sequence against the real container CLI. It needs running container system
// services and the image in CONTAINER_CI_E2E_IMAGE (default ubuntu:26.04).

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestE2E(t *testing.T) {
	cli, err := exec.LookPath("container")
	if err != nil {
		t.Skip("container CLI not on PATH")
	}
	if err := exec.Command(cli, "system", "status").Run(); err != nil {
		t.Skip("container system services are not running")
	}
	image := os.Getenv("CONTAINER_CI_E2E_IMAGE")
	if image == "" {
		image = "ubuntu:26.04"
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "gitlab-container-executor")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cache := filepath.Join(dir, "cache")
	jobID := fmt.Sprintf("e2e%d", time.Now().UnixNano())
	exitFile := filepath.Join(dir, "exit")
	env := append(os.Environ(),
		"CUSTOM_ENV_CI_JOB_ID="+jobID,
		"CUSTOM_ENV_CI_JOB_IMAGE="+image,
		"BUILD_FAILURE_EXIT_CODE=11",
		"SYSTEM_FAILURE_EXIT_CODE=12",
		"BUILD_EXIT_CODE_FILE="+exitFile,
	)
	flags := []string{"-container", cli, "-cpus", "2", "-memory", "1G", "-cache-dir", cache}

	stage := func(args ...string) (int, string) {
		t.Helper()
		var out bytes.Buffer
		cmd := exec.Command(bin, append(flags, args...)...)
		cmd.Env = env
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return code, out.String()
	}
	script := func(body string) string {
		p := filepath.Join(dir, fmt.Sprintf("script-%d", time.Now().UnixNano()))
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	if code, out := stage("config"); code != 0 || !strings.Contains(out, `"builds_dir":"/builds"`) {
		t.Fatalf("config = %d\n%s", code, out)
	}
	t.Cleanup(func() { stage("cleanup") })
	if code, out := stage("prepare"); code != 0 {
		t.Fatalf("prepare = %d\n%s", code, out)
	}

	if code, out := stage("run", script("mkdir -p /builds/p && cd /builds/p && echo hi > f && cat f && echo c > /cache/marker"), "build_script"); code != 0 || !strings.Contains(out, "hi") {
		t.Errorf("succeeding stage = %d\n%s", code, out)
	}
	if b, err := os.ReadFile(filepath.Join(cache, "marker")); err != nil || string(b) != "c\n" {
		t.Errorf("cache mount: %q, %v", b, err)
	}
	if code, out := stage("run", script("cat /builds/p/f; exit 1"), "build_script"); code != 11 {
		t.Errorf("stage exiting 1 = %d, want build failure 11\n%s", code, out)
	}
	if b, _ := os.ReadFile(exitFile); string(b) != "1" {
		t.Errorf("BUILD_EXIT_CODE_FILE = %q, want 1", b)
	}
	if code, out := stage("run", script("exit 42"), "after_script"); code != 11 {
		t.Errorf("stage exiting 42 = %d, want build failure 11\n%s", code, out)
	}
	if b, _ := os.ReadFile(exitFile); string(b) != "42" {
		t.Errorf("BUILD_EXIT_CODE_FILE = %q, want 42", b)
	}
	if code, out := stage("run", script("read line || true; echo after-read"), "build_script"); code != 0 || !strings.Contains(out, "after-read") {
		t.Errorf("stage reading stdin = %d\n%s", code, out)
	}
	if code, out := stage("run", script("ls -d /tmp/.container-ci-* 2>/dev/null | wc -l"), "build_script"); code != 0 || !strings.Contains(out, "1") {
		t.Errorf("scratch directories in the container (want only the running one):\n%s", out)
	}

	// The runner cancels a job, and ends it on timeout, by sending SIGTERM to run_exec.
	var out bytes.Buffer
	cmd := exec.Command(bin, append(flags, "run", script("trap 'echo got-term; exit 143' TERM; echo started; sleep 300 & wait $!"), "build_script")...)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 11 || !strings.Contains(out.String(), "got-term") {
		t.Errorf("interrupted stage = %v after %v, want build failure 11 and the script's TERM trap\n%s", err, time.Since(start), out.String())
	}
	if code, out := stage("run", script(`for p in /proc/[0-9]*; do tr '\0' ' ' < $p/cmdline; echo; done | grep -c '^sleep 300' || true`), "build_script"); code != 0 || strings.TrimSpace(out) != "0" {
		t.Errorf("job processes left after the interruption = %d\n%s", code, out)
	}

	if code, out := stage("cleanup"); code != 0 {
		t.Errorf("cleanup = %d\n%s", code, out)
	}
	if code, out := stage("run", script("true"), "build_script"); code != 12 {
		t.Errorf("stage after cleanup = %d, want system failure 12\n%s", code, out)
	}
}
