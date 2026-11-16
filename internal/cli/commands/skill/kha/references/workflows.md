# Sessions, Files, Deliverables, and Recovery

## Run a task with a local file

A remote Session runs in an isolated sandbox and cannot read your local disk directly. To give an Agent a local file:

1. Create the Session (or use an existing `SESSION_ID`).
2. Upload the local file with `kha files upload` to get a `FILE_ID`.
3. Bind that `FILE_ID` to the Session with `kha sessions resources add`, which mounts the file inside the sandbox and returns its remote `path`.
4. Reference that remote `path` in your prompt.

```sh
SESSION_ID=$(kha sessions create \
  --agent "$AGENT_ID" \
  --environment "$ENVIRONMENT_ID" \
  --title "Sales report" \
  --transform id -r)

FILE_ID=$(kha files upload --file sales.csv --transform id -r)
INPUT_PATH=$(kha sessions resources add "$SESSION_ID" --file-id "$FILE_ID" --transform path -r)

kha sessions send "$SESSION_ID" \
  "Read $INPUT_PATH, total the sales, explain any missing rows, and save a Markdown report as an Artifact." \
  --wait --timeout 10m
```

### Sending multi-line prompts

For longer instructions, pass a file or standard input via `--text-file` instead of a positional argument:

```sh
kha sessions send "$SESSION_ID" --text-file brief.md --wait --timeout 10m

kha sessions send "$SESSION_ID" --text-file - --wait --timeout 10m <<'TASK'
Summarize the current task's findings and remaining questions.
Use Markdown headings and cite the evidence for each conclusion.
TASK
```

To attach rich content blocks (for example a file reference alongside text), send a full `user.message` event instead of plain text:

```sh
kha sessions send "$SESSION_ID" --wait \
  --event '{type: user.message, data: {content: [{type: text, text: "summarize this CSV"}, {type: file, file_id: "'"$FILE_ID"'", mime_type: text/csv, title: data.csv, path: upload/data.csv}]}}'
```

A single-quoted heredoc (`<<'TASK'`) keeps `$VARIABLES` literal; put resolved remote paths in the task text.

### Uploading with a custom filename or extraction purpose

