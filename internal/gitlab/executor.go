// SPDX-FileCopyrightText: 2026 Christian Semmelmann
// SPDX-License-Identifier: Apache-2.0

// Package gitlab implements the stages of a GitLab Runner custom executor on
// top of package container: one container per job, every stage runs inside it.
package gitlab

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/extra-code/container-ci/internal/container"
)

// LabelNamespace starts every label key the executor sets; other labels must
// not use it.
const LabelNamespace = "io.github.extra-code.container-ci."

// Label keys on every container the executor creates, and their values:
// LabelOwner is always Owner; LabelPrefix, LabelRunner and LabelJob carry the
// -prefix flag, the runner's short token and the job ID; LabelContract marks a
// macOS guest with the Contract version it was started under.
const (
	LabelOwner    = LabelNamespace + "owner"
	LabelPrefix   = LabelNamespace + "prefix"
	LabelRunner   = LabelNamespace + "runner"
	LabelJob      = LabelNamespace + "job"
	LabelContract = LabelNamespace + "contract"
	Owner         = "container-ci" // value of LabelOwner
	Contract      = "1"            // value of LabelContract
)

// Driver names this executor in the config stage's driver block.
const Driver = "gitlab-container-executor"

// Paths inside the job container.
const (
	BuildsDir = "/builds"
	CacheDir  = "/cache"
	BinDir    = "/opt/container-ci/bin"
)

// Guest kinds. A macOS guest follows the runtime contract in
// docs/adr/adr_0004_one_repository_for_ci_integrations.md: no image, no mounts,
// and only the CI account's home directory is writable.
const (
	GuestLinux = "linux"
	GuestMacOS = "macos"
)

// idleScript keeps the container running until it receives SIGTERM.
const idleScript = `trap 'exit 0' TERM; while :; do sleep 3600 & wait $!; done`

// buildsScript creates the builds directory $1. It runs as root because the
// image's USER may not write to /, and mode 1777 lets that user create the
// project directory in it; without it get_sources fails with Permission denied.
const buildsScript = `mkdir -p "$1" && chmod 1777 "$1"`

// stageScript runs one stage. $1 is a fresh directory: the job script arrives
// on stdin and is stored there, then runs from that file with stdin from
// /dev/null. Its PID goes to $1/pid for signalScript. A failing script leaves
// its exit status in $1/exit; a succeeding one leaves nothing.
const stageScript = `d=$1
umask 077
mkdir -p "$d" && echo $$ > "$d/pid" && cat > "$d/script" || exit 1
if [ -d ` + BinDir + ` ]; then PATH=` + BinDir + `:$PATH; export PATH; fi
s=
for c in /usr/local/bin/bash /usr/bin/bash /bin/bash /usr/local/bin/sh /usr/bin/sh /bin/sh /busybox/sh; do
  if [ -x "$c" ]; then s=$c; break; fi
done
if [ -z "$s" ]; then echo "container-ci: no shell found in the image" >&2; exit 1; fi
"$s" "$d/script" </dev/null
rc=$?
rm -f "$d/script"
if [ "$rc" -eq 0 ]; then rm -rf "$d"; else echo "$rc" > "$d/exit"; fi
exit "$rc"`

// signalScript sends signal $1 to the process group of the stage whose
// scratch directory is $0; the stage shell leads that group, so it holds every
// process the job started. dash needs "--" before a negative PID and BusyBox
// rejects it, so signal 0 picks the form; the wrong form leaves the job running.
const signalScript = `IFS= read -r p < "$0/pid" || exit 1
if kill -s 0 -- "-$p" 2>/dev/null; then kill -s "$1" -- "-$p"; else kill -s "$1" "-$p"; fi`

// defaultStopGrace is how long an interrupted stage gets after SIGTERM, and
// again after SIGKILL, before Run gives up on it.
const defaultStopGrace = 10 * time.Second

// exitScript prints and removes the exit status stageScript left in $0.
const exitScript = `IFS= read -r c < "$0/exit" && rm -rf "$0" && echo "$c"`

