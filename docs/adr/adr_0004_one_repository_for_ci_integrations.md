<!-- doc_type: explanation -->

# ADR: One repository for every CI integration on Apple container, named container-ci

## Metadata

**Status:** Accepted
**Date:** 2026-11-05
**Deciders:** Christian Semmelmann
**Domain Tags:** architecture, repository, naming
**Supersedes:** N/A
**Superseded By:** N/A

## Context

A Mac CI host runs jobs in fresh Apple containers: Linux containers through the default runtime,
macOS guests through a separate runtime plugin selected with `--runtime`. Two CI systems connect
to it: GitLab through a custom executor, GitHub through a connector that starts one ephemeral
runner per job. Both need the same operations: create an instance, run commands in it, remove
it, sweep orphans.

Three questions follow: where that shared backend lives, how the integrations relate to it, and
what the repository is called.

## Decision Drivers

- One backend for every integration and job kind, kept in `internal/` packages: no published
  library to version, no third-party dependencies.
- Linux jobs work on a stock container installation. They must not depend on the macOS runtime
  plugin's repository or release cycle.
- One toolchain per repository, one release lane, one set of ADRs for the shared failure model.
- A name that stays correct when an integration or a job kind is added.
- A runtime and this repository share a contract on the `container` CLI, no source and no release
  cycle. Each describes what it offers or needs and names no other tool.

## Considered Options

### Placement

| Option | Pros | Cons |
|---|---|---|
| 1. One repository, one Go module: the backend in `internal/container`, one `internal/<ci>` package and one `cmd/` per CI system | Backend changes and their consumers land in one commit; `internal/` stays private | The repository grows with every integration |
| 2. A Go module inside the macOS runtime plugin's repository | One repository for the macOS story | Mixes Swift and Go toolchains; Linux jobs ship from a repository they do not need |
| 3. One repository per CI system plus a backend module | Small repositories | The backend becomes a published, versioned module; cross-cutting changes span several merge requests |

### Name

| Option | Cons |
|---|---|
| `gitlab-container-executor` | Wrong once the GitHub connector joins |
| `macos-ci` | Wrong for Linux jobs, which are the first to ship |
| `container-ci` | Does not say that the runtime is Apple's or that the host is a Mac |

## Decision Outcome

**Placement Option 1, named `container-ci`.**

- *container* names the runtime the repository drives, and the only interface it uses: the
  `container` CLI ([ADR 0001](adr_0001_cli_is_the_runtime_interface.md)).
- *ci* names the purpose without naming a CI system or a guest OS.
- Each command carries the CI system in its own name: `gitlab-container-executor`, later the
  GitHub connector.

### Scope

| In scope | Out of scope |
|---|---|
| The backend over the `container` CLI | The runtimes themselves, the macOS runtime plugin included |
| One integration per CI system, for Linux and macOS jobs | Building or distributing images |
| Per-job lifecycle: create, exec, remove, orphan sweep, host limits | Installing container or provisioning the host |

### Contract with a macOS guest runtime

A runtime selected with `--runtime` that boots a macOS guest as the container's init process runs
macOS jobs when it meets this contract. It may need a `container` build other than a stock release.
Linux jobs do not.

| Area | The runtime offers | An integration |
|---|---|---|
| Start | `container run -d` returns once `container exec` reaches the guest; on failure the guest is stopped | Reports a failed start as a system failure and needs no readiness retry |
| Guest selection | Runtime-specific labels on `container run`; no image, with `none` as the image argument | Passes the operator's runtime and labels through |
| Run options | The run command, `--env`, `--workdir` and `--user` of `container run` do not reach the guest; `container exec` honours its own | Sets a job's environment, directory and user per `exec` |
| Host limit | A start beyond the host's guest limit fails and leaves no guest; two guests per host | Schedules jobs within the limit |
| Job status | `container exec` exits with the job's status while the guest runs; a stopped guest or a lost process yields no job status | Classifies by [ADR 0002](adr_0002_exit_status_classification.md): no recorded status is a system failure |
| Writable paths | The CI account's home directory and `TMPDIR`, gone with the container; the system volume is read-only | Keeps builds, caches and scratch files there |
| Host access | No mounts and no published ports | Expects `git`, `gitlab-runner` and every other job tool in the guest; a cache that outlives a job does not use a host directory |
| User | `container exec` runs as the CI account, `--user root` as root | Does not assume root |
| Removal | `container delete --force` removes the guest and its writes | Uses the same cleanup and sweep as for Linux jobs |

### Consequences

- A new CI system adds `internal/<ci>` and a `cmd/` entry. A new job kind adds a runtime handler
  to the backend's spec. Neither changes the repository's name or layout.
- A change to the contract changes this table and the backend in one commit.
- The name alone does not tell a reader that the runtime is Apple container on macOS. The first
  line of the README states it.
