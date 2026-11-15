# Maintainers

- [Christian Semmelmann](mailto:christian.semmelmann@extraco.de)

## Releasing

1. In `CHANGELOG.md`, rename `[Unreleased]` to the version with its date, and add a new empty
   `[Unreleased]` section above it.
2. Update the compare links at the end of the file.
3. Commit the change on `main`.
4. Tag `vX.Y.Z` on `main` and push the tag.

The release workflow builds the binary, writes `checksums.txt`, attests the artifacts and
publishes the GitHub release. It then pushes the OCX package `ghcr.io/extra-code/container-ci`
with the version without the `v`, signs it keyless and verifies the signature. A stable release
also moves the tags `X.Y`, `X` and `latest`. A pre-release moves none.

If the `publish` or `announce` job fails, fix the cause and use "Re-run failed jobs" on the run.
The release job keeps the package as an artifact for 7 days. Never re-run the release job
itself, because its GitHub release already exists.

### OCX package setup

Before the first release tag:

1. Add a tag ruleset that lets only maintainers create or move tags matching `v*`.
2. Create the environment `release` with the secret `OCX_ANNOUNCE_TOKEN` and the variable
   `OCX_INDEX_FORK`. Limit its deployments to the branch `main` and the tags `v*`.

The `describe` run that the merge of `packaging/describe/` starts fails while the package does
not exist yet. After the first release made the package exist:

1. Make the package `ghcr.io/extra-code/container-ci` public in the package settings, and grant
   Actions access to this repository only.
2. Run the `claim` workflow by hand. It opens a pull request in the OCX index.
3. Once the claim is merged, set the repository variable `OCX_INDEX_ANNOUNCE` to `true`.
   From then on each stable release announces itself.
4. Run the `announce` workflow by hand for tags published before the claim.
5. Run the `describe` workflow by hand. It runs by itself after each change under
   `packaging/describe/`.
