# KHA Engineering Guide (`AGENTS.md`)

KHA (`github.com/MoonshotAI/kha`) is the official Go SDK and CLI for Kimi Hosted Agents.
All generated protocol files (`*.gen.go`) are committed to the repository, so building, linting, and testing require only the Go toolchain.

## Build, Test, and Verify

Requires **Go 1.27+**.

```sh
go build ./cmd/kha
go test ./... -count=1
go vet ./...
golangci-lint run ./...
```

To inspect the CLI command reference that matches the current codebase without touching the network or reading local credentials:

```sh
go run ./cmd/kha docs
go run ./cmd/kha docs sessions send
```

## Architecture & Directory Layout

- **Root `*.gen.go`**: Generated from the public OpenAPI contract in a compact layout: `types.gen.go` (request/response models), `client.gen.go` (`Client`, `ClientOption`, `HTTPResult[T]`, `ResponseError[T]`, runtime aliases), `operations.gen.go` (every resource client operation), and `doc.gen.go`.
- **Root handwritten `.go`**: Product-level SDK capabilities layered on the generated client:
  - `sdk.go`: `New` (Hosted Agents defaults, `KIMI_API_KEY` fallback, product retry transport), `NewPollClient`, and `LoadSecretFile`.
  - `doc.go` & `example_test.go`: Package overview and compile-checked examples for `pkg.go.dev`.
  - `pagination.go`: Lazy `iter.Seq2[*T, error]` pagination iterators (`All*`) for every resource.
  - `sessions_events.go`: Resilient SSE event stream state machine (`Sessions.Events`, `Sessions.ThreadEvents`) with heartbeat filtering, payload-ID deduplication, `Last-Event-ID` reconnects, and expired-cursor history backfill.
  - `waiter.go`: Event-driven (`SendMessageAndWait`) and polled (`WaitForReady`, `WaitOAuthSession`, `WaitForTerminal`) resource waiters.
  - `tool_runner.go`: `Sessions.RunTools`, which answers custom tool calls and tool confirmations from the Session event stream.
  - `retry.go`: Idempotency-aware retry transport (`GET`/`HEAD` excluding `WorkItems.Poll`, plus allowlisted JSON writes carrying an explicit `Idempotency-Key`).
  - `errors.go`: `ErrorDetails`, `HTTPError`, `IsProtocolError`, and HTTP status helpers (`IsNotFound`, `IsRateLimited`, etc.).
  - `webhook.go`: `UnwrapWebhook`, `VerifyWebhookSignature`, and `ParseWebhookEvent` for standard-webhooks HMAC-SHA256 verification and typed Trigger notification payloads.
- **`internal/`**:
  - `httpcodec/`, `jsoncodec/`, `optional/`, `stream/`, `transport/`, `validate/`: Generated runtime helpers (`*.gen.go`).
  - `cli/`: CLI command tree (`commands/`), output formatters (`output/`, `render/`), profile/keychain config (`config/`), tunnel agent (`tunnel/`), and the embedded AI coding assistant skill (`commands/skill/kha/`).
  - `genlayout/`: Generation step that merges the per-tag root files emitted by the generator into the compact `*.gen.go` layout.
- **`worker/` & `tools/`**: Public packages for the self-hosted environment worker poll/run loop and its sandbox tool executors (`read_file`, `write_file`, `edit_file`, `shell`, `todo_*` from `default_toolset_20260901`; `read`, `write`, `edit`, `bash`, `glob`, `grep` from `default_toolset_20261101`). Their exported surface, lease handling, and write-back rules are public API: review every exported change and record it in the CHANGELOG.
- **`docs/zh-CN/`**: Simplified Chinese README and CHANGELOG.
- **`cmd/kha/`**: CLI `main` package.
- **`deploy/`**: `install.sh`, `install.ps1`, and installer verification tests.

## Core Engineering Rules

1. **Never edit `*.gen.go` files by hand**: Regenerate them from the OpenAPI contract via the code generator instead.
2. **No redundant CRUD wrappers**: Ordinary operations use the generated `Client`, resource groups, request structs, and `HTTPResult[T]` directly. Handwritten helpers extend the generated resource client types in the same package.
3. **Zero external dependencies in root `kha`**: Keep the root SDK package strictly limited to the Go standard library and internal packages of this module. CLI-only dependencies stay in `internal/cli/` or `cmd/kha/`.
4. **Two-state vs. three-state optionality**: Use pointers (`*T`, e.g. `new(v)`) for two-state optional fields, and `Optional[T]` (`NewOptional(v)`, `SetNull()`) when omission and explicit `null` have distinct semantics.
5. **Preserve typed errors**: Do not flatten `*ResponseError[T]`, `*UnexpectedResponseError`, `*StreamError`, or `*StreamDecodeError` into untyped strings. Use `ErrorDetails` and `HTTPError` for inspection.
6. **Retry safety**: Never auto-generate random `Idempotency-Key` values, never replay multipart uploads (`File`), and never auto-retry `WorkItems.Poll`. The only derived key is `Sessions.RunTools`, which keys each answer by the server-assigned event ID it answers.
7. **CLI stream & secret hygiene**:
   - Write only structured or requested data to `stdout`; all progress notes, spinners, warnings, and errors go to `stderr`.
   - Read secrets from `stdin` or files rather than command-line flags, and strip terminal escape sequences from server-provided messages.
8. **Language & cleanliness**:
   - Keep non-ASCII literals in tests only when specifically testing Unicode handling or CJK terminal display width.

## Testing & Documentation Checklist

- Run `go test ./... -count=1`, `go vet ./...`, and `golangci-lint run ./...` after making changes.
- Use `httptest.NewServer` and `testing/synctest` to keep HTTP, retry, waiter, and SSE recovery tests fast and deterministic.
- Keep [`README.md`](README.md), [`docs/zh-CN/README.md`](docs/zh-CN/README.md), [`example_test.go`](example_test.go), `kha docs` (`internal/cli/commands/docs.go`), and the embedded skill (`internal/cli/commands/skill/kha/`) aligned with actual SDK signatures and CLI flags.
- Record user-visible SDK, CLI, skill, or documentation changes under `## [Unreleased]` in [`CHANGELOG.md`](CHANGELOG.md) and [`docs/zh-CN/CHANGELOG.md`](docs/zh-CN/CHANGELOG.md).
