# Maintainers

- [Christian Semmelmann](mailto:christian.semmelmann@extraco.de)

## Releasing

1. In `CHANGELOG.md`, rename `[Unreleased]` to the version with its date, and add a new empty
   `[Unreleased]` section above it.
2. Update the compare links at the end of the file.
3. Merge the change into `main` through a pull request.
4. Tag `vX.Y.Z` on `main` and push the tag.

The release workflow builds the binary, writes `checksums.txt`, attests the artifacts and
publishes the GitHub release. It then pushes the OCX package `ghcr.io/extra-code/container-ci`
with the version without the `v`, signs it keyless and verifies the signature. A stable release
also moves the tags `X.Y`, `X` and `latest`. A pre-release moves none.

The `publish` and `announce` jobs run in the environment `release` and wait for approval. Approve
them under "Review deployments" on the run. The `claim` and `announce` workflows wait the same way.

If the `publish` or `announce` job fails, fix the cause and use "Re-run failed jobs" on the run.
The release job keeps the package as an artifact for 7 days. Never re-run the release job
itself, because its GitHub release already exists.

### OCX package setup

Before the first release tag:

1. Add a tag ruleset that lets only maintainers create or move tags matching `v*`.
2. Create the environment `release` with the secret `OCX_ANNOUNCE_TOKEN` and the variable
   `OCX_INDEX_FORK`. Add a required reviewer, and limit its deployments to the branch `main` and
   the tags `v*`.

After the first release made the package exist:

1. Make the package `ghcr.io/extra-code/container-ci` public in the package settings, and grant
   Actions access to this repository only.
2. Run the `claim` workflow by hand. It opens a pull request in the OCX index.
3. Once the claim is merged, set the repository variable `OCX_INDEX_ANNOUNCE` to `true`.
   From then on each stable release announces itself.
4. Run the `describe` workflow by hand. It runs by itself after each change under
   `packaging/describe/`.
5. Run the `announce` workflow by hand for tags published before the claim. Each announce run
   also copies the published description and logo into the index, so run it after `describe`.

The `claim` and `announce` runs first sync the fork named in `OCX_INDEX_FORK` with the index.
The sync fails when the index changed a workflow file, with the message
`refusing to allow a Personal Access Token to create or update workflow`. Sync the fork by hand
and run the workflow again:

```bash
gh repo sync extra-code/index --source ocx-sh/index
```
