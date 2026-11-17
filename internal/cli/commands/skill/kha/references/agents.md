# Agents, Tools, Skills, and Plugins

## Create, Update, and Verify an Agent

To create an Agent, write `agent.yaml` (replace `model`, `tools`, `mcp_servers`, `skills`, or `plugins` with values and IDs verified in your target deployment):

```yaml
name: CSV analyst
model: kimi-k3.1-pro
system: |
  Analyze the files supplied in the Session and explain any missing inputs.
  Save requested reports as Artifacts and mention their filenames in your reply.
tools:
  - type: agent_toolset
    name: default_toolset_20260901
```

```sh
AGENT_ID=$(kha agents create --file agent.yaml --transform id -r)
```

Every Agent update creates a new immutable version and requires `--version` (or `"version"` in `--file`) for optimistic concurrency control (`409 Conflict` on mismatch). To update only the system prompt from a file:

```sh
VERSION=$(kha agents get "$AGENT_ID" --transform version -r)
NEW_VERSION=$(kha agents update "$AGENT_ID" \
  --version "$VERSION" \
  --system-file new-system.md \
  --transform version -r)

kha agents get "$AGENT_ID" -o json > agent-after.json
jq -e --rawfile system new-system.md '.system == $system' agent-after.json
```

Existing Sessions stay pinned to the Agent version they were created with. Always verify a new Agent version in a **new Session**:

```sh
SESSION_ID=$(kha sessions create \
  --agent "$AGENT_ID" \
  --agent-version "$NEW_VERSION" \
  --environment "$ENVIRONMENT_ID" \
  --transform id -r)
kha sessions send "$SESSION_ID" --text-file verification-task.md --wait --timeout 10m
kha sessions events "$SESSION_ID" --all \
  --transform '#(type=="agent.message")#.data.content.#(type=="text")#.text|@flatten' -r
```

To inspect version history or retire an Agent:

```sh
kha agents versions "$AGENT_ID" -o json
kha agents archive "$AGENT_ID"
```

## Agent Fields

Use scalar flags for basic settings or `--file` for nested JSON/YAML configuration (field flags and `--file` cannot be combined).

| Field | Description & Shape | CLI Input |
| --- | --- | --- |
| `name` | Display name (**required** on create). | `--name` |
| `description` | Human-readable summary. | `--description` |
| `model` | **Required** on create: model ID string or `{id, reasoning_effort?, provider?}`. `reasoning_effort` accepts `low`, `medium`, `high`, or `max`. | `--model` (ID) or `--file` (object) |
| `system` | System prompt. | `--system` or `--system-file` |
| `tools` | Built-in or MCP toolsets and per-tool settings. | Repeatable `--tool '{...}'` (JSON or relaxed YAML), or `--file` |
| `mcp_servers` | Connections `{type: "url", name, url, headers?}` (`name` unique; `type` defaults to `url` when omitted). | Repeatable `--mcp-server '{...}'`, or `--file` |
| `skills` | `{skill_id, version?}[]`; omitting `version` or passing `"latest"` pins latest at Agent write time. | Repeatable `--skill ID` or `--skill ID@version`, or `--file` |
| `plugins` | `{plugin_id, version?}[]`; `official` Plugins forbid `version`; `project` Plugins accept exact semver, omit `version` to pin latest at write, or pass `"latest"` to resolve on each wake. | Repeatable `--plugin ID` or `--plugin ID@version`, or `--file` |
| `metadata` | Caller-defined string map (`map[string]string`, up to 16 entries). | Repeatable `--metadata key=value`, or `--file` |
| `multiagent` | `{agents: [...]}` subagent roster, resolved at write time. | `--multiagent '{...}'`, or `--file` |
| `version` | **Update only**: current version for conflict detection (`409`). | `--version` |

- **Update semantics**: Omitted fields stay unchanged. Arrays and `metadata` replace whole collections when present (`[]` and `{}` clear them; `{multiagent: {agents: []}}` clears the roster). Do not resubmit raw `agents get` output wholesale—construct update payloads from writable fields plus `version`.
- **Repeatable binding flags**:
  ```sh
  kha agents create --name analyst --model kimi-k3.1-pro --system-file system.md \
    --skill "$SKILL_ID" --plugin "$PLUGIN_ID" -o json
  kha agents update "$AGENT_ID" --version "$VERSION" \
    --skill "$FIRST_SKILL_ID" --skill "$SECOND_SKILL_ID" -o json
  printf '%s\n' '{"version":"'"$VERSION"'","plugins":[]}' | kha agents update "$AGENT_ID" --file - -o json
  ```

