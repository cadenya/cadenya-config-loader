# Running in CI

`cadenya-config` is a standalone executable. It needs no Go toolchain, shell, jq,
or provider-specific environment variables after installation. GitHub Actions
uses a thin native Go adapter around the same command implementation.

## Install a pinned version

Once the repository has a release, download the archive for your OS and
architecture from [Releases](https://github.com/cadenya/cadenya-config-loader/releases).
Archives contain `cadenya-config` (or `cadenya-config.exe`), documentation, and
licenses. Verify the downloaded file against that release's `checksums.txt`:

```sh
# Linux; replace VERSION with the released version, without a leading v.
sha256sum --check --ignore-missing checksums.txt
tar -xzf cadenya-config_VERSION_linux_amd64.tar.gz
./cadenya-config --version
```

On macOS, use `shasum -a 256 -c checksums.txt`; on Windows, compare
`Get-FileHash -Algorithm SHA256` with the corresponding checksum. Only extract
after verification. For releases created by this repository's release workflow,
verify provenance as well:

```sh
gh attestation verify cadenya-config_VERSION_linux_amd64.tar.gz \
  --repo cadenya/cadenya-config-loader
```

Alternatively, with Go installed:

```sh
# Set VERSION to a published version such as v0.1.0, or a reviewed commit hash.
go install "github.com/cadenya/cadenya-config-loader/cmd/cadenya-config@${VERSION:?set VERSION}"
```

Either way, `cadenya-config --version` reports the release. Do not use an unpinned
`@latest` for deployment jobs.

## Common command contract

Inject `CADENYA_API_KEY` through the CI secret store. Store `bundleKey` in
`cadenya.yaml`; select the workspace through `CADENYA_WORKSPACE_ID` or a flag.

```sh
cadenya-config validate
cadenya-config plan --report-file plan.json
cadenya-config apply --operation-timeout 10m --report-file result.json
```

Commands are noninteractive. Exit 0 means success, and a plan containing changes
still exits 0. Exit 1 means the run failed (an invalid bundle, an API error, or a
partial apply). Exit 2 means the invocation was wrong (an unknown command or flag,
a bad value, or a missing setting such as the API key). Exit 130 means the process
stopped on SIGINT or SIGTERM. Logs from `log/slog` go to stderr; set
`--log-level` and `--log-format json` to fit your log collection. stdout defaults to human-readable output;
`--output json` gives the same compact JSON contract used by `--report-file`.
Progress and the final error go to stderr.

The JSON schema version is 1. Reports include `command`, `schemaVersion`, and
`error` on failure. Once remote discovery succeeds they also include the bundle,
workspace, planned operation counts, and each operation's `completed` flag.
Validation includes `valid: true` and a resource count when successful. Parsing,
configuration, discovery, and apply failures return JSON when command execution
has started. Invalid CLI syntax can fail before a report is available.

`--report-file` is relative to the process working directory, independently of
`--directory`. Its parent directory must exist. The destination is checked before
API calls. Reports are written with private permissions to a temporary file and
renamed over the destination; a previous report survives interrupted writes.
On platforms/filesystems that cannot atomically replace a destination, use a
unique report filename per job. Always inspect the job's exit status as well as
any existing report: an abruptly killed process cannot publish a final report.

Retries apply to idempotent requests only. `--timeout` limits each request;
`--operation-timeout` limits discovery and apply together, including backoff.
Cancellation stops subsequent operations and reports completed ones when the
runner gives the process enough time to exit. A timeout can leave the last
write's outcome unknown; rerun a fresh plan after checking remote state.

## Buildkite

Copy [the pipeline example](../examples/buildkite.pipeline.yml) into a consuming
repository. It uses native [command steps](https://buildkite.com/docs/pipelines/configure/step-types/command-step),
uploads JSON reports as artifacts, injects a Buildkite secret, and serializes
applies with a workspace/bundle concurrency group. It restricts apply to main
branch builds that are not pull requests. Run validation without secrets for PRs.

The agent must have a pinned CLI binary on PATH. `secrets` requires a configured
Buildkite secret; an agent environment hook or another secret manager can supply
the same `CADENYA_API_KEY` variable. Never commit the secret value to pipeline YAML.

The CLI does not read Buildkite metadata or use the Buildkite API. The same
commands work on GitLab CI, Jenkins, CircleCI, and local automation. Configure
their secret injection, artifact collection, and deployment serialization around
the common command contract above.

## GitHub Actions

See [action.yml](../action.yml) for inputs and outputs and
[the workflow example](../examples/github-workflow.yaml) for a consuming workflow.
The composite action builds its native adapter from the pinned action revision.
It exports `result`, `report-path`, and planned counts, including on partial
failures. To inspect outputs after an action failure, use a subsequent step with
`if: always()`; preserving the action's failing status keeps the job red.

Use a single deployment writer per workspace/bundle. Concurrency groups in
different CI providers do not coordinate with each other; choose one provider to
own each deployment scope.
