# Releases

Release tooling uses GoReleaser 2.18.2. It builds static CLI binaries for Linux,
macOS, and Windows, each on amd64 and arm64. The GitHub Action downloads those
binaries, so every tag is both a CLI release and an action release.

## Verify locally

```sh
direnv exec . make check build
direnv exec . make release-check
direnv exec . make snapshot
```

`release-check` validates `.goreleaser.yaml`. `snapshot` requires a Git checkout
with at least one commit and creates local archives and checksums in `dist/`.
It does not publish anything. Extract the archive for your platform and run its
`--version` and `validate -C examples/basic` commands.

Before a release, verify:

- CI passes on the release commit, including the oldest supported Go version.
- The changelog describes the release and any migration steps.
- `NOTICE` and `licenses/` match dependency upgrades.
- Snapshot archives contain the executable, LICENSE, NOTICE, and dependency licenses.
- Checksums verify, and the native archive's CLI runs.
- A disposable workspace passes the opt-in live acceptance test when API behavior changes.

## Publish

1. Choose a semantic version. Before 1.0, document breaking changes in the minor
   release notes. After 1.0, breaking CLI or report changes require a major release.
2. Add a `## vX.Y.Z (YYYY-MM-DD)` section to `CHANGELOG.md`. The release workflow
   publishes that section as the release notes, and fails if it's missing.
3. Set the `version` input's default in `action.yml` to `X.Y.Z`. That's the release
   the action downloads, so the release workflow refuses a tag that doesn't match,
   and a test fails until it matches the newest CHANGELOG release.
4. Commit both with the reviewed changes, then push a `vX.Y.Z` tag pointing at that
   commit. A prerelease tag such as `v1.2.0-rc.1` creates a prerelease.
5. The release workflow reruns verification, builds archives, creates checksums,
   generates GitHub provenance attestations, and publishes the GitHub release. Then
   it runs the action on Linux, macOS, and Windows, downloading the new release the
   way consumers do.
6. Inspect the published assets and verify a downloaded archive and attestation.

The release job needs the Actions permissions it declares: contents, attestations,
and OIDC. The workflow doesn't move floating major tags such as `v0`, so consumers
pin a release tag or a commit SHA.
