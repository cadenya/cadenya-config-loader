# Changelog

## Unreleased

- CLI built on urfave/cli v3 and the Cadenya Go SDK.
- Strict YAML loading, local references, and bundle ownership labels.
- Automatic pagination, archived resource discovery, dependency ordering,
  deletion checks, and assignment removal before pruning.
- Offline validation, plans, dry runs, bounded request retries, operation
  deadlines, and versioned JSON reports, including partial failures.
- Memory layers and memory entries under `memoryLayers/`, with
  `external_id:` references from `memoryLayerAssignments` and detaching
  before a layer is deleted.
- Agent `state: draft | published`. Published agents keep a variation
  while theirs are replaced, and are unpublished before deletion.
- Validate checks model ID format and memory entry keys offline.
- Widgets under `widgets/`, with `external_id:` agent and variation
  references, origin checks, and the embed host in reports. Agents bound to a
  widget outside the bundle are never deleted.
- API errors include the field violations and reasons Cadenya returns, such as
  a missing scope, instead of only `validation failed`.
- Plan refuses deletes that would break resources outside the bundle (an
  assigned tool, memory layer, or sub-agent, a pinned variation, a bound agent)
  and deletes that would strip a published agent of its last variation.
- A delete that finds the resource already gone counts as done.
- Structured `log/slog` logs on stderr (`--log-level`, `--log-format`), with the
  API key redacted.
- Exit codes: 1 for failed runs, 2 for usage and configuration errors, 130 when
  interrupted.
- GitHub composite action with native outputs and job summaries.
- Buildkite and generic CI examples.
- Release archives for Linux, macOS, and Windows on amd64 and arm64, including
  checksums, dependency licenses, and build provenance in the release workflow.
