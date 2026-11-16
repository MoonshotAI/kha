# Vaults, Credentials, Memory Stores, and Dream Policies

## Store Secrets in a Vault

Always store API keys, tokens, and passwords in a **Vault**—never in task prompts or Agent instructions. Pass secret values via `--value-stdin` or `--file -` so they stay out of shell history and process arguments.

### 1. Environment-Variable Credentials (Outbound Substitution)

Creating an `environment_variable` credential with flags alone defaults to `networking: {type: "limited", allowed_hosts: []}`—meaning **no host receives the secret** until `allowed_hosts` (or `"unrestricted"`) is configured. To create a credential with permitted outbound hosts in one step (ensure `github-token.txt` has no trailing newline, or strip it in `jq`):

```sh
VAULT_ID=$(kha vaults create --display-name "Analysis credentials" --transform id -r)

jq -n --rawfile secret github-token.txt \
  '{
    auth: {
      type: "environment_variable",
      secret_name: "GITHUB_TOKEN",
      secret_value: ($secret | sub("[\r\n]+$"; "")),
      networking: {
        type: "limited",
        allowed_hosts: ["api.github.com"]
      },
      injection_location: {
        header: true,
        body: false
      }
    }
  }' | kha vaults credentials create "$VAULT_ID" --file - -o json
```

To rotate an existing credential's secret without changing its networking rules:

```sh
kha vaults credentials update "$VAULT_ID" "$CREDENTIAL_ID" --value-stdin < new-token.txt
```

### 2. MCP Server Credentials (Static Bearer or Interactive OAuth)

```sh
# Static bearer token for an MCP server
kha vaults credentials create "$VAULT_ID" --type static_bearer --mcp-url "$MCP_URL" --value-stdin

# Interactive OAuth flow: print the authorize_url for the user, then wait for completion
kha vaults oauth create "$VAULT_ID" --mcp-url "$MCP_URL" -o json
kha vaults oauth wait "$VAULT_ID" "$OAUTH_SESSION_ID" --timeout 10m
```

### Vault and Credential Field Rules

| Resource / `auth.type` | Fields & Rules |
| --- | --- |
| Vault create / update | `display_name` (**required** on create; `--display-name`), `metadata` (`map[string]string`, up to 16 entries; `--file`, `{}` clears). |
| `environment_variable` | `secret_name` (immutable env var name), `secret_value`, optional `networking`, optional `injection_location` (`{header: bool, body: bool}`; omitting enables both). |
| `static_bearer` | `mcp_server_url` (immutable absolute URI), `token`. |
| `mcp_oauth` | `mcp_server_url`, `access_token`, optional `expires_at` (RFC3339), optional `refresh` (`{token_endpoint, client_id, token_endpoint_auth_type, refresh_token?, client_secret?, scope?}`), optional `egress_environment_id` (`""` clears on update). |
| `vaults oauth create` | Required `mcp_server_url` (`--mcp-url`); optional `display_name`, `egress_environment_id` (`--egress-environment`), and `client` (`{client_id, client_secret?, token_endpoint_auth_type?, scope?}` via `--file`; auth type is `"none"`, `"client_secret_post"`, or `"client_secret_basic"`). |

- **`allowed_hosts` validation**: Validated on both `"limited"` and `"unrestricted"` writes. Accepts exact hostnames, single IP addresses, `*.example.com` subdomain wildcards (excluding `example.com` itself), and host-only `http(s)://` URLs with optional trailing `/` (ports, paths, queries, regexes, and CIDR ranges are rejected). Outbound matching ignores the destination port.
- **Credential update semantics**: Secret values are write-only and cannot be cleared to empty. `networking` replaces as a whole; `injection_location` and `mcp_oauth.refresh` merge by field.

## Manage Shared Memory Stores and Dream Policies

When you pass `--content-file` (or `--content`), the CLI reads raw bytes and base64-encodes them automatically:

```sh
STORE_ID=$(kha memory-stores create --name team-memory --description "Shared notes" --transform id -r)
MEMORY_ID=$(kha memory-stores memories create "$STORE_ID" \
  --path README.md --content-file notes.md --transform id -r)

# Read and decode a Memory's content
kha memory-stores memories get "$STORE_ID" "$MEMORY_ID" --transform content -r | base64 -d

# Update a Memory using its current content_sha256 for conflict detection (409)
CONTENT_SHA=$(kha memory-stores memories get "$STORE_ID" "$MEMORY_ID" --transform content_sha256 -r)
kha memory-stores memories update "$STORE_ID" "$MEMORY_ID" \
  --sha256 "$CONTENT_SHA" --content-file notes-v2.md
kha memory-stores memories versions list "$STORE_ID" "$MEMORY_ID" -o json
```

| Resource | Field | Rules & CLI Input |
| --- | --- | --- |
| Store create / update | `name` | **Required** on create (`--name`): 1–64 Unicode letters/digits/`_`/`-`, starting with a letter/digit. Unique across active and archived stores; used as mount name. |
| Store create / update | `description` | Guidance shown to the Agent (`--description`). |
| Memory create / update | `path` | **Required** on create (`--path`): relative POSIX path up to 900 UTF-8 bytes (rejects leading `/`, `\`, percent-encoding, and empty/`.`/`..` segments). |
| Memory create / update | `content` | Up to 20 MiB decoded (`--content` / `--content-file` encode raw bytes; `--file` JSON/YAML expects base64). |
| Memory update | `content_sha256` | Current hash for optimistic concurrency (`--sha256`). |

### Dream Policy (`memory-stores dream-policy`)

Dreams consolidate Memory Store content after Session activity or on a daily schedule. Read with `kha memory-stores dream-policy "$STORE_ID" -o json`; replace with `--file` (the body must include both `activity` and `schedule`; `environment_id` is required when either is enabled, and `agent_id` is optional):

```sh
jq -n --arg env "$ENVIRONMENT_ID" \
  '{
    environment_id: $env,
    activity: {enabled: true, min_changed_sessions: 5},
    schedule: {enabled: true, local_time: "03:00", timezone: "Asia/Shanghai"}
  }' | kha memory-stores dream-policy "$STORE_ID" --file - -o json
```

Set both `activity` and `schedule` to `{"enabled": false}` to disable Dream consolidation.

Each Dream is one run of the Trigger the policy manages (`trigger_id`). Start one now with `kha memory-stores dream-policy run "$STORE_ID" --wait -o json`, and list past Dreams with `kha memory-stores dream-policy runs "$STORE_ID"` (`--has-error` keeps failed ones). Both print Trigger runs; stop a running Dream with `kha sessions interrupt` on the run's `session_id`. The managed Trigger itself cannot be updated, paused, or archived.

## Bind Vaults and Memory Stores to a Session

Bind Vaults and Memory Stores **at Session creation time**—they cannot be added or modified later:

```sh
jq -n --arg agent "$AGENT_ID" --arg env "$ENVIRONMENT_ID" \
  --arg vault "$VAULT_ID" --arg store "$STORE_ID" \
  '{
    agent_id: $agent,
    environment_id: $env,
    resources: [
      {type: "vault", vault_id: $vault},
      {type: "memory_store", memory_store_id: $store, access: "read_only"}
    ]
  }' > session-request.json
SESSION_ID=$(kha sessions create --file session-request.json --transform id -r)
```