// JobError is a failure the job causes. The runner reports it as a build
// failure; any other error is a system failure.
type JobError struct {
	Code int // exit status of the job script; 0 when no script ran
	Err  error
}

func (e *JobError) Error() string { return e.Err.Error() }
func (e *JobError) Unwrap() error { return e.Err }

// Env is the environment the runner passes to every stage.
type Env map[string]string

// EnvFrom parses KEY=value pairs as os.Environ returns them.
func EnvFrom(environ []string) Env {
	e := Env{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			e[k] = v
		}
	}
	return e
}

// Job returns the job's CI/CD variable key.
func (e Env) Job(key string) string { return e["CUSTOM_ENV_"+key] }

func (e Env) code(key string, fallback int) int {
	if n, err := strconv.Atoi(e[key]); err == nil {
		return n
	}
	return fallback
}

// Executor runs the stages of one job.
type Executor struct {
	CLI          container.CLI
	Guest        string            // GuestLinux or GuestMacOS; empty selects GuestLinux
	Runtime      string            // runtime handler; empty selects the CLI's default
	Labels       map[string]string // extra labels for the runtime, e.g. to select a macOS guest
	Home         string            // the CI account's home directory in a macOS guest
	Env          Env
	Version      string        // reported as the driver version
	Prefix       string        // container name prefix
	DefaultImage string        // used when the job names no image
	AllowImages  []string      // image patterns; '*' matches any run of characters; empty allows all
	Platform     string        // container platform; empty selects the CLI's default
	CPUs         int           // 0 selects the CLI's default
	Memory       string        // empty selects the CLI's default
	CacheDir     string        // host directory mounted at CacheDir; empty mounts none
	BinDir       string        // host directory mounted read-only at BinDir; empty mounts none
	OrphanAge    time.Duration // Sweep removes containers older than this
	StopGrace    time.Duration // wait after each signal to an interrupted stage; 0 selects 10s
	TmpDir       string        // scratch root in a Linux container; empty selects /tmp
	Stdout       io.Writer
	Stderr       io.Writer
	Now          func() time.Time
}

var (
	prefixPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	memoryPattern = regexp.MustCompile(`^([0-9]+)([kmgtp]b?)?$`)
)

// validMemory reports whether s is a positive size with an optional unit K, M, G, T or P; B may follow a unit.
func validMemory(s string) bool {
	m := memoryPattern.FindStringSubmatch(strings.ToLower(s))
	return m != nil && strings.Trim(m[1], "0") != ""
}

