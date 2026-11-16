# Execution Environments

## Create, Update, and Wait for Builds

Inspect existing Environments (`kha environments list -o json`) before creating a new one.

- **Default cloud Environment**:
  ```sh
  ENVIRONMENT_ID=$(kha environments create --name "Analysis runtime" --wait --transform id -r)
  ```
- **Custom packages, setup script, and network allowlist** (`environment.json`):
  ```json
  {
    "name": "CSV analysis runtime",
    "config": {
      "type": "cloud",
      "packages": {
        "pip": ["pandas==2.3.2"],
        "apt": ["jq"]
      },
      "setup_script": "mkdir -p /workspace/output",
      "networking": {
        "type": "limited",
        "allowed_hosts": ["github.com", "*.example.org:8443"],
        "allow_package_managers": true,
        "allow_mcp_servers": true
      }
    }
  }
  ```
  ```sh
  ENVIRONMENT_ID=$(kha environments create --file environment.json --wait --timeout 10m --transform id -r)
  kha environments get "$ENVIRONMENT_ID" -o json
  ```

To update an Environment's configuration, write the complete desired `config` object to `environment-v2.json` and wait for the new immutable snapshot build:

```sh
kha environments update "$ENVIRONMENT_ID" --file environment-v2.json -o json
kha environments wait-ready "$ENVIRONMENT_ID" --timeout 10m
```

Existing Sessions continue running on the Environment version they pinned at creation time; new Sessions pin the latest `ready` version. To retire an Environment so no new Sessions can start against it, run `kha environments archive "$ENVIRONMENT_ID"`.

## Environment Fields

| Field | Description & Update Semantics | CLI Input |
| --- | --- | --- |
| `name` | Non-empty display name (**required** on create). | `--name` |
| `description` | Optional summary; passing `""` on update clears it. | `--description` |
| `config` | Runtime specification. Omitting on flag-only `create` defaults to `{type: "cloud"}` (or `{type: "self_hosted"}` with `--self-hosted`). Cloud networking/packages/setup map to `--networking`, `--allow-host`, `--allow-package-managers`, `--pip`/`--npm`/`--go`/`--cargo`, and `--setup-script(-file)`; proxy and other advanced knobs stay on `--file`. Replacing `config` creates a new version and cannot change `type`; always supply the full `config` object (`update --config '{...}'` also works). | `--networking`/`--pip`/… or `--file` (create); `--config '{...}'` or `--file` (update) |
| `metadata` | Caller-defined string map (`map[string]string`, up to 16 entries). Update replaces the map; `{}` clears it. | Repeatable `--metadata key=value`, or `--file` |

### Cloud `config` Fields

| `config` Field | Description |
| --- | --- |
| `type` | `"cloud"` (managed sandbox) or `"self_hosted"` (external worker queue). |
| `packages` | Optional package lists: `apt`, `pip`, `npm`, `go`, `cargo`. |
| `setup_script` | Shell script run after package installation; a non-zero exit code fails the build. |
| `networking.type` | `"unrestricted"` (default when `networking` is omitted) or `"limited"`. |
| `networking.allowed_hosts` | In `"limited"` mode: domains or `*.example.com` wildcards with optional `:port` (defaults to `80`/`443`), or IP/CIDR entries (direct TCP on all ports; must omit port). |
| `networking.allow_package_managers` | Add official package registries to a `"limited"` allowlist (enable this whenever a `"limited"` Environment installs `packages`). |
| `networking.allow_mcp_servers` | Add the Agent's declared MCP server endpoints to a `"limited"` allowlist. |
| `networking.proxy` | Optional outbound upstream proxy configuration (see below). |

### Outbound Proxy (`networking.proxy`)

```json
{
  "url": "https://proxy.example.com:8443",
  "hosts": ["internal.example.com"],
  "route_all": false,
  "auth": {
    "type": "basic",
    "vault_id": "vlt_proxy",
    "credential_id": "cred_proxy"
  }
}
```

- `url` accepts `http://` or `https://` without userinfo.
- `hosts` route through this proxy even if not listed in `allowed_hosts`; `route_all: true` routes all external hosts through the proxy and bypasses `allowed_hosts` (platform-internal endpoints stay direct).
- Optional `ca` is a non-secret PEM trust bundle.
- Optional `auth` references a Vault credential: `"basic"` (`user:password`) or `"client_cert"` (PEM certificate + private key; requires `https://` proxy `url`).
