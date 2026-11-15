// SPDX-FileCopyrightText: 2026 Christian Semmelmann
// SPDX-License-Identifier: Apache-2.0

// gitlab-container-executor is a GitLab Runner custom executor that runs every
// job in its own Apple container (internal/gitlab on top of internal/container).
//
// Usage:
//
//	gitlab-container-executor [flags] config               # config_exec: print the executor's JSON
//	gitlab-container-executor [flags] prepare              # prepare_exec: start the job container
//	gitlab-container-executor [flags] run <script> <stage> # run_exec: run one stage's script in it
//	gitlab-container-executor [flags] cleanup              # cleanup_exec: remove the job container
//	gitlab-container-executor [flags] sweep                # remove orphaned job containers
//	gitlab-container-executor version [-json]              # print the build's version
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/extra-code/container-ci/internal/container"
	"github.com/extra-code/container-ci/internal/gitlab"
	"github.com/extra-code/container-ci/internal/version"
)

// verbs is this CLI's surface, in the order the usage text lists them.
var verbs = []struct{ name, desc string }{
	{"config", "config_exec: print the executor's JSON"},
	{"prepare", "prepare_exec: start the job container"},
	{"run", "run_exec: run <script> of <stage> in the job container"},
	{"cleanup", "cleanup_exec: remove the job container"},
	{"sweep", "remove job containers older than -orphan-age"},
	{"version", `print this build's version; -json for {"version":"...","commit_date":"..."}`},
}

