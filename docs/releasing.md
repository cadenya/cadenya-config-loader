# Releasing the Action

The Action has no binary to build. Its `version` input selects a published `cadenya/cadenya-cli` release that contains `cadenya config`.

1. Release the CLI and verify its archives, checksum manifest, and `cadenya config validate` command.
2. Decide whether this Action tag should require callers to pin the CLI version or provide a tested default in `action.yml`. Review the download and output adapter against that release.
3. Update the changelog and tag a new Action version. Do not move existing tags; consumers may pin either a tag or a commit SHA.
4. Run the Action on Linux, macOS, and Windows with `examples/basic` and inspect its JSON outputs.

The Action version and CLI version are independent. An Action release changes the wrapper or its default CLI pin; it never publishes a second CLI binary.
