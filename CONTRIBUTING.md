# Contributing

Issues and pull requests are welcome. For bugs, include the CLI version, operating
system, command, and a minimal YAML example. Remove API keys and private data.

## Develop

Use a maintained Go toolchain and direnv:

```sh
direnv allow
direnv exec . make check build
```

The module's minimum Go version is 1.22. Go caches stay in `.direnv/`. Tests run
against local HTTP fixtures and do not need credentials. Copy `.env.example` to
`.env` only if you want to use a real workspace.

Before opening a pull request:

1. Run `make fmt check build` through direnv.
2. Add regression coverage for changed reconciliation behavior. Cover API failures
   and ownership boundaries whenever a change can write or delete resources.
3. Update the README, examples, and changelog for user-visible behavior.
4. Run `make release-check` with GoReleaser 2.18.2 when changing packaging.

## Compatibility

JSON reports carry `schemaVersion`. Add fields without removing or changing the
meaning of existing fields within a version. Exit codes are part of the contract
too: 0 for success, 1 for a failed run, 2 for usage and configuration errors, and
130 when interrupted (see `internal/command/exit.go`). Preserve these for CI
consumers. Flag parsing errors may occur before a report can be created.

Resource YAML follows the pinned Cadenya SDK types. Update `go.mod`, `go.sum`,
`NOTICE`, and dependency licenses together. Preserve API-owned secret and Liquid
templates verbatim. Do not add local environment interpolation implicitly.

Keep tests isolated from developer credentials. CI for pull requests must not
receive workspace credentials. Live tests require a dedicated disposable
workspace and an explicit opt-in.

## Releases

See [the release guide](docs/releasing.md). Contributions are accepted under the
repository's Apache-2.0 license. No contributor agreement or sign-off is required.
