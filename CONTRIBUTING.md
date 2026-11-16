# Contributing to KHA

Thank you for your interest in contributing to **KHA** (`github.com/MoonshotAI/kha`), the official Go SDK and CLI for Kimi Hosted Agents!

This repository is completely **self-contained**: all generated OpenAPI protocol code (`*.gen.go`) is committed directly to the tree, so you only need a standard Go toolchain to build, test, and work on the project locally.

---

## Local Development Setup

### Prerequisites

- **Go 1.27 or newer**
- **`golangci-lint` v2.14.0** (matching CI) for running the linter

### Build, Test, and Lint

```sh
# 1. Build the CLI binary
go build ./cmd/kha

# 2. Run the full unit and integration test suite
go test ./... -count=1

# 3. Run Go vet
go vet ./...

# 4. Install and run golangci-lint (same version as CI)
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
golangci-lint run ./...
```

---

## Repository Structure

```text
.
├── *.gen.go                 # Generated OpenAPI code: types, client, operations, and package doc
├── sdk.go, doc.go, ...      # Handwritten SDK extensions: client init, pagination, SSE recovery, waiters, retries, webhooks
├── example_test.go          # Compile-checked Go SDK examples rendered on pkg.go.dev
├── cmd/kha/                 # CLI binary entrypoint
├── worker/                  # Public self-hosted environment worker loop and lease lifecycle
├── tools/                   # Public sandbox tool executors for self-hosted workers
├── docs/zh-CN/              # Simplified Chinese README and CHANGELOG
├── internal/
│   ├── httpcodec/, ...      # Generated protocol runtime packages (*.gen.go)
│   ├── cli/                 # CLI commands, output formatting, config/keychain profiles, and embedded agent skill
│   └── genlayout/           # Merges generator output into the compact root *.gen.go layout
└── deploy/                  # Cross-platform CLI install scripts (install.sh, install.ps1) and tests
```

---

## Engineering & Design Guidelines

1. **Never edit `*.gen.go` files by hand**:
   Protocol models and base client methods are generated from the Kimi Hosted Agents OpenAPI contract. If a change requires updating a generated model or endpoint, open an issue to discuss the contract change with maintainers.
2. **Extend generated types directly**:
   Handwritten SDK capabilities—such as `All*` pagination iterators, `Sessions.Events` SSE recovery, and `WaitFor*` resource waiters—are implemented as methods on the generated client types (`AgentsClient`, `SessionsClient`, etc.) in the root package rather than wrapping them in a second client layer.
3. **Zero external dependencies in the root SDK package**:
   The root `kha` package must depend only on the Go standard library and internal packages within this module. Third-party libraries (such as `cobra`, `gjson`, or keychain bindings) belong strictly under `internal/cli/` or `cmd/kha/`.
4. **Preserve retry and idempotency guarantees**:
   - `kha.New` configures the product retry policy (`kha.NewClient` provides raw protocol defaults).
   - Never auto-generate `Idempotency-Key` headers on behalf of the caller, never replay multipart file uploads (`kha.File`), and never auto-retry `WorkItems.Poll` (where a duplicate request could claim a second work item).
5. **Keep CLI streams and secrets clean**:
   - `stdout` is strictly for command data (`table`, `json`, `yaml`, or raw strings); progress messages, spinners, warnings, and errors must go to `stderr`.
   - Read secrets from `stdin` or files, never from command-line flags that expose values in shell history or `ps`, and strip untrusted terminal escape sequences from server-controlled error messages.
6. **Code style, tests, and documentation**:
   - Write code comments, CLI output, error messages, and test diagnostics in clear English. Focus comments on *why* a decision exists or non-obvious protocol invariants rather than restating the code.
   - Use deterministic local tests (`httptest.NewServer`, `testing/synctest`) for retry, timeout, pagination, and SSE recovery behavior.
   - Update [`README.md`](README.md), [`docs/zh-CN/README.md`](docs/zh-CN/README.md), [`example_test.go`](example_test.go), or the embedded skill (`internal/cli/commands/skill/kha/`) when user-facing behavior changes, and record notable changes under `## [Unreleased]` in [`CHANGELOG.md`](CHANGELOG.md) (and [`docs/zh-CN/CHANGELOG.md`](docs/zh-CN/CHANGELOG.md) when updating Chinese documentation).

---

## Compatibility Rules

The platform evolves independently of released SDK and CLI binaries, so an older KHA must keep working against a newer server. Changes must preserve these rules:

- **Tolerate additions**: Unknown JSON object fields are ignored rather than rejected, and unknown event or union variants decode without error so callers can skip them. Required and non-null constraints that decoding depends on still apply.
- **Validate only what decoding needs**: The client does not re-run server-side business validation. Range, length, or enum checks that the server owns are left to the server's `400` response.
- **Reject only locally decidable CLI input**: The CLI rejects invalid flag combinations and malformed local files before sending a request (exit code `2`), but never guesses whether the server would accept a value.
- **Never generate idempotency keys**: Only a key supplied by the caller enables write retries, and one logical call reuses that key for every attempt. The one exception is `Sessions.RunTools`, which derives each answer's key from the server-assigned event ID it answers, so the key identifies the answer rather than an attempt.
- **Never retry Poll automatically**: A lost `WorkItems.Poll` response must not trigger a second request that could claim another work item.
- **Breaking changes go through the changelog**: Any change to exported Go signatures, CLI flags, or output formats is marked `(breaking change)` in [`CHANGELOG.md`](CHANGELOG.md) with migration steps.

---

## Submitting Pull Requests

For substantial new features or architectural changes, please [open an issue](https://github.com/MoonshotAI/kha/issues) first so we can align on the design before you invest time in implementation.

For bug fixes, documentation improvements, and agreed-upon features:

1. Fork the repository and create a branch from the latest `main`.
2. Make your changes, add or update tests, and verify that `go test ./...`, `go vet ./...`, and `golangci-lint run ./...` pass.
3. Open a [Pull Request](https://github.com/MoonshotAI/kha/pulls) describing what changed and why.

### How External PRs Are Integrated and Credited

Because KHA is developed alongside the Kimi Hosted Agents platform in an upstream monorepo, the `MoonshotAI/kha` repository is published as clean release snapshots rather than a commit-by-commit mirror:

1. **Review on GitHub**: Maintainers review your PR and run CI directly on GitHub.
2. **Upstream sync**: Once approved, maintainers import your change into the upstream development repository and close the GitHub PR with a reference to the upstream change.
3. **Included in the next release**: Your change ships in the very next `v0.X.Y` release snapshot (a PR is merged directly on GitHub only when the exact change is already present upstream).
4. **Public attribution**: Because snapshot releases squash commit history, we explicitly credit contributors by GitHub handle (`@username`) with a link to your PR in both [`CHANGELOG.md`](CHANGELOG.md) and the GitHub Release notes.
