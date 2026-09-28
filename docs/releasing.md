# Releases

Release tooling uses GoReleaser 2.18.2. It builds static CLI binaries for Linux,
macOS, and Windows, each on amd64 and arm64. The action builds its own adapter
from source, so action and CLI versions can share a tag.

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
2. Commit the release notes and reviewed changes, then push a `vX.Y.Z` tag pointing
   at that commit. A prerelease tag such as `v0.1.0-rc.1` creates a prerelease.
3. The release workflow reruns verification, builds archives, creates checksums,
   generates GitHub provenance attestations, and publishes the GitHub release.
4. Inspect the published assets and verify a downloaded archive and attestation.

Publishing requires the repository's Actions permissions for contents,
attestations, and OIDC. Enable GitHub private vulnerability reporting before
announcing the package. Pin consuming actions to a reviewed commit SHA or release
tag; this workflow does not move floating major tags automatically.

The working tree alone does not create a hosted release. Repository creation,
pushing code/tags, and release publication are separate maintainer actions.
