# Installation, Authentication, Output Extraction, and Recovery

## Install or Upgrade the kha CLI

Use `kha upgrade` when an installed CLI is outdated, or run the platform installer below when `kha` is missing. Neither Go nor API credentials are required.

### macOS and Linux

```sh
curl -fsSL https://ha-static.moonshot.cn/kha/install.sh | sh
export PATH="$HOME/.local/bin:$PATH"
kha version
```

Installs into `~/.local/bin` after verifying SHA-256 (override with `KHA_INSTALL_DIR` or pin a version with `KHA_VERSION=0.X.Y`). Add `~/.local/bin` to your shell profile or invoke `~/.local/bin/kha` in subsequent shell commands.

### Windows (PowerShell)

```powershell
$installer = Join-Path ([IO.Path]::GetTempPath()) 'kha-install.ps1'
Invoke-WebRequest https://ha-static.moonshot.cn/kha/install.ps1 -UseBasicParsing -OutFile $installer
& $installer
$env:Path = "$(Join-Path $env:LOCALAPPDATA 'kha\bin');$env:Path"
kha version
```

Installs into `%LOCALAPPDATA%\kha\bin` (customize with `-InstallDir`, `-Version 0.X.Y`, or `-Architecture amd64|arm64`).

`kha upgrade` also refreshes skill copies previously installed with `kha install-skill` (project `./.agents/skills/kha/` or user `~/.agents/skills/kha/` with `-g`).

## Connection

`kha whoami` verifies your connection and prints active profile/endpoint metadata; `kha login` stores credentials in the system keychain and saves profile settings. `kha profile list` inspects all profiles (endpoints, API versions, and where each key lives — never the key itself), and `kha profile use <name>` switches the current profile for interactive shells (it warns when `KHA_PROFILE` keeps overriding the switch; scripts should keep using `--profile`/`KHA_PROFILE`).

- **With `--profile <name>`**: Data commands use that profile and ignore `KIMI_API_KEY` and `KHA_BASE_URL` (explicit `--api-key`, `--base-url`, and `--api-version` flags still override).
- **Without `--profile`**: `KHA_PROFILE` or the config file's `current` entry selects the profile; `KIMI_API_KEY` and `KHA_BASE_URL` override its values, and explicit flags take highest precedence.
- **How `kha login` saves settings**: Writes to `--profile <name>` or `default` (not automatically to `current`, and does not switch `current`). Saves `--base-url` > `KHA_BASE_URL` > previous value, and `--api-version` > previous value. On machines without a keychain, set `KIMI_API_KEY` directly instead of logging in.
- **Switching profiles**: `kha profile list` shows saved profiles with the current one marked and the key's storage location (`keychain`, `plaintext`, or `none`), never the key itself. `kha profile use <name>` updates `current`; `KHA_PROFILE` and `--profile` still take precedence.

To rotate or configure a profile key non-interactively:

```sh
kha login --profile "$PROFILE" --base-url "$BASE_URL" --api-key-stdin < new-key.txt
kha whoami --profile "$PROFILE" -o json
```

## Recent History and Field Extraction (`--transform`)

Read the latest 50 events in chronological order:

```sh
kha sessions events "$SESSION_ID" --order desc --limit 50 \
  --transform 'items|@reverse|#(type=="agent.message")#.data.content.#(type=="text")#.text|@flatten' -r
```

Continue with `--page-token` (from `next_page_token`) or `--all` for earlier history. Use `--transform <gjson-path>` with `-r` (`--raw-output`) to extract fields without temporary files:

```sh
AGENT_VERSION=$(kha agents get "$AGENT_ID" --transform version -r)
kha agents list --transform '{id,name}'
kha files list --all --transform id -r
```

On list commands, field paths and multipaths apply per item, while collection queries (`#(...)`) apply to the item array; watch mode evaluates the path per event.

`-o jsonl` renders one compact JSON value per line: lists emit one line per item and stream page by page with `--all` (no buffered array), with `--transform` and `-r` applied per item — `kha agents list --all -o jsonl --transform id -r` is the idiomatic ID feed for `xargs`. `agents list` and `sessions list` accept `--since 24h|7d|2w|2026-11-01` for recent-item filtering.

To drive a Session from a pipeline, `kha sessions attach <id> --input` sends each nonempty stdin line as a `user.message` and detaches itself once stdin closes and the last message's turn ends.

## Exit Codes and Failure Recovery

- **Structured errors**: Add `--error-output json` (or `yaml`) to render runtime errors on `stderr` as `{"error": "..."}`.
- **Exit codes**: `0` success, `1` runtime/API/validation failure (`plugins validate` keeps findings on `stdout`), `2` usage/flag error before any request, `130` interrupted (`Ctrl-C`).
- **Mutation retries**: Each CLI run is a new process; if a mutation (`create`, `update`, `send`) loses its connection before printing a response, inspect the resource before repeating the command.

| Observation | Next Action |
| --- | --- |
| **HTTP `401`** | Run `kha whoami -o json` with the same `--profile` / `--base-url` flags; replace missing or invalid credentials via stdin and verify. |
| **Exit code `2`** | Run `kha docs <resource> <verb>` to check the exact flags and arguments for your installed CLI version. |
| **Build still running** | Run `kha environments wait-ready "$ENVIRONMENT_ID" --timeout 10m`; inspect `kha environments get "$ENVIRONMENT_ID" -o json` if it fails. |
| **HTTP `409` conflict** | On `create`: inspect existing resources and reuse if suitable, or pick a distinct name. On `update`: re-fetch the latest `version` or `content_sha256`, reconcile changes, and retry. |
| **Archive running Session** | Run `kha sessions interrupt "$SESSION_ID"` first, wait for `idle`, then run `kha sessions archive "$SESSION_ID"`. |
