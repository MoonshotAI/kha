# Scheduled Triggers and Webhook Notifications

## Create, Run, and Control a Trigger

Write the recurring prompt into `scheduled-task.md` and include `initial_events` in the Trigger definition so every scheduled Session receives the opening user message automatically:

```sh
jq -n --arg agent "$AGENT_ID" --arg env "$ENVIRONMENT_ID" \
  --rawfile task scheduled-task.md \
  '{
    name: "Daily report",
    agent_id: $agent,
    environment_id: $env,
    policy: {type: "cron", expression: "0 9 * * *", timezone: "Asia/Shanghai"},
    session_mode: "new_session",
    initial_events: [{type: "user.message", data: {content: [{type: "text", text: $task}]}}]
  }' > trigger.json

TRIGGER_ID=$(kha triggers create --file trigger.json --transform id -r)
kha triggers list --status active -o json
```

To fire a Trigger immediately (optionally appending extra user text via `--payload` or `--payload-file`) and inspect its runs:

```sh
kha triggers run "$TRIGGER_ID" --wait --timeout 10m -o json > run.json
kha triggers runs "$TRIGGER_ID" -o json
```

- **`completed` means dispatch succeeded, not that the Agent finished**: Inspect `status`, `error`, and `session_id` in `run.json`, then read that Session's status, messages, and Artifacts using [workflows.md](workflows.md).
- To resume waiting on a known run or pause future scheduled runs:
  ```sh
  kha triggers runs wait "$TRIGGER_ID" "$RUN_ID" --timeout 10m -o json
  kha triggers pause "$TRIGGER_ID"
  ```

## Trigger Fields and Webhook Notifications

| Field | Description & Update Semantics | CLI Input |
| --- | --- | --- |
| `name` | Display name (**required** on create). | `--name` |
| `agent_id` | Target Agent (**required** on create). | `--agent` |
| `agent_version` | Optional pin; omitted on create or `""` on update resolves latest when firing. | `--agent-version` |
| `environment_id` | Target Environment (**required** on create; cannot be cleared). | `--environment` |
| `policy` | `{type: "cron", expression, timezone}` (5-field cron and IANA timezone; flag-based create defaults to `UTC`). | `--cron`, `--timezone`, or `--file` |
| `session_mode` | `"new_session"` (default) or `"reuse_session"` (reuses a single Trigger-managed Session; overlap with an in-flight turn records `skipped` / `overlap_in_flight`). | `--session-mode` |
| `initial_events` | Opening messages sent on every run (`user.message` only). Update replaces the array; `[]` clears. | Repeatable `--initial-event '{...}'`, or `--file` |
| `resources` | Session bindings (same shapes as [workflows.md](workflows.md#resource-binding-shapes-resources)). Update replaces the array; `[]` clears. | Repeatable `--vault`/`--memory-store`/`--file-id`/`--repo` shorthands, `--resource '{...}'`, or `--file` |
| `notification` | `{url, secret_vault: {vault_id}}` (`https://` URL and signing Vault must be supplied together; `{}` detaches). | `--notification '{...}'`, or `--file` |

Use `pause`, `resume`, and `archive` subcommands for lifecycle transitions rather than sending a `status` field.

### Webhook Notification Setup

`notification.secret_vault` must reference a Vault containing an `environment_variable` credential named `webhook_secret` (resolved at delivery time to sign standard-webhooks HMAC-SHA256 headers):

```json
{
  "session_mode": "reuse_session",
  "notification": {
    "url": "https://hooks.example.com/agent-runs",
    "secret_vault": {"vault_id": "vlt_webhooks"}
  }
}
```

Only scheduled runs deliver notifications. Each delivery is a thin JSON envelope `{"type": "event", "id": <delivery id>, "created_at": ..., "data": {"type": <event>, "id": <resource id>, "scope_ref": ...}}`. Run events (`trigger_run.started`, `trigger_run.succeeded`, `trigger_run.failed`) carry the TriggerRun ID; `trigger.paused` carries the Trigger ID. Receivers should deduplicate by the delivery `id`, then read current state with `kha triggers get <trigger-id>` or `kha triggers runs <trigger-id>`; give each Trigger its own notification URL when the receiver needs to know which Trigger a run belongs to. Go receivers can verify and decode a delivery with `kha.UnwrapWebhook`.