| Flag / Field | Description |
| --- | --- |
| `--file <path>` | Local file path to stream from disk (**required** on `files upload`). |
| `--filename <name>` | Remote filename seen by the platform (defaults to the local file's basename). |
| `--purpose <value>` | Optional processing mode: `file-extract`, `image`, `video`, or `batch` (omit for the default raw file upload). |

```sh
kha files upload --file ./export.bin --filename report.pdf --purpose file-extract -o json
kha files list -o json
kha files get "$FILE_ID" -o json
kha files download "$FILE_ID" --out downloaded-content
kha sessions resources list "$SESSION_ID" -o json
```

Uploading a file does not bind it to a Session by itself. When a file is uploaded with `--purpose file-extract` (such as a PDF), `kha files download` returns the extracted Markdown inside JSON rather than the original binary PDF. An ID that is not a `file_` ID and not a batch object ID returns that same parsed-text JSON when parsed text exists.

## Read the answer and collect deliverables

After `sessions send --wait` completes, read the Agent's messages and list any Artifacts produced during the Session:

```sh
kha sessions events "$SESSION_ID" --all \
  --transform '#(type=="agent.message")#.data.content.#(type=="text")#.text|@flatten' -r

kha artifacts list --session "$SESSION_ID" -o json
kha artifacts download "$ARTIFACT_ID" --out report.md
```

- **Earlier Artifact versions**: Each update to the same Artifact creates a new version. Inspect versions with `kha artifacts versions "$ARTIFACT_ID" -o json` and download a specific revision with `kha artifacts download "$ARTIFACT_ID" --version "$ARTIFACT_VERSION" --out report-v1.md` (add `--force` only when you intentionally want to overwrite an existing local file).
- **Download a Session workspace file**: If an Agent wrote a file to its sandbox workspace without publishing an Artifact, browse and download it from the Session filesystem (paths are relative to the Session file root; directory listings are not recursive, and `--out` overwrites the target path):
  ```sh
  kha sessions fs list "$SESSION_ID" --prefix output/ -o json
  kha sessions fs download "$SESSION_ID" output/report.md --out report.md
  ```
- **Multi-turn Sessions**: In an existing conversation, check the latest events so you read the current turn's answer rather than an earlier reply.

## Recover, continue, or stop a session

A local `--timeout` or `Ctrl-C` only stops your local CLI wait—it **does not stop the remote Session**. Always inspect the Session's current state before sending another message or retrying:

```sh
kha sessions get "$SESSION_ID" -o json
kha sessions events "$SESSION_ID" --order desc --limit 50 -o json
kha sessions pending list "$SESSION_ID" -o json
```

- **`running`**: The Agent is still working in the cloud. Watch progress with `attach` or `events --watch`, or poll status/events. Do not resend the same prompt—sending a message while `running` queues an additional user input rather than retrying the active turn.
- **`idle`**: The turn has finished. Check the `stop_reason`, read the Agent's messages, and download any deliverables. Send a follow-up message if needed.
- **`pending` queued inputs**: Wait for the queued input to be consumed, or retract an unwanted queued input with `kha sessions pending retract "$SESSION_ID" "$INPUT_ID"`.
- **`failed` / `session.error`**: Inspect `session.error` events and failed tool results in the event history before fixing inputs/configuration and retrying.

### Live streaming, Threads, and Session control

Live subscriptions stay open across turns until you detach locally with `Ctrl-C`:

```sh
kha sessions attach "$SESSION_ID"
kha sessions events "$SESSION_ID" --watch --since "$CURSOR" -o json
```

`kha sessions attach "$SESSION_ID" --input` also sends each non-empty stdin line as a `user.message`; lines sent mid-turn wait in the input queue (`kha sessions pending`). After end of input, `attach` exits once the Session is idle following the last message, so `printf 'step one\nstep two\n' | kha sessions attach "$SESSION_ID" --since now --input` works in scripts. `--input` cannot be combined with `--thread`.

`--since` takes a numeric `stream_cursor` from a previous `sessions events` page (shown as `CURSOR` in table output) or `now` to observe only future events—never an event ID (`evt_...`).

```sh
# Inspect child Threads spawned by a coordinator Agent
kha sessions threads list "$SESSION_ID" -o json
kha sessions threads events "$SESSION_ID" "$THREAD_ID" --all -o json

# Continue the conversation, interrupt the active turn, or retract a queued input
kha sessions send "$SESSION_ID" --text-file follow-up.md --wait --timeout 10m
kha sessions interrupt "$SESSION_ID"
kha sessions pending retract "$SESSION_ID" "$INPUT_ID"
```

## Session Fields and Overrides

Resource bindings, metadata, and per-Session Agent overrides map to dedicated flags; `--file` remains the whole-body alternative.

| Field | Create | Update | Description & CLI Flag |
| --- | --- | --- | --- |
| `agent_id` | **Required** | Immutable | Agent to run (`--agent`). |
| `agent_version` | Optional | Immutable | Exact Agent version to pin; omitting pins the latest version at creation (`--agent-version`). |
| `environment_id` | **Required** | Immutable | Environment to run in; pins its newest `ready` snapshot version (`--environment`). |
| `title` | Optional | Mutable | Human-readable Session title (`--title`). |
| `metadata` | Optional | Immutable | Caller-defined string map (`map[string]string`, up to 16 entries; repeatable `--metadata key=value` or `--file`). |
| `resources` | Optional | Use `sessions resources` | Initial File, Vault, Memory Store, or GitHub repository bindings. Shorthands: repeatable `--vault`, `--memory-store id[:read_only\|read_write]`, `--file-id`, `--repo url[:branch]` (+`--repo-token(-stdin)`); generic `--resource '{...}'` or `--file` for anything else. |
| `agent_overrides` | Optional | Mutable (except `roster`) | Session-local overrides that do not create a new Agent version (`--override '{...}'` or `--file`). |

### Resource Binding Shapes (`resources`)

When passing `resources` in `sessions create --file`, include only writable binding fields (omit server-assigned `id`, mounted `path`, and readback timestamps):

```json
{
  "agent_id": "agent_selected",
  "environment_id": "env_selected",
  "resources": [
    {"type": "file", "file_id": "file_uploaded"},
    {"type": "vault", "vault_id": "vlt_selected"},
    {"type": "memory_store", "memory_store_id": "mstr_selected", "access": "read_only", "instructions": "Use these reference notes."}
  ]
}
```

A Session supports up to **16 Vaults** and **8 Memory Stores** (`access` is `"read_only"` or `"read_write"`, defaulting to `read_write` in the `--memory-store` shorthand), and those bindings are fixed at creation. `--agent` accepts `agent_xxx@3` to pin a version (mutually exclusive with `--agent-version`). Uploaded files can be bound at creation or later with `kha sessions resources add "$SESSION_ID" --file-id "$FILE_ID"`.

### Per-Session Agent Overrides (`agent_overrides`)

| Override Field | Shape & Update Semantics |
| --- | --- |
| `model` | Model ID string or `{id, reasoning_effort?, provider?}` (see [agents.md](agents.md#agent-fields)). The provider must match the Session's provider. |
| `system` | System prompt override string; passing `""` clears the override. |
| `tools`, `mcp_servers`, `skills`, `plugins` | Same shapes as [Agent fields](agents.md#agent-fields). Supplying a non-empty list replaces that override; passing `[]` removes the override and reverts to the Agent's pinned list; omitting leaves the current override unchanged. |
| `skills[].version` | In Session overrides, omitting `version` pins latest at write time, while `"latest"` resolves dynamically on each wake. |
| `roster` | **Create only**: `{id, version}[]` resolved to exact Agent versions at creation; rejected on `sessions update`. |

```sh
jq -n --rawfile system session-system.md \
  '{agent_overrides: {system: $system}}' | \
  kha sessions update "$SESSION_ID" --file - -o json
```
