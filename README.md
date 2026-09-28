# Configure Cadenya in GitHub Actions

This repository is a composite GitHub Action. It downloads a pinned release of the [Cadenya CLI](https://github.com/cadenya/cadenya-cli) and runs `cadenya config` with your workflow inputs. The CLI owns YAML validation, planning, reconciliation, reports, and API access.

```yaml
- uses: actions/checkout@v4
- uses: cadenya/cadenya-config-loader@v0.2.0
  with:
    version: '1.10.0'
    command: plan
    directory: .
    api-key: ${{ secrets.CADENYA_API_KEY }}
```

Set `command` to `validate`, `plan` (the default), or `apply`. Put `cadenya.yaml` and a `.cadenya/` resource directory in the checked-out repository. See the [CLI bundle guide](https://github.com/cadenya/cadenya-cli/blob/main/docs/config-bundles.md) for the file format and reconciliation behavior.

The `version` input selects a CLI release containing `cadenya config` and defaults to the tested v1.10.0 release. `binary-path` uses a preinstalled CLI instead of downloading one. Downloads come from `cadenya/cadenya-cli` GitHub releases and are verified against that release's checksum manifest. Existing `@v0.1.2` workflows continue to use the original standalone binary because Git tags are immutable.

Inputs and outputs are declared in [action.yml](action.yml). The CLI's `cadenya config github-action` adapter writes the JSON result and GitHub outputs. This repository contains no Go package or standalone binary release.
