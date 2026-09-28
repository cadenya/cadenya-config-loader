# Configure Cadenya

A GitHub Action that makes a Cadenya workspace match the YAML in your repository. Tool sets, tools, memory layers, memory entries, agents, agent variations, and widgets live in `.cadenya/`. Open a pull request and the action checks it against the workspace. Merge it and the action applies it.

There's no state file. The workspace is the state, and a label says which parts of it are yours. (If you've used Terraform, it's that idea without the `.tfstate`.)

The action runs `cadenya-config`, a Go CLI you can also run on your laptop or in another CI system. [Running it outside GitHub Actions](#running-it-outside-github-actions) covers that.

## Quick start

Three things go in your repository: a bundle, a secret, and a workflow.

### 1. Add a bundle

Copy [examples/basic](examples/basic/) to the root of your repository. It holds `cadenya.yaml` and a `.cadenya/` directory with one of everything: three tool sets, a tool, a memory layer with an entry, an agent with a variation, and a widget.

`cadenya.yaml` names the bundle and the workspace:

```yaml
bundleKey: support-tools
workspaceId: development
```

**The bundle key marks what this repository owns, and the action deletes anything with that key that isn't in `.cadenya/`.** Pick one no other repository uses. [How ownership works](#how-ownership-works) has the details.

Every Cadenya account starts with a `development` workspace, so the example works as written.

### 2. Add an API key

Create a Cadenya API key with `agents:manage`, `tools:manage`, `memory:manage`, and `widgets:manage`, plus the matching `:read` scopes. Save it as a repository secret:

```sh
gh secret set CADENYA_API_KEY
```

Plan needs only the four read scopes, if you'd rather give pull requests a key that can't write. A missing scope fails the step with `SCOPE_MISSING` and names both the scope Cadenya wanted (`required_scope=widgets:manage`) and the ones the key has.

### 3. Add the workflow

Save this as `.github/workflows/cadenya.yaml` (it's also [examples/github-workflow.yaml](examples/github-workflow.yaml)):

```yaml
# Copy to .github/workflows/cadenya.yaml. The workspace and bundle key come
# from cadenya.yaml, so the workflow doesn't repeat them.
name: Configure Cadenya
on:
  pull_request:
    paths:
      - .cadenya/**
      - cadenya.yaml
      - .github/workflows/cadenya.yaml
  push:
    branches: [main]
    paths:
      - .cadenya/**
      - cadenya.yaml
      - .github/workflows/cadenya.yaml
  workflow_dispatch:
permissions:
  contents: read
jobs:
  # Every pull request gets an offline check. It needs no API key, so it's safe
  # for forks.
  validate:
    if: github.event_name == 'pull_request'
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      # Pin a release tag, or a reviewed commit SHA for stricter supply-chain control.
      - uses: cadenya/cadenya-config-loader@v0.1.2
        with:
          command: validate
  # Pull requests from branches in this repository also get a plan against the
  # workspace. Forks can't read secrets, so they stop at validate.
  plan:
    if: github.event_name == 'pull_request' && github.event.pull_request.head.repo.full_name == github.repository
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: cadenya/cadenya-config-loader@v0.1.2
        with:
          command: plan
          api-key: ${{ secrets.CADENYA_API_KEY }}
  apply:
    if: github.event_name != 'pull_request'
    runs-on: ubuntu-latest
    environment: development
    # One apply at a time for this workspace and bundle. Never cancel one halfway.
    concurrency:
      group: cadenya-development-support-tools
      cancel-in-progress: false
    steps:
      - uses: actions/checkout@v7
      - uses: cadenya/cadenya-config-loader@v0.1.2
        id: cadenya
        with:
          command: apply
          api-key: ${{ secrets.CADENYA_API_KEY }}
```

Here's what each event gets:

1. Every pull request that touches the bundle gets `validate`. It checks the YAML and every reference without calling Cadenya, so it needs no secret and runs on forks.
2. A pull request from a branch in your repository also gets `plan`, which reads the workspace and reports what apply would change. Forks can't read secrets, so they stop at step 1.
3. A push to `main` runs `apply`.

### What you'll see

Each run adds a summary to the job:

```
### Cadenya configuration

apply: Succeeded

| Create | Update | Delete | Detach | State change |
| --- | --- | --- | --- | --- |
| 9 | 0 | 0 | 0 | 1 |
```

That's the first apply of the example: nine creates, plus publishing the agent. After that, a clean apply updates all nine resources and changes nothing else ([why every resource updates](#why-does-a-clean-plan-list-every-resource)).

The step log gets a line for each write as it lands, with the resource, its canonical ID, and how long it took. A widget's line carries the host you embed:

```
level=INFO msg="operation succeeded" command=apply bundle=support-tools workspace=development action=create kind=widget resource="widget support-chat" id=wgt_01HXKD2E5NQM3T9AYWCF4BXRZE duration_ms=362 host=x7k2m9qd4wbn.widgets.cadenya.com
```

The step log also prints the full JSON report, operation by operation. On a pull request, that's where a plan's details are.

## Inputs

| Input | Default | What it does |
| --- | --- | --- |
| `command` | `plan` | `validate`, `plan`, or `apply` |
| `api-key` | `CADENYA_API_KEY` from the environment | The Cadenya API key. Plan and apply need it, validate doesn't |
| `workspace-id` | `workspaceId` in `cadenya.yaml` | The workspace ID or its alias, like `development` |
| `bundle-key` | `bundleKey` in `cadenya.yaml` | The label that marks what this bundle owns |
| `base-url` | `https://api.cadenya.com` | The API origin, without `/v1` |
| `directory` | `.` | The directory holding `cadenya.yaml` and `.cadenya/` |
| `config` | `cadenya.yaml` | The settings file, relative to `directory` |
| `resource-dir` | `.cadenya` | The resource directory, relative to `directory` |
| `allow-empty` | `false` | Lets an apply with an empty `.cadenya/` delete the whole bundle |
| `timeout` | `60s` | Timeout for each HTTP request |
| `operation-timeout` | `10m` | Deadline for the whole plan or apply |
| `retries` | `2` | Retries for reads and deletes. Creates and updates never retry |
| `log-level` | `info` | `debug`, `info`, `warn`, or `error` |
| `log-format` | `text` | `text` or `json` |
| `version` | The release this action version belongs to | Which `cadenya-config` release to download. See [Versions and runners](#versions-and-runners) |
| `binary-path` | None | Runs this binary instead of downloading one |

An input beats a `CADENYA_*` environment variable, and an environment variable beats `cadenya.yaml`. So one workflow can point the same bundle at several workspaces ([recipe](#several-workspaces)).

## Outputs

| Output | What it holds |
| --- | --- |
| `result` | The JSON report |
| `report-path` | Where the JSON report is on the runner |
| `creates`, `updates`, `deletes`, `detaches`, `state-changes` | The plan's counts |

The report has `schemaVersion: 1` and `command`. A plan or apply adds `bundleKey`, `workspaceId`, `applied`, `summary`, and `operations`. Each operation has its action, resource, canonical ID, and a `completed` flag. **The counts describe the plan, not what finished,** so after a failed apply, the `completed` flags show how far it got. A failure adds `error`, with the API key redacted. When Cadenya rejects a request, the error carries the field and the reason it gave, like `spec.model_config.model_id: model_id must be a reference key`.

A failed run fails the step, but the outputs are still set. Read them from a later step with `if: always()` ([recipe](#keep-the-report-when-a-run-fails)).

## Versions and runners

**Each version of the action runs the `cadenya-config` release it belongs to.** Pin `@v0.1.2` and the action downloads cadenya-config 0.1.2 for the runner's OS and architecture, checks it against the release's `checksums.txt`, and runs it. There's no Go to install and nothing to compile, so the action takes a couple of seconds.

Pin a release tag, or the commit SHA behind one if you want the action's code fixed too. `version` can run a different release, but each version's inputs and outputs are only tested against its own release, so leave it alone unless you mean it.

GitHub-hosted Ubuntu, macOS, and Windows runners all work, on x64 or ARM64. A self-hosted runner needs Bash, `curl`, and access to github.com. One that can't reach github.com can set `binary-path` to a `cadenya-config` binary you put there yourself.

## Recipes

### Several workspaces

Run the same bundle against each workspace. The bundle key only has to be unique within a workspace, so both workspaces can use `support-tools`:

```yaml
jobs:
  apply:
    strategy:
      matrix:
        workspace: [development, production]
    runs-on: ubuntu-latest
    environment: ${{ matrix.workspace }}
    concurrency:
      group: cadenya-${{ matrix.workspace }}-support-tools
      cancel-in-progress: false
    steps:
      - uses: actions/checkout@v7
      - uses: cadenya/cadenya-config-loader@v0.1.2
        with:
          command: apply
          api-key: ${{ secrets.CADENYA_API_KEY }}
          workspace-id: ${{ matrix.workspace }}
```

Give each [environment](https://docs.github.com/en/actions/managing-workflow-runs-and-deployments/managing-deployments/managing-environments-for-deployment) its own `CADENYA_API_KEY` secret, and add required reviewers to `production` if a person should approve those applies.

### Stop before deleting anything

Plan first, and stop the job if the plan deletes something:

```yaml
    steps:
      - uses: actions/checkout@v7
      - uses: cadenya/cadenya-config-loader@v0.1.2
        id: plan
        with:
          command: plan
          api-key: ${{ secrets.CADENYA_API_KEY }}
      - name: Refuse deletes
        if: steps.plan.outputs.deletes != '0'
        run: |
          echo "::error::This apply would delete ${{ steps.plan.outputs.deletes }} resources. Apply it by hand after review."
          exit 1
      - uses: cadenya/cadenya-config-loader@v0.1.2
        with:
          command: apply
          api-key: ${{ secrets.CADENYA_API_KEY }}
```

Apply builds its own plan, so something could change in between. For a hard guarantee, put the apply in an environment with required reviewers instead.

### Keep the report when a run fails

The report file survives a failed apply, and it's the record of what finished:

```yaml
      - uses: cadenya/cadenya-config-loader@v0.1.2
        id: cadenya
        with:
          command: apply
          api-key: ${{ secrets.CADENYA_API_KEY }}
      - uses: actions/upload-artifact@v7
        if: always()
        with:
          name: cadenya-report
          path: ${{ steps.cadenya.outputs.report-path }}
```

## How ownership works

Every resource the action writes gets a label: `bundle_key=<your bundle key>`. That label is the whole ownership model. Plan and apply list everything that carries it, compare that with `.cadenya/`, and act on the difference.

**Anything with your bundle key that isn't in `.cadenya/` gets deleted on the next apply.** That's the point: delete a file, apply, and the resource is gone. But it means the bundle key has to be yours alone. Pick one per repository (or one per bundle, if a repository has several) and don't share it.

The label also keeps the action away from everything else in the workspace:

1. It never adopts a resource without your label. If an external ID you're about to create already exists outside the bundle, plan fails and names it.
2. It never deletes a parent with children outside the bundle. An agent with a hand-made variation stays put, and so does a Bare or HTTP tool set holding someone else's tool, or a memory layer holding someone else's entry.
3. It never deletes something a resource outside the bundle depends on: a tool, tool set, memory layer, or sub-agent that another variation assigns, a variation another widget pins, or an agent another widget is bound to. Plan names the resource that's in the way. (Checking this lists every agent, variation, and widget in the workspace, so it only happens when the plan deletes something.)
4. It refuses to apply an empty `.cadenya/` unless you set `allow-empty: true`. An empty tree means "delete the whole bundle." That deserves a flag.

Changing the bundle key doesn't rename or move anything. It starts a new, empty scope, and the old resources keep the old label.

## Bundle layout

```text
cadenya.yaml                     # settings, optional
.cadenya/
  toolSets/
    ask-cadenya.yaml             # MCP
    frontend.yaml                # Bare
    frontend/
      fetch-form-state.yaml      # a tool in the frontend tool set
    petstore.yaml                # OpenAPI
  memoryLayers/
    playbooks.yaml
    playbooks/
      tool-set-setup.yaml        # an entry in the playbooks layer
  agents/
    ask-cadenya.yaml
    ask-cadenya/
      default.yaml               # a variation of the ask-cadenya agent
  widgets/
    support-chat.yaml            # embeds the ask-cadenya agent
```

One resource per file, as `.yaml` or `.yml`. A child directory takes its name from the parent's **filename**, even when the parent sets its own external ID. A child's identity includes its parent, so two agents can each have a `default` variation.

The loader is strict on purpose. It rejects unknown fields (typos inside an adapter included), unknown directories under `.cadenya/`, child directories with no parent file, symlinks, YAML aliases, and duplicate identities. It skips files that aren't YAML, so a `README.md` in there is fine.

A missing `toolSets/`, `memoryLayers/`, `agents/`, or `widgets/` directory means you want none of that type. **So removing `agents/` deletes every agent in the bundle.** The `.cadenya/` directory itself has to exist.

## Settings

`cadenya.yaml` sits in the action's `directory` (the repository root, by default):

```yaml
bundleKey: support-tools
workspaceId: development
baseUrl: https://api.cadenya.com
resourceDir: .cadenya
```

Every key is optional in the file, but `bundleKey` has to come from somewhere: the file, the `bundle-key` input, or `CADENYA_BUNDLE_KEY`. It's a label value: 1 to 63 letters, digits, `-`, `_`, or `.`, starting and ending with a letter or digit.

Changing the bundle key doesn't rename or move anything. It starts a new, empty scope, and the old resources keep the old label.

Keep the API key in a secret. It never belongs in `cadenya.yaml`.

## Resource files

Each file is the SDK's JSON shape, written as YAML: `metadata` and `spec`. The external ID comes from a top-level `externalId`, then `metadata.externalId`, then the filename without its extension. If you set both fields, they have to match. `metadata.name` defaults to the external ID.

**Renaming a file without an explicit external ID changes its identity.** Plan shows a delete and a create, not a rename.

### Tool set

`.cadenya/toolSets/frontend.yaml`:

```yaml
metadata:
  name: Frontend tools
  labels:
    team: frontend
spec:
  description: Tools executed by the frontend application
  adapter:
    type: bare
    bare: {}
```

Your own labels ride along with `bundle_key`. Setting `bundle_key` yourself to anything but the bundle key is an error.

MCP and OpenAPI tool sets need nothing but the adapter. Here's the Swagger Petstore, from `.cadenya/toolSets/petstore.yaml`:

```yaml
metadata:
  name: Swagger Petstore
spec:
  description: Find pets, place orders, and manage the store inventory
  adapter:
    type: openapi
    openapi:
      type: url
      url: https://petstore3.swagger.io/api/v3/openapi.json
```

Cadenya syncs the tools once the tool set exists (19 of them, for the Petstore). Every apply sends an update, and an update starts another sync. A sync that finds nothing new changes nothing.

### Tool

`.cadenya/toolSets/frontend/fetch-form-state.yaml`:

```yaml
metadata:
  name: Fetch form state
spec:
  description: Return the current form fields and their values
  parameters:
    type: object
    properties: {}
    additionalProperties: false
  config:
    type: bare
    bare: {}
```

Tools go under Bare and HTTP tool sets, and a tool's `config` type has to match its parent's adapter. MCP and OpenAPI tool sets sync their own tools, so there's nothing to write for them. `parameters` is required, and `{}` works for a tool that takes no arguments.

An HTTP pair looks like this: the tool set gets `adapter: {type: http, http: {baseUrl: ...}}`, and each tool gets `config: {type: http, http: {requestMethod: GET, path: /forms}}`.

### Agent

`.cadenya/agents/ask-cadenya.yaml`:

```yaml
externalId: cadenya-assistant
metadata:
  name: Ask Cadenya
spec:
  description: Answer questions about Cadenya and the current form
```

In the API, this agent is `cadenya-assistant`. Its variations still live in `agents/ask-cadenya/`, because the directory follows the filename.

### Agent state

An agent file takes one more top-level field, `state`: `draft` or `published`. The example publishes its agent:

```yaml
externalId: cadenya-assistant
state: published
metadata:
  name: Ask Cadenya
spec:
  description: Answer questions about Cadenya and the current form
```

Apply publishes after the agent's variations exist, because Cadenya won't publish an agent without one. **So `state: published` needs at least one variation in the bundle, and validate checks that.** An archived agent with a `state` gets unarchived first.

The same rule runs the other way. If an agent is published in Cadenya and the plan would delete its last variation, plan stops, even when the YAML leaves `state` out. Keep a variation, or set `state: draft`.

Leave `state` out and the action leaves the state alone, the same as any other field you don't write. New agents start as drafts.

### Variation

`.cadenya/agents/ask-cadenya/default.yaml`:

```yaml
metadata:
  name: Default
spec:
  systemPromptTemplate: |
    Help the user configure Cadenya. Consult the documentation tools for API
    questions, and fetch the current form state when needed.
  modelConfig:
    modelId: getting-started-key.openai-gpt-4-1
    temperature: 0
  assignments:
    - type: toolSetId
      toolSetId: external_id:ask-cadenya
    - type: toolId
      toolId: external_id:frontend/fetch-form-state
```

`modelId` is a reference key: your AI provider key's external ID, a dot, then the model's external ID. Or use its canonical `model_` ID. The model's external ID has no dots of its own, so GPT-4.1 is `openai-gpt-4-1`, not `gpt-4.1`. **`openai.gpt-4.1` fails, and validate tells you so before apply can.**

Every Cadenya account starts with a provider key whose external ID is `getting-started-key`, so the example works as written. A provider key you add has its own external ID. To see the models a workspace can use, list them (that takes the `models:read` scope).

### Memory layer

`.cadenya/memoryLayers/playbooks.yaml`:

```yaml
metadata:
  name: Support playbooks
spec:
  type: MEMORY_LAYER_TYPE_SKILLS
  description: How the assistant handles common configuration questions
```

`type` is required, and `MEMORY_LAYER_TYPE_SKILLS` is the one a bundle can create. (Episodic layers belong to the runtime.) **A layer's type can't change after it exists.** Plan stops if you try, so give the layer a new external ID to replace it.

### Memory entry

`.cadenya/memoryLayers/playbooks/tool-set-setup.yaml`:

```yaml
metadata:
  name: Set up a tool set
spec:
  key: skills/tool-set-setup
  description: Use when someone asks how to connect an API or MCP server to an agent.
  content: |
    1. Ask whether the API has an OpenAPI spec or an MCP server.
    2. For OpenAPI, point a tool set at the spec URL. Cadenya syncs it hourly.
    3. For MCP, use the server URL. Tools sync once the tool set exists.
    4. Filter the tools before assigning the tool set to a variation.
```

`key` is what the model passes to `get_memory`, and it defaults to the external ID. Keys can hold slashes and external IDs can't, so a hierarchical key like `skills/tool-set-setup` needs its own `key` field. Validate applies Cadenya's key rules: ASCII letters, digits, and `! - _ . * ' ( ) /`, no leading, trailing, or doubled slash, and nothing under `cadenya/` or `system/`.

For a skills layer, `description` is the "when to use this" line the model sees before it loads the body. Write it for the model.

To put a layer in a variation's memory cascade, add it to `memoryLayerAssignments`:

```yaml
  memoryLayerAssignments:
    - memoryLayerId: external_id:playbooks
      position: 0
```

Lower positions are consulted first.

### Widget

`.cadenya/widgets/support-chat.yaml`:

```yaml
metadata:
  name: Support chat
spec:
  agentId: external_id:cadenya-assistant
  originAllowlist:
    - http://localhost:3000
```

A widget embeds one agent. `originAllowlist` holds exact origins: a scheme, a host, and an optional port. No paths, no wildcards, and validate says so before Cadenya has to.

`variationId` pins every conversation to one of the agent's variations, as `external_id:<agent>/<variation>`. Leave it out and the agent's selection mode picks. To unpin, set `variationId: null`. **Delete a pinned variation and plan asks you to set `variationId` first**, since Cadenya won't delete a variation out from under its widget.

Widgets have no children, so `widgets/` holds files only. There's no `state` for widgets.

### References

Variations and widgets point at resources in the same bundle by external ID. Apply swaps in the canonical IDs as it creates things, so a variation can reference a tool created earlier in the same run:

| Assignment | Reference |
| --- | --- |
| `toolSetId` | `external_id:<tool set>` |
| `toolId` | `external_id:<tool set>/<tool>` |
| `subAgentId` | `external_id:<agent>` |
| `memoryLayerAssignments[].memoryLayerId` | `external_id:<memory layer>` |
| Widget `agentId` | `external_id:<agent>` |
| Widget `variationId` | `external_id:<agent>/<variation>` |

Validate checks every one of these before any API call. For a resource outside the bundle, use its canonical ID. Agent pools work the same way: reference them by ID. Bundles don't create them.

## What an update changes

Every update sends the name, the external ID, and the complete label map. For `spec`, the top-level fields in your YAML become the update mask:

1. A field you leave out keeps whatever value it has in Cadenya.
2. A field you include replaces the remote value whole, nested objects and all.
3. An empty value clears the field. `assignments: []` removes every assignment, and `{}`, `null`, `""`, `false`, and `0` clear the same way.

Memory entries follow the same rule, and `content` counts as a field: leave it out and the entry's body stays as it is.

Nothing is interpolated. `${SECRET_NAME}` references and Liquid templates reach Cadenya as written.

### Why does a clean plan list every resource?

Because every resource that exists gets an update on every apply, changed or not. Run plan right after an apply and you see:

```
update    toolSet ask-cadenya
update    toolSet frontend
update    toolSet petstore
update    tool frontend/fetch-form-state
update    memoryLayer playbooks
update    memoryEntry playbooks/tool-set-setup
update    agent cadenya-assistant
update    agentVariation cadenya-assistant/default
update    widget support-chat
Plan: 0 create, 9 update, 0 delete, 0 detach
```

There's no second `publish`. State changes only show up when the state differs.

**Plan lists operations, not a field-by-field diff.** That's the tradeoff. You can't see which fields would change. In return, the action never compares its idea of a field with the server's, and every field you wrote matches the YAML after each apply.

## What apply does

Plan and apply read everything before they write anything:

1. Load and validate the whole bundle.
2. List every resource with your label, archived ones included, across every page. (A server that repeats a pagination cursor fails the plan instead of looping forever.)
3. Check that nothing you're about to create exists outside the bundle, and that nothing you're about to delete has children outside it.

Then apply writes, in this order:

4. Detach: remove assignments to resources that are about to be deleted.
5. Unarchive or unpublish agents.
6. Delete, children first: widgets, variations, agents, memory entries, memory layers, tools, then tool sets.
7. Create or update: tool sets, tools, memory layers, memory entries, agents, variations, then widgets.
8. Publish agents.
9. Delete what had to wait: variations removed from agents that stay, and agents a widget is moving off of.

Why the split between steps 6 and 9? Because Cadenya won't delete the last variation of a published agent, or an agent a widget is bound to. Swap a published agent's only variation for a new one and the new one has to exist first. Move a widget to a new agent and the widget has to move (step 7) before the old agent can go. A published agent you delete outright gets unpublished in step 5, so its variations can go.

Say you delete `frontend/fetch-form-state.yaml` and take its assignment out of `default.yaml`. Plan shows the detach first:

```
detach    agentVariation cadenya-assistant/default
delete    tool frontend/fetch-form-state
update    toolSet ask-cadenya
update    toolSet frontend
update    toolSet petstore
update    memoryLayer playbooks
update    memoryEntry playbooks/tool-set-setup
update    agent cadenya-assistant
update    agentVariation cadenya-assistant/default
update    widget support-chat
Plan: 0 create, 8 update, 1 delete, 1 detach
```

A detach needs the field it changes in the variation's YAML: `assignments` for tools, tool sets, and sub-agents, `memoryLayerAssignments` for memory layers. Without it, plan stops and names the field to add (`[]` works). The detach matters for memory layers in particular: Cadenya refuses to delete a layer that a variation still assigns.

**Apply isn't transactional.** It stops at the first failed request and reports what finished. It can't roll back writes that already landed. Fix the cause and run it again: every run builds a fresh plan from what's in Cadenya now.

Two applies against the same bundle at the same time race each other. Give each workspace and bundle one `concurrency` group, as the quick start does.

The action manages configuration and stops there. Beyond `state`, lifecycle changes (archiving, for one) are out of scope, along with secrets and waiting for an MCP or OpenAPI tool set to finish syncing.

## Running it outside GitHub Actions

The action runs `cadenya-config`, and the CLI does everything the action does. Use it to try a bundle from your laptop, or to run it in GitLab CI, Jenkins, CircleCI, or Buildkite.

Download the archive for your platform from [the latest release](https://github.com/cadenya/cadenya-config-loader/releases/latest). Check it against `checksums.txt` and its build provenance:

```sh
gh release download v0.1.2 -R cadenya/cadenya-config-loader -p 'cadenya-config_0.1.2_linux_amd64.tar.gz' -p checksums.txt
sha256sum --check --ignore-missing checksums.txt
gh attestation verify cadenya-config_0.1.2_linux_amd64.tar.gz --repo cadenya/cadenya-config-loader
tar -xzf cadenya-config_0.1.2_linux_amd64.tar.gz
```

(With Go 1.22 or newer, `go install github.com/cadenya/cadenya-config-loader/cmd/cadenya-config@v0.1.2` works too.) [docs/ci.md](docs/ci.md) covers macOS and Windows.

The commands match the action's `command` input:

```sh
./cadenya-config -C examples/basic validate
CADENYA_API_KEY=... ./cadenya-config -C examples/basic plan
CADENYA_API_KEY=... ./cadenya-config -C examples/basic apply
```

`validate` prints `Valid bundle support-tools: 9 resources`. `plan` and `apply` print one line per operation and a summary:

```
create    toolSet ask-cadenya
create    toolSet frontend
create    toolSet petstore
create    tool frontend/fetch-form-state
create    memoryLayer playbooks
create    memoryEntry playbooks/tool-set-setup
create    agent cadenya-assistant
create    agentVariation cadenya-assistant/default
create    widget support-chat
publish   agent cadenya-assistant
Plan: 9 create, 0 update, 0 delete, 0 detach, 1 state change
```

The action's `command` input is the command here, and its other inputs are flags with the same names (`--bundle-key`, `--log-level`, and so on), except `version` and `binary-path`, which only the action needs. `-C` is short for `--directory`. The CLI adds a few flags of its own:

| Flag | Default | What it does |
| --- | --- | --- |
| `--output` | `text` | `text` or `json` on stdout |
| `--report-file` | None | Also writes the JSON report here, even when the run fails. Relative to the directory you run from, not `--directory` |
| `--dry-run` | `false` | `apply` only: plans and prints without writing |

Commands never prompt, and the exit code says what kind of failure it was:

| Code | Meaning |
| --- | --- |
| `0` | Success, including a plan with changes in it |
| `1` | The run failed: an invalid bundle, an API error, or an apply that stopped partway |
| `2` | The command was wrong: an unknown command or flag, a bad value, a missing setting like the API key, an unreadable `cadenya.yaml`, or a `--report-file` path that can't be written |
| `130` | Interrupted by SIGINT or SIGTERM |

stdout carries the result and stderr carries the logs, so `--output json > report.json` keeps them apart. [docs/ci.md](docs/ci.md) has the full contract for other CI systems, and [examples/buildkite.pipeline.yml](examples/buildkite.pipeline.yml) is a complete Buildkite pipeline.

## Development

Install Go and [direnv](https://direnv.net/), then:

```sh
direnv allow
make check build
```

The `.envrc` keeps Go's caches in `.direnv/`, puts `bin/` on your `PATH`, and loads a gitignored `.env` if you have one. `make check` runs `go vet`, the tests with the race detector, and a validate of `examples/basic`.

The tests run against a fake Cadenya API on a local HTTP server, so they need no credentials. The one test that talks to the real API is behind a build tag and an opt-in variable. Point it at a **throwaway workspace**:

```sh
# Set CADENYA_API_KEY, CADENYA_WORKSPACE_ID, and CADENYA_TEST_MODEL_ID first.
CADENYA_LIVE_TEST=1 direnv exec . go test -tags=integration ./internal/command -run '^TestLiveBundle$' -count=1 -v
```

It creates a bundle with a random key: a tool set, a tool, a memory layer with one entry, a published agent with one variation, and a widget pinned to that variation. Then it checks assignment clearing, entry updates, tool pruning, and retiring an assigned memory layer, and deletes everything on the way out (unpublishing first). It runs no objectives. If cleanup fails, the error names the bundle key so you can clean up by hand. (Kill the test with `-9` and you're cleaning up by hand anyway.)

The code:

1. `cmd/cadenya-config`: the CLI entry point.
2. `internal/command`: flags, the three commands, reports, and the tests.
3. `internal/config`: the YAML loader, identities, and local references. It never calls the API.
4. `internal/reconcile`: listing, ownership checks, planning, and apply.
5. `internal/action`: the GitHub Action adapter, which runs as the hidden `cadenya-config github-action` command.
6. `action.yml`: downloads the release and runs the adapter.

[docs/releasing.md](docs/releasing.md) covers cutting a release.

## License

Apache-2.0. See [LICENSE](LICENSE), with third-party notices in [NOTICE](NOTICE) and `licenses/`. Found a security issue? [SECURITY.md](SECURITY.md) says where to send a private report.

Want to change something? Start with [CONTRIBUTING.md](CONTRIBUTING.md).
