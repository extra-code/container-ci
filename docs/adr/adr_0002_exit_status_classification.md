<!-- doc_type: explanation -->

# ADR: A script leaves its exit status in the container; its absence is a system failure

## Metadata

**Status:** Accepted
**Date:** 2026-10-02
**Deciders:** Christian Semmelmann
**Domain Tags:** reliability, ci
**Supersedes:** N/A
**Superseded By:** N/A

## Context

The runner distinguishes two failures. A job failure (`BUILD_FAILURE_EXIT_CODE`) fails the job
with the script's exit code, written to `BUILD_EXIT_CODE_FILE` so that `allow_failure:exit_codes`
works. A system failure (`SYSTEM_FAILURE_EXIT_CODE`) marks the infrastructure as broken and
triggers `retry:` on `runner_system_failure`.

`container exec` 1.5.0 exits with the process's status, and 128+n on a signal. It exits **1 for its
own failures**, such as a missing container, a process that cannot start or a broken XPC
connection. Measured:

| Case | `container exec` exit |
|---|---|
| script `exit 7` | 7 |
| script killed by SIGTERM | 143 |
| `/nonexistent` as the command | 1 |
| container `nosuch` | 1 |

A script that exits 1 is indistinguishable from a runtime failure by the status alone. Executors
that map every non-zero status to a job failure hide broken hosts behind red jobs. Mapping 1 to a
system failure retries genuine test failures.

## Decision Drivers

- No misclassification in either direction for the common cases.
- Nothing installed in the image and nothing mounted from the host for it.
- Works unchanged for macOS guests that run `exec`.

## Considered Options

| Option | Pros | Cons |
|---|---|---|
| 1. Status only | Simplest | 1 is ambiguous |
| 2. Parse the CLI's `Error:` lines on stderr | No extra call | Ties correctness to message wording; stderr is also the job log |
| 3. Probe the container state after a failure | No wrapper | Cannot tell a script exit 1 from a process that never started in a running container |
| 4. A wrapper records the script's status in the container; a second `exec` reads it | Unambiguous: a status file exists only if the script ran to completion | One extra `exec` per failed stage |
| 5. Record the status in a host-mounted directory | No second `exec` | A mount per job; shared-directory mount coherence becomes part of correctness |

## Decision Outcome

**Option 4.** Each stage runs `/bin/sh -c <stage script> container-ci <dir>`. The directory `<dir>` is
`.container-ci-<random>` under `/tmp`, or under the CI account's home in a macOS guest:

1. The shell writes its PID to `<dir>/pid`. The job script arrives on stdin and is stored in
   `<dir>/script`. The directory has mode 0700.
2. The script runs from that file with bash, else sh, with stdin from `/dev/null`.
3. The script file is removed. On status 0 the directory is removed. Otherwise the status is
   written to `<dir>/exit`.

Exit 0 from `exec` is success. Any other status triggers a second `exec` that prints and removes
`<dir>/exit`. A status there is a job failure with that status. No status is a system failure, as when the
container is gone, the image has no shell or `exec` is broken. A stage interrupted by the runner (timeout,
cancel) is reported as a job failure, since the runner has already decided the job's outcome.

### Consequences

- Running the script from a file, not from stdin, keeps commands that read stdin from consuming
  the rest of the script.
- The script, which carries the job token, never stays in the container after its stage.
- A script that deletes the scratch directory or fills the disk turns its own failure into a
  system failure. Accepted.
- `task test:e2e` asserts the table above against the real CLI.