### Tools, MCP Servers, and Multiagent Roster

- **Built-in toolset**: `{type: "agent_toolset", name: "default_toolset_20260901"}` (`read_file`, `write_file`, `edit_file`, `shell`, `ipython`, `todo_read`, `todo_write`, `web_search`, `web_open_url`, `search_image_by_text`, `search_image_by_image`, `save_artifact`).
- **MCP toolset**: `{type: "mcp_toolset", mcp_server_name: "<name from mcp_servers>"}`.
- Both accept `default_config: {enabled?, permission_policy?, load_mode?}` and `configs: [{name, enabled?, permission_policy?, load_mode?, result_hint?}]` (`permission_policy` is `"always_allow"`; `load_mode` is `"eager"` or `"on_demand"`).
- **MCP `headers`**: For non-secret routing headers only (authentication belongs in Vaults; see [bindings.md](bindings.md)). On update, unchanged `name`/`type`/`url` with omitted or empty `headers` preserves stored headers; non-empty `headers` replaces them, and `""` deletes a key.
- **Multiagent roster**: 1–20 entries on create. `{type: "agent", id: "agent_...", version?: "2"}` references another Agent (omitted `version` pins latest at write); `{type: "self"}` references the coordinator's own version.

```yaml
name: Research analyst
model:
  id: kimi-k3.1-pro
  reasoning_effort: high
system: Cite primary sources and save the final report as an Artifact.
tools:
  - type: agent_toolset
    name: default_toolset_20260901
    configs:
      - name: shell
        enabled: false
      - name: web_search
        load_mode: on_demand
  - type: mcp_toolset
    mcp_server_name: research
    default_config:
      load_mode: on_demand
mcp_servers:
  - type: url
    name: research
    url: https://mcp.example.com/mcp
multiagent:
  agents:
    - type: agent
      id: agent_researcher
      version: "2"
    - type: self
metadata:
  team: research
```

## Upload Skills and Publish Project Plugins

### Upload and attach a Skill bundle (`.zip` or `.skill`)

```sh
kha skills list -o json
kha skills create ./bundle.zip -o json > skill-created.json
SKILL_ID=$(jq -er .skill.id skill-created.json)
SKILL_VERSION=$(jq -er .version.version skill-created.json)

kha skills get "$SKILL_ID" -o json
kha skills versions "$SKILL_ID" -o json
kha skills content "$SKILL_ID" "$SKILL_VERSION" --out downloaded-skill.zip
```

To add a specific Skill version to an existing Agent while preserving its other Skill bindings:

```sh
kha agents get "$AGENT_ID" -o json > agent-before.json
jq --arg id "$SKILL_ID" --arg version "$SKILL_VERSION" \
  '{version: .version, skills: (((.skills // []) | map(select(.skill_id != $id))) +
    [{skill_id: $id, version: $version}])}' agent-before.json > agent-skills.json
kha agents update "$AGENT_ID" --file agent-skills.json -o json
```

Use `kha skills archive "$SKILL_ID"` when retiring a Skill.

### Validate and publish a project Plugin (`.zip` up to 64 MiB with `kimi.plugin.json`)

```sh
kha plugins list --query github -o json
kha plugins list --scope project -o json
kha plugins get "$PLUGIN_ID" -o json
```

Always run `kha plugins validate` before publishing (`validate` reports all findings on `stdout` and exits `0` when `report.ok` is `true` or `1` when blocking errors exist):

```sh
kha plugins validate ./data-tools.zip -o json
PLUGIN_ID=$(kha plugins create ./data-tools.zip --transform plugin.id -r)

# Bump the semantic version in kimi.plugin.json before publishing a new version:
kha plugins versions create "$PLUGIN_ID" ./data-tools-1.1.0.zip
kha plugins versions "$PLUGIN_ID" -o json
kha plugins content "$PLUGIN_ID" 1.1.0 --out published-plugin.zip
```

`official` Plugins are read-only. To retire a project-owned Plugin and free its name (while Agents pinned to an exact version keep working), run `kha plugins archive "$PLUGIN_ID"`.