// invalidImage reports whether s starts with '-' or holds whitespace or control characters.
func invalidImage(s string) bool {
	return strings.HasPrefix(s, "-") || strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

// Validate reports a configuration the stages cannot run with.
func (x *Executor) Validate() error {
	for k := range x.Labels {
		if strings.HasPrefix(k, LabelNamespace) {
			return fmt.Errorf("label %s: the %s prefix is reserved for the executor's own labels", k, LabelNamespace)
		}
	}
	if !prefixPattern.MatchString(x.Prefix) {
		return fmt.Errorf("-prefix %q: want lowercase letters, digits and '-', starting with a letter or digit", x.Prefix)
	}
	if x.CPUs < 0 {
		return fmt.Errorf("-cpus %d: want 0 for the CLI's default or a positive number", x.CPUs)
	}
	if x.Memory != "" && !validMemory(x.Memory) {
		return fmt.Errorf("-memory %q: want a positive number with an optional unit K, M, G, T or P, such as 512M or 8G", x.Memory)
	}
	if x.OrphanAge <= 0 {
		return fmt.Errorf("-orphan-age %v: want a duration greater than zero, such as 24h", x.OrphanAge)
	}
	switch x.Guest {
	case "", GuestLinux:
		return nil
	case GuestMacOS:
	default:
		return fmt.Errorf("guest %q: want %s or %s", x.Guest, GuestLinux, GuestMacOS)
	}
	switch {
	case x.Runtime == "":
		return errors.New("a macOS guest needs -runtime")
	case !strings.HasPrefix(x.Home, "/"):
		return errors.New("a macOS guest needs -home, the CI account's absolute home directory in the guest")
	case x.CacheDir != "" || x.BinDir != "":
		return errors.New("a macOS guest takes no mounts: drop -cache-dir and -bin-dir")
	}
	return nil
}

func (x *Executor) macOS() bool { return x.Guest == GuestMacOS }

// buildsDir and cacheDir are the job's directories in the container.
func (x *Executor) buildsDir() string {
	if x.macOS() {
		return x.Home + "/builds"
	}
	return BuildsDir
}

func (x *Executor) cacheDir() string {
	if x.macOS() {
		return x.Home + "/cache"
	}
	return CacheDir
}

// scratchDir is where stages keep their scratch directories; a macOS guest
// writes nowhere outside the home directory.
func (x *Executor) scratchDir() string {
	if x.macOS() {
		return x.Home
	}
	if x.TmpDir != "" {
		return x.TmpDir
	}
	return "/tmp"
}

// rootUser is the exec user that runs as root. A Linux image may lack a passwd
// entry for root, so it gets the uid; the macOS contract names the account.
func (x *Executor) rootUser() string {
	if x.macOS() {
		return "root"
	}
	return "0"
}

func (x *Executor) logf(format string, args ...any) {
	fmt.Fprintf(x.Stderr, format+"\n", args...)
}

// Name returns the job container's name: <prefix>-<runner short token>-<job id>,
// lowercased, with every character outside [a-z0-9-] replaced by '-'.
func (x *Executor) Name() (string, error) {
	job := x.Env.Job("CI_JOB_ID")
	if job == "" {
		return "", errors.New("CUSTOM_ENV_CI_JOB_ID is not set; run this from a GitLab Runner custom executor")
	}
	parts := []string{x.Prefix}
	if t := x.Env.Job("CI_RUNNER_SHORT_TOKEN"); t != "" {
		parts = append(parts, t)
	}
	parts = append(parts, job)
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 'a' - 'A'
		}
		return '-'
	}, strings.Join(parts, "-")), nil
}

// Config writes the config stage's JSON to w.
func (x *Executor) Config(w io.Writer) error {
	name, err := x.Name()
	if err != nil {
		return err
	}
	type driver struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	return json.NewEncoder(w).Encode(struct {
		BuildsDir         string `json:"builds_dir"`
		CacheDir          string `json:"cache_dir"`
		BuildsDirIsShared bool   `json:"builds_dir_is_shared"`
		Hostname          string `json:"hostname"`
		Driver            driver `json:"driver"`
	}{x.buildsDir(), x.cacheDir(), false, name, driver{Driver, x.Version}})
}

// image returns the image the job runs in.
func (x *Executor) image() (string, error) {
	image := x.Env.Job("CI_JOB_IMAGE")
	if x.macOS() {
		if image != "" {
			x.logf("WARNING: image %s is ignored: this runner's labels select the macOS guest", image)
		}
		return "none", nil
	}
	if image == "" {
		image = x.DefaultImage
	}
	if image == "" {
		return "", &JobError{Err: errors.New("the job names no image and the runner has no default image: set image: in the job or -default-image on the runner")}
	}
	if invalidImage(image) {
		return "", &JobError{Err: fmt.Errorf("image %q is invalid: it must not start with '-' or contain whitespace or control characters", image)}
	}
	if len(x.AllowImages) == 0 {
		return image, nil
	}
	for _, p := range x.AllowImages {
		if globMatch(p, image) {
			return image, nil
		}
	}
	return "", &JobError{Err: fmt.Errorf("image %q is not allowed on this runner (allowed: %s)", image, strings.Join(x.AllowImages, ", "))}
}

// globMatch reports whether s matches pattern, where '*' matches any run of
// characters, '/' included.
func globMatch(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$").MatchString(s)
}

