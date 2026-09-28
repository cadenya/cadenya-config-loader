# Running configuration in CI

Use `cadenya config validate`, `cadenya config plan`, or `cadenya config apply` from a pinned [Cadenya CLI release](https://github.com/cadenya/cadenya-cli/releases). On GitHub Actions, use this repository's composite Action, which downloads and verifies that release for the runner's operating system and architecture.

```sh
cadenya config validate -C .
CADENYA_API_KEY=... cadenya config plan -C . --report-file plan.json
CADENYA_API_KEY=... cadenya config apply -C . --report-file result.json
```

Pin an Action tag or commit SHA in workflows. `action.yml` lists all inputs and outputs. Use `binary-path` if the runner has a reviewed CLI binary already installed.
