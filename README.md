<!-- doc_type: readme -->

<img src="packaging/describe/logo.svg" alt="container-ci logo" width="128">

# container-ci

container-ci runs every GitLab CI job in its own fresh Apple `container` on a Mac. The container
starts with the job and is gone when the job ends.

It is for operators of Apple silicon Mac CI hosts whose GitLab Runner uses a custom executor.
The command `gitlab-container-executor` is that executor. It drives the runtime only through the
`container` CLI.

container-ci is an independent project. It is not affiliated with or endorsed by GitLab Inc.

## Requirements

- An Apple silicon Mac. Requires Apple `container` 1.0.0 or newer on macOS 26. Tested with 1.5.0.
- The container services running: `container system start`.
- GitLab Runner with the [custom executor](https://docs.gitlab.com/runner/executors/custom/).
- A job image that provides `/bin/sh`, `sleep`, `cat`, `mkdir`, `chmod`, `rm` and `kill`.
  `git` is needed for `get_sources`, `git-lfs` only for repositories that use Git LFS, and
  `bash` is optional.

## Install

There are four ways to get the binary. Pick one.

### OCX package

Check the signature of the exact version first, then install that version:

```bash
ocx package verify -p darwin/arm64 \
  --certificate-identity "https://github.com/extra-code/container-ci/.github/workflows/release.yml@refs/tags/v<version>" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/extra-code/container-ci:<version>

ocx package install --link ~/.container-ci/executor ghcr.io/extra-code/container-ci:<version>
```

`<version>` is a release such as `1.0.0`, not a rolling tag such as `latest`. The certificate
identity names the release tag that built the package.

Point the runner at the binary in the link, with the home directory written out, such as
`/Users/ci/.container-ci/executor/content/bin/gitlab-container-executor`. To upgrade, run both
commands again with the new version. The link moves, the path stays.

### GitHub release asset

Download `gitlab-container-executor_<version>_darwin_arm64.tar.gz` and `checksums.txt` from the
GitHub release, then check and unpack the archive:

```bash
shasum -a 256 -c checksums.txt --ignore-missing
tar -xzf gitlab-container-executor_<version>_darwin_arm64.tar.gz
```

Copy the binary to a directory the runner can execute, for example
`/usr/local/bin/gitlab-container-executor`.

### Go toolchain

```bash
go install github.com/extra-code/container-ci/cmd/gitlab-container-executor@latest
```

Use `@latest` or a release such as `@v1.0.0`. A build of a release reports that release's version.

### Source checkout

```bash
task build
```

This writes `build/bin/gitlab-container-executor`.

Whichever way you choose, `gitlab-container-executor version -json` prints
`{"version":…,"commit_date":…}`.

## Register the runner

```bash
gitlab-runner register --executor custom --name mac-runner-1
```

Then edit `config.toml` so that all four stages call the executor:

```toml
[[runners]]
  name = "mac-runner-1"
  executor = "custom"
  [runners.custom]
    config_exec = "/usr/local/bin/gitlab-container-executor"
    config_args = ["-default-image", "ubuntu:26.04", "-cache-dir", "/Users/ci/container-ci/cache", "-bin-dir", "/Users/ci/container-ci/linux-arm64", "config"]
    prepare_exec = "/usr/local/bin/gitlab-container-executor"
    prepare_args = ["-default-image", "ubuntu:26.04", "-cache-dir", "/Users/ci/container-ci/cache", "-bin-dir", "/Users/ci/container-ci/linux-arm64", "prepare"]
    run_exec = "/usr/local/bin/gitlab-container-executor"
    run_args = ["-default-image", "ubuntu:26.04", "-cache-dir", "/Users/ci/container-ci/cache", "-bin-dir", "/Users/ci/container-ci/linux-arm64", "run"]
    cleanup_exec = "/usr/local/bin/gitlab-container-executor"
    cleanup_args = ["-default-image", "ubuntu:26.04", "-cache-dir", "/Users/ci/container-ci/cache", "-bin-dir", "/Users/ci/container-ci/linux-arm64", "cleanup"]
```

The flags `-prefix`, `-container`, `-platform`, `-guest`, `-home` and `-runtime` must be identical
in the args of all four stages. So must the flags that shape the container: `-label`, `-cpus`
and `-memory`. The simplest way is the same args list everywhere, with only the verb at the end.
Flags go before the verb. The runner appends the script path and the stage name to `run_args`.

## How a job runs

| Runner stage | What the executor does |
|---|---|
| `config` | Reports `builds_dir` `/builds`, `cache_dir` `/cache`, the container name as hostname |
| `prepare` | Sweeps orphaned job containers and removes any container with the job's own name, starts the job container from the job's `image:`, checks that it runs `/bin/sh`, creates `/builds` as root with mode `1777` |
| `run` | Copies the stage script into the container and runs it with bash, falling back to sh |
| `cleanup` | Stops and deletes the job container |

With `GIT_STRATEGY: none` on an image without git, the runner's generated script still calls
`git` and logs `git: not found`. The job is unaffected.

## Usage

```text
gitlab-container-executor [flags] config               # config_exec
gitlab-container-executor [flags] prepare              # prepare_exec
gitlab-container-executor [flags] run <script> <stage> # run_exec
gitlab-container-executor [flags] cleanup              # cleanup_exec
gitlab-container-executor [flags] sweep                # remove orphaned job containers
gitlab-container-executor version [-json]              # print the build's version
```

| Flag | Default | Meaning |
|---|---|---|
| `-container` | `container` on `PATH`, else `/usr/local/bin/container` | container CLI |
| `-prefix` | `gitlab` | name prefix of job containers, matching `^[a-z0-9][a-z0-9-]*$` |
| `-default-image` | none | image for jobs without `image:` |
| `-allow-image` | all | allowed image pattern, `*` matches anything, repeatable |
| `-platform` | CLI default (`linux/arm64`) | platform of the job containers, as `os/arch` |
| `-cpus` | `4` | CPUs per job container, `0` is the CLI default |
| `-memory` | `8G` | memory per job container: empty, or a positive number, optionally followed by `K`, `M`, `G`, `T` or `P` (any case) and then an optional `B` |
| `-cache-dir` | none | host directory mounted writable at `/cache` |
| `-bin-dir` | none | host directory with a `gitlab-runner` binary for the image's platform, mounted read-only at `/opt/container-ci/bin` and put on `PATH` |
| `-guest` | `linux` | job kind: `linux` or `macos` (see [macOS jobs](#macos-jobs)) |
| `-runtime` | CLI default | runtime handler of job containers |
| `-label` | none | extra `key=value` label on job containers, for example to select a macOS guest, repeatable |
| `-home` | none | absolute path of the CI account's home directory in a macOS guest |
| `-orphan-age` | `24h` | age after which `prepare` and `sweep` remove a job container, above `0` |

### Exit codes

The executor validates its flags at start. An invalid value exits with `2`. A message for a value
the executor validates starts with `gitlab-container-executor: `. The flag parser words its own
errors. The runner sets `BUILD_FAILURE_EXIT_CODE` and `SYSTEM_FAILURE_EXIT_CODE`. Without them the
defaults are `1` and `2`.

| Outcome | Exit code | Runner reports |
|---|---|---|
| Script exits 0 | `0` | success |
| Script exits n ≠ 0 | `BUILD_FAILURE_EXIT_CODE`, and n goes to `BUILD_EXIT_CODE_FILE` | job failure with exit code n |
| Disallowed or invalid image, no image and no `-default-image`, stage interrupted by the runner | `BUILD_FAILURE_EXIT_CODE` | job failure |
| Container missing or not running, CLI failure, missing `container` CLI, missing `CUSTOM_ENV_CI_JOB_ID` | `SYSTEM_FAILURE_EXIT_CODE` | system failure, retried per `retry:` |
| Usage or configuration error | `2` | not a stage result |
| `sweep` could not remove containers or reach the CLI | `1` | not a stage result |

When the runner interrupts a stage, on cancel or timeout, the executor sends SIGTERM to the
stage's processes in the container. It sends SIGKILL 10 seconds later and stops waiting for the
stage after another 10 seconds. `cleanup` then removes the container.

### Validation

| Flag | Rule |
|---|---|
| `-prefix` | matches `^[a-z0-9][a-z0-9-]*$` |
| `-cpus` | at least `0` |
| `-memory` | empty, or a positive number, optionally followed by `K`, `M`, `G`, `T` or `P` (any case) and then an optional `B` |
| `-orphan-age` | above `0` |
| `-guest macos` | needs `-runtime` and an absolute `-home`, and takes no `-cache-dir` or `-bin-dir` |
| `-label` | `key=value`, with a key outside the reserved namespace |
| `-guest` | `linux` or `macos` |

An image name that starts with `-` or contains whitespace or control characters fails the job.

## Images, labels and names

`-allow-image` takes a pattern, and `*` matches anything. With no pattern every image is
allowed. A runner shared between projects or groups should set `-allow-image`.

The `-cache-dir` mount is writable and shared by every project the runner serves. One runner with
`-cache-dir` should serve only projects that trust each other.

Every job container is named `<prefix>-<runner short token>-<job id>`, lowercased, with every other
character mapped to `-`. Every job container carries these labels:

| Label | Value |
|---|---|
| `io.github.extra-code.container-ci.owner` | `container-ci` |
| `io.github.extra-code.container-ci.prefix` | the `-prefix` value |
| `io.github.extra-code.container-ci.runner` | the runner short token |
| `io.github.extra-code.container-ci.job` | the job id |
| `io.github.extra-code.container-ci.contract` | `1`, on macOS guests only: the runtime contract version |

The namespace `io.github.extra-code.container-ci.` is reserved. A `-label` key in it is rejected.

The label keys and the name format are a stable interface across releases. A change would orphan
running containers.

## Sweep orphans

`sweep` removes containers that carry the owner and prefix labels and are older than
`-orphan-age`. It prints the removed ids. `prepare` runs the same sweep before each job.

Run `sweep` periodically from launchd or cron to clear containers a crashed runner or host left
behind. `-orphan-age` must exceed the longest job timeout, or a running job is swept. Pass the same
`-prefix`, `-container` and `-orphan-age` as the runner's stages.

```bash
/usr/local/bin/gitlab-container-executor -prefix gitlab -orphan-age 48h sweep
```

## Run under launchd

Under launchd, `gitlab-runner` has a minimal `PATH`. A missing `container` CLI makes `prepare`,
`run` and `cleanup` print `ERROR: container CLI not found at <path>: …` and exit with
`SYSTEM_FAILURE_EXIT_CODE`. `sweep` exits `1`. Install Apple `container` or pass `-container` with
an absolute path. Either pass `-container /usr/local/bin/container` in every stage's args, or set
`PATH` in the runner's environment, for example under `EnvironmentVariables` in the launchd plist.

## macOS jobs

With `-guest macos` the executor starts a macOS guest through a runtime handler that meets the
runtime contract in [ADR 0004](docs/adr/adr_0004_one_repository_for_ci_integrations.md). Such a
runtime may need a `container` build other than a stock release.

| Aspect | Behaviour |
|---|---|
| Guest selection | `-runtime` and the runtime's own `-label` values. The image argument is `none`, and a job's `image:` is ignored with a warning |
| Paths | `builds_dir` is `<home>/builds`, `cache_dir` `<home>/cache`, and scratch files go to `<home>`. All are gone with the guest |
| Mounts | none: `-cache-dir` and `-bin-dir` are refused. A cache that outlives a job needs the runner's distributed cache |
| User | stages run as the CI account. Only signals to an interrupted stage run as `root` |
| Host limit | two guests per Mac. Register one runner per Mac with `limit = 2` ([why](docs/macos_guest_limit.md)). A start beyond the limit is a system failure |

```toml
[[runners]]
  name = "mac-runner-macos-1"
  executor = "custom"
  limit = 2
  [runners.custom]
    config_exec = "/usr/local/bin/gitlab-container-executor"
    config_args = ["-guest", "macos", "-home", "/Users/ci", "-runtime", "<runtime handler>", "-label", "<runtime label>=<value>", "config"]
    prepare_exec = "/usr/local/bin/gitlab-container-executor"
    prepare_args = ["-guest", "macos", "-home", "/Users/ci", "-runtime", "<runtime handler>", "-label", "<runtime label>=<value>", "prepare"]
    run_exec = "/usr/local/bin/gitlab-container-executor"
    run_args = ["-guest", "macos", "-home", "/Users/ci", "-runtime", "<runtime handler>", "-label", "<runtime label>=<value>", "run"]
    cleanup_exec = "/usr/local/bin/gitlab-container-executor"
    cleanup_args = ["-guest", "macos", "-home", "/Users/ci", "-runtime", "<runtime handler>", "-label", "<runtime label>=<value>", "cleanup"]
```

Replace the angle-bracket placeholders with the values of your runtime.

`-home` must be absolute. `config` reports the paths under `-home`, and `run` writes its scratch
files there.

The guest has to contain:

| Need | For |
|---|---|
| `/bin/sh`, `cat`, `mkdir`, `rm`, `kill` | every job |
| `git` | `get_sources`, unless `GIT_STRATEGY: none` |
| `git-lfs` | repositories that use Git LFS |
| `gitlab-runner` for darwin | caches and artifacts |

Building the guest is up to the runtime. This repository keeps no recipe for it.

## Limitations

- `services:` are ignored, with a warning in the job log.
- A cancelled or timed-out job fails its running stage with `BUILD_FAILURE_EXIT_CODE`. It is
  not a system failure, so the runner does not retry it.
- On cancellation, `run` sends SIGTERM to the job's processes and SIGKILL 10 seconds later. It
  gives up 10 seconds after that. Set the runner's `graceful_kill_timeout` to at least 20
  seconds. With a shorter value the runner kills the executor first, and the job keeps running
  until `cleanup` deletes the container.
- GitLab keeps the custom executor in maintenance mode, with security fixes and no new
  features. See [ADR 0001](docs/adr/adr_0001_cli_is_the_runtime_interface.md).
- macOS jobs are tested against the runtime contract, not against a real guest.

## Build and test

```bash
ocx exec -g build -- task test       # gofmt, go vet, go test -race
ocx exec -g build -- task build      # build/bin/gitlab-container-executor
ocx exec -g build -- task test:e2e   # a full job against the running container services
```

## Documentation

- [How a job reaches a container](docs/architecture.md)
- [The macOS guest limit](docs/macos_guest_limit.md)
- [Architecture decisions](docs/adr/)
- [GitLab acceptance jobs](test/gitlab/README.md)
- [Maintainers](MAINTAINERS.md)

## License

See [LICENSE](LICENSE).
