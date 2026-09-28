# Security

Use the latest patch release in a supported major version. Before 1.0, only the
latest minor release receives fixes.

Report vulnerabilities through [GitHub private vulnerability reporting](https://github.com/cadenya/cadenya-config-loader/security/advisories/new).
If that form is unavailable, ask a maintainer for a private reporting channel
without posting exploit details, credentials, or private resource data publicly.

Include the affected version, a minimal reproduction, and the impact. Never send
a working production API key. Response times depend on maintainer availability.

## Deployment guidance

- Store API keys in the CI secret store and expose `CADENYA_API_KEY` only to trusted jobs.
- Use read-only scopes for plans and management scopes for applies.
- Serialize applies by workspace and bundle. GitHub and Buildkite examples include this.
- Pin the action to a reviewed commit or install a specific CLI release.
- Verify release checksums and, when available, GitHub artifact attestations.
- Treat bundle YAML as deployment code: review changes before applying them.

Apply is not transactional. A network timeout can leave a write's outcome
unknown. Read the report, investigate the API state, and run a fresh plan before
retrying. The CLI does not retry POST or PATCH requests automatically.
