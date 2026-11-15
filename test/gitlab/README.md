<!-- doc_type: how-to -->
<!-- doc_tier: integration -->

# GitLab acceptance jobs

`.gitlab-ci.yml` in this directory holds jobs that check the executor under a real GitLab Runner,
in the cases a passing job does not reach. Every job is manual.

## Run the jobs

1. Register a runner that uses this executor, as [Usage](../../README.md#usage) describes.
2. Push this directory as the root of an empty GitLab project. The runner must accept the
   project's untagged jobs.
3. Start each job from the pipeline page, and cancel `cancel` once it prints
   `cancel: waiting for the cancellation`.
4. After each job, check that `container list --all` shows no container of the job.

## Expected results

| Job | Status | The log shows |
|---|---|---|
| `non-root` | passed | `uid=65532`, the commit, `non-root: checkout and write as uid 65532` and `non-root: after_script ran` |
| `cancel` | canceled | `Stage … interrupted: sending SIGTERM`, `cancel: child received TERM`, `cancel: script received TERM` and `cancel: after_script ran` |
| `timeout` | failed, reason job timeout | `timeout: script received TERM`, and no `after_script` output |
| `script-timeout` | failed, reason job timeout | `script-timeout: script received TERM` and `script-timeout: after_script ran` |

The runner sees a cancellation at its next job update, which can be a minute after the API call.
A job that exceeds its `timeout:` ends without `after_script`. A job that exceeds
`RUNNER_SCRIPT_TIMEOUT` runs `after_script` in the time left.
