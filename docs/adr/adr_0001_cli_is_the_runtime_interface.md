<!-- doc_type: explanation -->

# ADR: The `container` CLI is the only interface to the runtime

## Metadata

**Status:** Accepted
**Date:** 2026-10-02
**Deciders:** Christian Semmelmann
**Domain Tags:** architecture, integration
**Supersedes:** N/A
**Superseded By:** N/A

## Context

The CI integrations, such as a GitLab custom executor, create a container per job, run commands
in it, and remove it. Apple container offers three ways in: the
`container` CLI, the XPC API of its apiserver, and the Swift packages behind both. One backend
serves every integration and every job kind. The macOS guest runtime plugs in as a further
runtime handler (`--runtime`).

## Decision Drivers

- Every host installs container from Apple's signed package. The backend must work against any
  release at or above the pin, without a rebuild per release.
- Works with any Apple `container` release from 1.0.0, tested with 1.5.0.
- Zero third-party dependencies, as in the team's other Go tools.
- Linux and macOS jobs differ only in the runtime handler and the image.

## Considered Options

| Option | Pros | Cons |
|---|---|---|
| 1. Drive the `container` CLI, read state through `--format json` | Stable public surface, works with any installed release, Go stdlib only, every call reproducible by hand | One process per operation, and the CLI's exit status is coarse (see ADR 0002) |
| 2. Speak XPC to the apiserver | No process per call | Private, unversioned API, needs cgo or a Swift shim, tied to one release |
| 3. A Swift backend linking the container packages | Full type safety | Second toolchain, links one release's internals, no Go reuse |

## Decision Outcome

**Option 1.** `internal/container` wraps the CLI: `run --detach`, `list --all --format json`,
`exec`, `delete --force`. Job containers carry labels in the `io.github.extra-code.container-ci.` namespace (`owner`, `prefix`,
`runner`, `job`, and `contract` on macOS guests). The orphan sweep selects by the owner and
prefix labels and the creation date from the JSON listing.

### Consequences

- A runtime handler other than `container-runtime-linux` needs no backend change beyond
  `Spec.Runtime`.
- Process start per call is negligible against a container boot.
- The JSON field names (`configuration.labels`, `configuration.creationDate`, `status.state`)
  are a dependency. `internal/container/testdata/list.json` records them from container 1.5.0.
- GitLab keeps the [custom executor](https://docs.gitlab.com/runner/executors/custom/) in
  maintenance mode. It gets security fixes but no new features. A runner feature that needs
  executor support may never reach this executor.
