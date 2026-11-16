---
name: kha
description: >-
  Operate Kimi Hosted Agents with the kha CLI: run and recover remote tasks,
  collect outputs, and configure Agents and their resources. Not for SDK
  development or ordinary Kimi model API calls.
---

# kha — Kimi Hosted Agents CLI Guide

Use this skill to run tasks on **Kimi Hosted Agents**, inspect or recover remote Sessions, collect generated files and Artifacts, and configure Agents, Environments, Vaults, Memory Stores, Skills, Plugins, and Triggers with the `kha` CLI.

Each reference below is self-contained (covering both step-by-step CLI workflows and full JSON/YAML request schemas). Read only the reference needed for your current task. When you need subcommands or flags not shown here, run `kha docs <resource> <verb>` (local; never contacts the API).

| Task | Reference |
| --- | --- |
| Run tasks with local files, collect Artifacts/Session files, or recover and control Sessions & Threads | [references/workflows.md](references/workflows.md) |
| Create/update Agents, configure tools & MCP servers, or upload/publish Skills and Plugins | [references/agents.md](references/agents.md) |
| Configure cloud runtimes, packages, outbound networking, and wait for Environment builds | [references/environments.md](references/environments.md) |
| Store Vault credentials, authorize MCP OAuth, manage shared Memory Stores, or configure Dream policies | [references/bindings.md](references/bindings.md) |
| Schedule cron Triggers, fire manual runs, or configure webhook notifications | [references/triggers.md](references/triggers.md) |
| Install or upgrade `kha`, manage profiles/login, extract fields with `--transform`, and recover from errors | [references/operations.md](references/operations.md) |

## Common Conventions

- **Shell & IDs**: Examples assume a POSIX shell (`sh`/`bash`/`zsh`) and `jq` for complex JSON construction. Always use real resource IDs returned by the API or provided by the user.
- **Data on `stdout`, diagnostics on `stderr`**:
  - `-o json`: Single-page list commands return an object with `.items` and `.next_page_token`; adding `--all` returns a flat JSON array across all pages; `--watch` emits newline-delimited JSON (NDJSON).
  - `-o jsonl`: One compact JSON value per line. List commands print one line per item (with `--all`, lines stream as pages arrive), so large listings can be piped without buffering a whole array. `--transform` applies to each item.
  - `--transform <gjson-path>` filters or reshapes output before rendering. Combine with `-r` (`--raw-output`) to print strings or string arrays without JSON quotes, one value per line.
  - Prefer server-side list filters over client-side filtering: `--created-at-gte`/`--updated-at-lt` and the other `-gt/-gte/-lt/-lte` variants take RFC 3339 times (`agents`, `sessions`), `--official-only` (`agents`, `environments`), `--category` (`skills`, `plugins`), and `--agent` (`sessions`, `triggers`).
- **Flags vs. `--file` request bodies**:
  - On `create` and `update` commands, pass **either** scalar field flags (`--name`, `--model`, `--system-file`) **or** a complete JSON/YAML body via `--file` (`--file -` reads stdin). The two input styles cannot be mixed on the same command.
  - Exception: `kha files upload --file <path>` selects the local file to upload.
- **Literal strings vs. content files**:
  - String flags (`--system`, `--payload`) and positional arguments (`sessions send <id> <text>`) are always treated as literal text—never as file paths.
  - To load a field's content from a file (preserving trailing newlines and empty files), use `--system-file`, `--text-file`, `--payload-file`, or `--content-file` (pass `-` to read from stdin). Do not pass both the literal flag/argument and the `*-file` flag for the same field.
  - `kha sessions send` rejects empty or whitespace-only messages (exit code `2`). To ask an Agent to keep going, send an explicit prompt such as `"Continue."`.

## Quick Start: Run a Remote Task

If `kha` is not installed yet, follow [Install or upgrade the kha CLI](references/operations.md#install-or-upgrade-the-kha-cli). Before starting a new Session, verify your active connection if you have not already done so:

```sh
kha whoami -o json
```

> If you are resuming an existing Session or recovering from a local timeout, skip creation and go straight to [Recover, continue, or stop a session](references/workflows.md#recover-continue-or-stop-a-session).

### 1. Select an Agent and Environment

Inspect available Agents and Environments to find a pair with the right instructions, tools, preinstalled packages, and network access:

```sh
kha agents list -o json
kha environments list -o json

kha agents get "$AGENT_ID" -o json
kha environments get "$ENVIRONMENT_ID" -o json
```

If the Environment snapshot is still building (`build_status` is not `ready`), wait for it to finish before creating a Session:

```sh
kha environments wait-ready "$ENVIRONMENT_ID" --timeout 10m
```

### 2. Create a Session and Send the Task

A remote Session runs in an isolated cloud sandbox—it does not automatically see your local terminal history or local files. Write a self-contained task brief (goal, input paths, constraints, and expected outputs) to `brief.md`.

- **Need local files?** Follow [Run a task with a local file](references/workflows.md#run-a-task-with-a-local-file) to upload and bind them (`--text-file` only sends prompt text, not attachments).
- **Need Vault secrets or shared Memory Stores?** Bind them at Session creation time using [references/bindings.md](references/bindings.md)—Vaults and Memory Stores cannot be attached after the Session is created.

```sh
SESSION_ID=$(kha sessions create \
  --agent "$AGENT_ID" \
  --environment "$ENVIRONMENT_ID" \
  --title "Research task" \
  --transform id -r)

kha sessions send "$SESSION_ID" --text-file brief.md --wait --timeout 10m
```

### 3. Read the Reply and Download Deliverables

`kha sessions send --wait` returns the turn's terminal `stop_reason` (such as `end_turn`), not the Agent's text reply. Always inspect the Session's messages and Artifacts to verify the result:

```sh
kha sessions events "$SESSION_ID" --all \
  --transform '#(type=="agent.message")#.data.content.#(type=="text")#.text|@flatten' -r

kha artifacts list --session "$SESSION_ID" -o json
kha artifacts download "$ARTIFACT_ID" --out report.md
```