// listFlag collects a repeatable string flag.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func printUsage(fs *flag.FlagSet) {
	out := fs.Output()
	fmt.Fprintln(out, "gitlab-container-executor - GitLab Runner custom executor on Apple container.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Usage: gitlab-container-executor [flags] verb [args]")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Verbs:")
	for _, v := range verbs {
		fmt.Fprintf(out, "  %-8s %s\n", v.name, v.desc)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Exit codes:")
	fmt.Fprintln(out, "  0   success")
	fmt.Fprintln(out, "  2   usage or configuration error")
	fmt.Fprintln(out, "  prepare, run, cleanup: the runner's BUILD_FAILURE_EXIT_CODE for a job failure,")
	fmt.Fprintln(out, "      SYSTEM_FAILURE_EXIT_CODE for a system failure (default 2), including a missing container CLI")
	fmt.Fprintln(out, "  sweep: 1 when it cannot remove containers or reach the container CLI")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Flags:")
	fs.PrintDefaults()
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	code := dispatch(ctx, os.Args[1:], os.Environ(), os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// defaultCLI returns the container executable on PATH, or the path Apple's
// installer uses.
func defaultCLI() string {
	if p, err := exec.LookPath("container"); err == nil {
		return p
	}
	return "/usr/local/bin/container"
}

// needsCLI reports whether verb runs the container CLI.
func needsCLI(verb string) bool {
	switch verb {
	case "prepare", "run", "cleanup", "sweep":
		return true
	}
	return false
}

// dispatch is the CLI test seam: arguments and environment in, an exit code out.
func dispatch(ctx context.Context, args, environ []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gitlab-container-executor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printUsage(fs) }
	var allow, labels listFlag
	cli := fs.String("container", defaultCLI(), "container CLI executable")
	prefix := fs.String("prefix", "gitlab", "name prefix of job containers")
	image := fs.String("default-image", "", "image for jobs that name none")
	fs.Var(&allow, "allow-image", "allowed image pattern, '*' matches anything; repeatable; none allows all")
	platform := fs.String("platform", "", "container platform as os/arch (default: the CLI's)")
	cpus := fs.Int("cpus", 4, "CPUs per job container")
	memory := fs.String("memory", "8G", "memory per job container")
	cacheDir := fs.String("cache-dir", "", "host directory mounted at "+gitlab.CacheDir+" (default: no mount)")
	binDir := fs.String("bin-dir", "", "host directory with a gitlab-runner binary for the image's platform, mounted read-only at "+gitlab.BinDir+" and put on PATH")
	guest := fs.String("guest", gitlab.GuestLinux, "job kind: "+gitlab.GuestLinux+" or "+gitlab.GuestMacOS+" (a macOS guest needs -runtime and -home and takes no mounts)")
	runtime := fs.String("runtime", "", "runtime handler of job containers (default: the CLI's)")
	fs.Var(&labels, "label", "extra label key=value on job containers, e.g. to select a macOS guest; repeatable")
	home := fs.String("home", "", "the CI account's home directory in a macOS guest; builds and cache go there")
	orphanAge := fs.Duration("orphan-age", 24*time.Hour, "age after which prepare and sweep remove a job container")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	verb, rest := fs.Arg(0), fs.Args()[min(1, fs.NArg()):]
	want := 0
	switch verb {
	case "run":
		want = 2
	case "version":
		var asJSON bool
		vfs := flag.NewFlagSet("version", flag.ContinueOnError)
		vfs.SetOutput(stderr)
		vfs.BoolVar(&asJSON, "json", false, "print JSON")
		if err := vfs.Parse(rest); err != nil {
			return 2
		}
		v, date := version.Get()
		if asJSON {
			_ = json.NewEncoder(stdout).Encode(struct {
				Version    string `json:"version"`
				CommitDate string `json:"commit_date,omitempty"`
			}{v, date})
		} else {
			fmt.Fprintln(stdout, v)
		}
		return 0
	case "":
		printUsage(fs)
		return 2
	}
	if len(rest) != want {
		fmt.Fprintf(stderr, "gitlab-container-executor: %s takes %d argument(s), got %d (flags go before the verb)\n", verb, want, len(rest))
		return 2
	}

	extra := map[string]string{}
	for _, l := range labels {
		k, v, ok := strings.Cut(l, "=")
		if !ok || k == "" {
			fmt.Fprintf(stderr, "gitlab-container-executor: -label %q: want key=value\n", l)
			return 2
		}
		extra[k] = v
	}
	v, _ := version.Get()
	x := &gitlab.Executor{
		CLI:          container.CLI{Path: *cli, Stderr: stderr},
		Guest:        *guest,
		Runtime:      *runtime,
		Labels:       extra,
		Home:         strings.TrimSuffix(*home, "/"),
		Env:          gitlab.EnvFrom(environ),
		Version:      v,
		Prefix:       *prefix,
		DefaultImage: *image,
		AllowImages:  allow,
		Platform:     *platform,
		CPUs:         *cpus,
		Memory:       *memory,
		CacheDir:     *cacheDir,
		BinDir:       *binDir,
		OrphanAge:    *orphanAge,
		Stdout:       stdout,
		Stderr:       stderr,
		Now:          time.Now,
	}
	if err := x.Validate(); err != nil {
		fmt.Fprintf(stderr, "gitlab-container-executor: %v\n", err)
		return 2
	}

	var err error
	if needsCLI(verb) {
		if _, lerr := exec.LookPath(*cli); lerr != nil {
			err = fmt.Errorf("container CLI not found at %s: install Apple container, or pass -container with its absolute path (a runner under launchd has a minimal PATH)", *cli)
			if verb == "sweep" {
				fmt.Fprintf(stderr, "gitlab-container-executor: %v\n", err)
				return 1
			}
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return x.ExitCode(err)
	}
	switch verb {
	case "config":
		err = x.Config(stdout)
	case "prepare":
		err = x.Prepare(ctx)
	case "run":
		err = x.Run(ctx, rest[0], rest[1])
	case "cleanup":
		err = x.Cleanup(ctx)
	case "sweep":
		var removed []string
		removed, err = x.Sweep(ctx)
		for _, id := range removed {
			fmt.Fprintln(stdout, id)
		}
		if err != nil {
			fmt.Fprintf(stderr, "gitlab-container-executor: %v\n", err)
			return 1
		}
	default:
		names := make([]string, len(verbs))
		for i, v := range verbs {
			names[i] = v.name
		}
		fmt.Fprintf(stderr, "gitlab-container-executor: unknown verb %q (use: %s)\n", verb, strings.Join(names, ", "))
		return 2
	}
	if err != nil {
		var je *gitlab.JobError
		if !errors.As(err, &je) || je.Code == 0 {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
		}
	}
	return x.ExitCode(err)
}
