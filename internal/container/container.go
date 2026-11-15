// SPDX-FileCopyrightText: 2026 Christian Semmelmann
// SPDX-License-Identifier: Apache-2.0

// Package container drives the Apple container CLI. Every operation is one CLI
// invocation; listings are read through `--format json`.
package container

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// stopGrace is how long a CLI invocation may run on after its context ends and
// it has received SIGTERM.
const stopGrace = 10 * time.Second

// Mount binds a host directory into a container.
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// Spec describes a container that Run creates and starts detached.
type Spec struct {
	Name       string
	Image      string
	Runtime    string // runtime handler; empty selects the CLI's default
	Platform   string // os/arch; empty selects the CLI's default
	CPUs       int    // 0 selects the CLI's default
	Memory     string // e.g. 8G; empty selects the CLI's default
	Labels     map[string]string
	Mounts     []Mount
	Entrypoint string
	Args       []string
}

// runArgs returns the CLI arguments that create and start s.
func (s Spec) runArgs() ([]string, error) {
	if s.Name == "" || s.Image == "" {
		return nil, errors.New("container spec needs a name and an image")
	}
	args := []string{"run", "--detach", "--progress", "none", "--name", s.Name}
	if s.Runtime != "" {
		args = append(args, "--runtime", s.Runtime)
	}
	if s.Platform != "" {
		args = append(args, "--platform", s.Platform)
	}
	if s.CPUs > 0 {
		args = append(args, "--cpus", strconv.Itoa(s.CPUs))
	}
	if s.Memory != "" {
		args = append(args, "--memory", s.Memory)
	}
	keys := make([]string, 0, len(s.Labels))
	for k := range s.Labels {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		args = append(args, "--label", k+"="+s.Labels[k])
	}
	for _, m := range s.Mounts {
		if strings.Contains(m.Source, ":") || strings.Contains(m.Target, ":") {
			return nil, fmt.Errorf("mount %s:%s: paths must not contain ':'", m.Source, m.Target)
		}
		v := m.Source + ":" + m.Target
		if m.ReadOnly {
			v += ":ro"
		}
		args = append(args, "--volume", v)
	}
	if s.Entrypoint != "" {
		args = append(args, "--entrypoint", s.Entrypoint)
	}
	// "--" ends the options, so an image name is never read as one.
	args = append(args, "--", s.Image)
	return append(args, s.Args...), nil
}

// Container is the part of a container listing entry this package reads.
type Container struct {
	ID            string        `json:"id"`
	Configuration Configuration `json:"configuration"`
	Status        Status        `json:"status"`
}

// Configuration is the part of a container's configuration this package reads.
type Configuration struct {
	CreationDate   time.Time         `json:"creationDate"`
	Labels         map[string]string `json:"labels"`
	RuntimeHandler string            `json:"runtimeHandler"`
}

// Status is the part of a container's status this package reads.
type Status struct {
	State string `json:"state"`
}

// Process is a command that Exec runs in a container.
type Process struct {
	Args   []string
	Env    []string  // KEY=value
	User   string    // name or uid[:gid]; empty runs as the image's user
	Stdin  io.Reader // nil runs the process without standard input
	Stdout io.Writer
	Stderr io.Writer
}

// CLI runs the container executable at Path.
type CLI struct {
	Path string
	// Stderr receives the CLI's diagnostics and the container ID Run prints.
	Stderr io.Writer
}

func (c CLI) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.Path, args...)
	// The runner signals the caller's process group on cancel and timeout. In
	// that group the CLI tries to forward the signal, fails, and prints the
	// failure into the job log; the caller stops the job's processes itself. A
	// CLI that outlives a killed caller ends when cleanup deletes its container.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = stopGrace
	cmd.Stderr = c.Stderr
	return cmd
}

// Run creates and starts the container s describes, detached.
func (c CLI) Run(ctx context.Context, s Spec) error {
	args, err := s.runArgs()
	if err != nil {
		return err
	}
	cmd := c.command(ctx, args...)
	cmd.Stdout = c.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("container run %s: %w", s.Name, err)
	}
	return nil
}

// List returns every container, running or not.
func (c CLI) List(ctx context.Context) ([]Container, error) {
	var out bytes.Buffer
	cmd := c.command(ctx, "list", "--all", "--format", "json")
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("container list: %w", err)
	}
	var cs []Container
	if err := json.Unmarshal(out.Bytes(), &cs); err != nil {
		return nil, fmt.Errorf("container list: decode the CLI's output: %w", err)
	}
	return cs, nil
}

// Exec runs p in the running container name and returns the exit status the
// CLI reports. The CLI exits 1 both when the process exits 1 and when it fails
// itself, so the status alone does not tell the two apart. err is non-nil only
// when the CLI could not run or ended through a signal.
func (c CLI) Exec(ctx context.Context, name string, p Process) (int, error) {
	args := []string{"exec"}
	if p.Stdin != nil {
		args = append(args, "--interactive")
	}
	if p.User != "" {
		args = append(args, "--user", p.User)
	}
	for _, e := range p.Env {
		args = append(args, "--env", e)
	}
	args = append(args, name)
	args = append(args, p.Args...)
	cmd := c.command(ctx, args...)
	cmd.Stdin = p.Stdin
	cmd.Stdout = p.Stdout
	if p.Stderr != nil {
		cmd.Stderr = p.Stderr
	}
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.Exited() {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return -1, fmt.Errorf("container exec %s: %w", name, err)
	}
	return 0, nil
}

// Remove stops and deletes the named containers. A name that no container
// carries is not an error.
func (c CLI) Remove(ctx context.Context, names ...string) error {
	if len(names) == 0 {
		return nil
	}
	cmd := c.command(ctx, append([]string{"delete", "--force"}, names...)...)
	cmd.Stderr = io.Discard
	runErr := cmd.Run()
	if runErr == nil {
		return nil
	}
	cs, err := c.List(ctx)
	if err != nil {
		return fmt.Errorf("container delete: %w; list: %w", runErr, err)
	}
	var left []string
	for _, ct := range cs {
		if slices.Contains(names, ct.ID) {
			left = append(left, ct.ID)
		}
	}
	if len(left) > 0 {
		return fmt.Errorf("container delete %s: %w", strings.Join(left, " "), runErr)
	}
	return nil
}

// Select returns the IDs of the containers that carry every label in labels
// and were created before cutoff.
func Select(cs []Container, labels map[string]string, cutoff time.Time) []string {
	var ids []string
	for _, ct := range cs {
		if !ct.Configuration.CreationDate.Before(cutoff) {
			continue
		}
		match := true
		for k, v := range labels {
			if ct.Configuration.Labels[k] != v {
				match = false
				break
			}
		}
		if match {
			ids = append(ids, ct.ID)
		}
	}
	return ids
}