// Prepare starts the job container and waits until it runs commands.
func (x *Executor) Prepare(ctx context.Context) error {
	if s := x.Env.Job("CI_JOB_SERVICES"); s != "" && s != "[]" && s != "null" {
		x.logf("WARNING: services are not supported by this executor and are ignored")
	}
	image, err := x.image()
	if err != nil {
		return err
	}
	name, err := x.Name()
	if err != nil {
		return err
	}
	if removed, err := x.Sweep(ctx); err != nil {
		x.logf("WARNING: orphan sweep: %v", err)
	} else if len(removed) > 0 {
		x.logf("Removed orphaned containers: %s", strings.Join(removed, " "))
	}
	if err := x.CLI.Remove(ctx, name); err != nil {
		return err
	}

	labels := map[string]string{
		LabelOwner:  Owner,
		LabelPrefix: x.Prefix,
		LabelRunner: x.Env.Job("CI_RUNNER_SHORT_TOKEN"),
		LabelJob:    x.Env.Job("CI_JOB_ID"),
	}
	if x.macOS() {
		labels[LabelContract] = Contract
	}
	for k, v := range x.Labels {
		labels[k] = v
	}
	spec := container.Spec{
		Name:     name,
		Image:    image,
		Runtime:  x.Runtime,
		Platform: x.Platform,
		CPUs:     x.CPUs,
		Memory:   x.Memory,
		Labels:   labels,
	}
	// A macOS guest's init process is the guest itself and ignores the run
	// command; a Linux container without the idle loop exits at once.
	if !x.macOS() {
		spec.Entrypoint = "/bin/sh"
		spec.Args = []string{"-c", idleScript}
	}
	if x.CacheDir != "" {
		if err := os.MkdirAll(x.CacheDir, 0o755); err != nil {
			return fmt.Errorf("cache directory: %w", err)
		}
		spec.Mounts = append(spec.Mounts, container.Mount{Source: x.CacheDir, Target: CacheDir})
	}
	if x.BinDir != "" {
		spec.Mounts = append(spec.Mounts, container.Mount{Source: x.BinDir, Target: BinDir, ReadOnly: true})
	}

	x.logf("Starting container %s from %s", name, image)
	if err := x.CLI.Run(ctx, spec); err != nil {
		return err
	}
	var diag bytes.Buffer
	code, err := x.CLI.Exec(ctx, name, container.Process{Args: []string{"/bin/sh", "-c", "exit 0"}, Stderr: &diag})
	if err == nil && code != 0 {
		err = fmt.Errorf("exit status %d: %s", code, strings.TrimSpace(diag.String()))
	}
	if err != nil {
		return fmt.Errorf("container %s does not run /bin/sh: %w", name, err)
	}
	if x.macOS() {
		return nil // the CI account creates its builds directory in its own home
	}
	diag.Reset()
	code, err = x.CLI.Exec(ctx, name, container.Process{
		Args:   []string{"/bin/sh", "-c", buildsScript, "container-ci", BuildsDir},
		User:   "0",
		Stderr: &diag,
	})
	if err == nil && code != 0 {
		err = fmt.Errorf("exit status %d: %s", code, strings.TrimSpace(diag.String()))
	}
	if err != nil {
		return fmt.Errorf("container %s: create %s as root: %w", name, BuildsDir, err)
	}
	return nil
}

