<!-- doc_type: changelog -->

# Changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
This project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-11-15

### Added

- `gitlab-container-executor`, a GitLab Runner custom executor that runs each job on Apple `container`.
- The custom executor stages `config`, `prepare`, `run` and `cleanup`.
- One fresh container per job, started from the job's `image:` and deleted when the job ends.
- An image allow-list through `-allow-image` that rejects every image outside the patterns.
- A cache mount through `-cache-dir` and a read-only bin mount through `-bin-dir`.
- macOS guest jobs through a runtime handler that a runtime plugin provides.
- An orphan sweep in `prepare` that removes job containers left behind by earlier runs.
- A `sweep` verb that removes job containers older than `-orphan-age`, also run by `prepare`.
- Resource and placement flags `-cpus`, `-memory`, `-label` and `-platform` for job containers.
- A `version` verb that prints the build version, or JSON with `-json`.
- Exit codes that separate job failures from system failures, so the runner retries only the latter.
- Releases as a GitHub release asset with `checksums.txt` and build provenance.
- Releases as the signed OCX package `ghcr.io/extra-code/container-ci`.

[Unreleased]: https://github.com/extra-code/container-ci/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/extra-code/container-ci/releases/tag/v0.1.0
