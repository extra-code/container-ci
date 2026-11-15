<!-- doc_type: explanation -->

# ADR: Every stage of a job runs in one job container; nothing runs on the host

## Metadata

**Status:** Accepted
**Date:** 2026-10-02
**Deciders:** Christian Semmelmann
**Domain Tags:** security, ci
**Supersedes:** N/A
**Superseded By:** N/A

## Context

A GitLab job runs in stages: `prepare_script`, `get_sources`, `restore_cache`,
`download_artifacts`, `build_script` and `step_*`, `after_script`, `archive_cache`,
`upload_artifacts_*`, `cleanup_file_variables`. `get_sources` needs `git`, and the cache and
artifact stages need the `gitlab-runner` binary on `PATH`. A job image does not necessarily
carry either. An executor that starts one container per job can solve this with a helper
container that shares the build volume.

## Decision Drivers

- Job-controlled code never runs on the CI host. `get_sources` runs the job's
  `hooks:pre_get_sources_script`, so it counts as job code.
- One VM per job: each Apple container is its own VM, and memory is the binding resource on a
  Mac.
- No per-job state on the host beyond the container.

## Considered Options

| Option | Pros | Cons |
|---|---|---|
| 1. All stages in the job container; `gitlab-runner` mounted read-only from a host directory | One VM per job; builds directory lives and dies with the container | Images need `git` (or `GIT_STRATEGY: none`) |
| 2. Source, cache and artifact stages on the host, builds directory shared by a same-path mount | Any image works | Runs job hooks on the host; host user writes into job state |
| 3. A helper container per job sharing a host-mounted builds directory | Any image works; job code stays in containers | Two VMs per job; a host directory per job; a shared-directory mount on the build hot path |

## Decision Outcome

**Option 1.** `prepare` starts one container with `/bin/sh` as entrypoint idling until SIGTERM.
Every stage runs in it through `container exec`.

`/builds` lives in the container's own filesystem. In a Linux container `prepare` creates it as
root with mode 1777, so the image's `USER` can create the project directory in it. `-cache-dir`
mounts a writable host directory at `/cache`, shared across jobs. `-bin-dir` mounts a host
directory holding a `gitlab-runner` binary for the image's platform. It is read-only at
`/opt/container-ci/bin`, which the stage wrapper puts first on `PATH`.

Each stage keeps a scratch directory holding its script, pid and exit files. It lives under
`/tmp` in a Linux container and under the CI account's home in a macOS guest. A macOS guest
gets no mounts, and its `/builds` equivalent is `<home>/builds`, created by the CI account.

Option 3 stays open for images without `git`. Nothing decided here prevents adding it behind a
flag.

### Consequences

- A Linux image needs `/bin/sh`, `sleep`, `cat`, `mkdir`, `chmod`, `rm` and `kill`, and `git` for
  `get_sources`. A macOS guest needs `/bin/sh`, `cat`, `mkdir`, `rm` and `kill`.
- Every job clones afresh, and `builds_dir_is_shared` is `false`.
- Removing the container removes all job state. `cleanup` and the orphan sweep are the only
  cleanup there is.
- The `gitlab-runner` binary in `-bin-dir` is the operator's to provide and update per platform.