// Run executes the script of one stage in the job container.
func (x *Executor) Run(ctx context.Context, script, stage string) error {
	name, err := x.Name()
	if err != nil {
		return err
	}
	f, err := os.Open(script)
	if err != nil {
		return fmt.Errorf("stage %s: %w", stage, err)
	}
	defer f.Close()
	token := make([]byte, 8)
	if _, err := rand.Read(token); err != nil {
		return err
	}
	dir := x.scratchDir() + "/.container-ci-" + hex.EncodeToString(token)

	// A signal to the CLI does not reach the process it runs, so cancelling
	// its context alone leaves the job running in the container. execCtx
	// outlives ctx, and stop cancels it only once the signals have had no effect.
	execCtx, cancelExec := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelExec()
	stderr := &lockedWriter{w: x.Stderr}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		select {
		case <-done:
		case <-ctx.Done():
			x.stop(execCtx, name, dir, stage, stderr, done)
			cancelExec()
		}
	})
	code, err := x.CLI.Exec(execCtx, name, container.Process{
		Args:   []string{"/bin/sh", "-c", stageScript, "container-ci", dir},
		Stdin:  f,
		Stdout: x.Stdout,
		Stderr: stderr,
	})
	close(done)
	wg.Wait()
	if ctx.Err() != nil {
		return &JobError{Err: fmt.Errorf("stage %s: interrupted", stage)}
	}
	if err != nil {
		return fmt.Errorf("stage %s: %w", stage, err)
	}
	if code == 0 {
		return nil
	}

	var out, diag bytes.Buffer
	rc, err := x.CLI.Exec(ctx, name, container.Process{
		Args:   []string{"/bin/sh", "-c", exitScript, dir},
		Stdout: &out,
		Stderr: &diag,
	})
	status, convErr := strconv.Atoi(strings.TrimSpace(out.String()))
	if err != nil || rc != 0 || convErr != nil {
		return fmt.Errorf("stage %s: container %s reported exit status %d without a script result (%s)",
			stage, name, code, strings.TrimSpace(diag.String()))
	}
	return &JobError{Code: status, Err: fmt.Errorf("stage %s: script exited with status %d", stage, status)}
}

// stop sends SIGTERM and then SIGKILL to the processes of the stage in dir,
// each followed by StopGrace, logs each to log, and returns early once done is
// closed.
func (x *Executor) stop(ctx context.Context, name, dir, stage string, log io.Writer, done <-chan struct{}) {
	grace := x.StopGrace
	if grace <= 0 {
		grace = defaultStopGrace
	}
	for _, sig := range []string{"TERM", "KILL"} {
		fmt.Fprintf(log, "WARNING: Stage %s interrupted: sending SIG%s to its processes\n", stage, sig)
		sctx, cancel := context.WithTimeout(ctx, grace)
		// As root: the job may have started processes under another user.
		// A failure is not reported: the next signal or the CLI's cancellation follows.
		_, _ = x.CLI.Exec(sctx, name, container.Process{
			Args:   []string{"/bin/sh", "-c", signalScript, dir, sig},
			User:   x.rootUser(),
			Stderr: io.Discard,
		})
		cancel()
		select {
		case <-done:
			return
		case <-time.After(grace):
		}
	}
}

// lockedWriter serialises writes from the stage's output and the interruption log.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// Cleanup removes the job container.
func (x *Executor) Cleanup(ctx context.Context) error {
	name, err := x.Name()
	if err != nil {
		return err
	}
	return x.CLI.Remove(ctx, name)
}

// Sweep removes the containers this executor created with its prefix label that are
// older than OrphanAge, and returns their names.
func (x *Executor) Sweep(ctx context.Context) ([]string, error) {
	cs, err := x.CLI.List(ctx)
	if err != nil {
		return nil, err
	}
	ids := container.Select(cs, map[string]string{LabelOwner: Owner, LabelPrefix: x.Prefix}, x.Now().Add(-x.OrphanAge))
	return ids, x.CLI.Remove(ctx, ids...)
}

// ExitCode maps the result of a stage to the process exit code the runner
// expects. A JobError carrying a script status also writes that status to
// BUILD_EXIT_CODE_FILE.
func (x *Executor) ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var je *JobError
	if !errors.As(err, &je) {
		return x.Env.code("SYSTEM_FAILURE_EXIT_CODE", 2)
	}
	if f := x.Env["BUILD_EXIT_CODE_FILE"]; f != "" && je.Code != 0 {
		if werr := os.WriteFile(f, []byte(strconv.Itoa(je.Code)), 0o644); werr != nil {
			x.logf("WARNING: write BUILD_EXIT_CODE_FILE %s: %v", f, werr)
		}
	}
	return x.Env.code("BUILD_FAILURE_EXIT_CODE", 1)
}
