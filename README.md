<div align="center">

# KHA

**Go SDK and CLI for Kimi Hosted Agents**

[English](README.md) · [简体中文](docs/zh-CN/README.md) · [Documentation](https://platform.kimi.com/docs/hosted-agents/overview) · [API Reference](https://platform.kimi.com/docs/api-reference) · [Go Reference](https://pkg.go.dev/github.com/MoonshotAI/kha)

[![CI](https://github.com/MoonshotAI/kha/actions/workflows/ci.yml/badge.svg)](https://github.com/MoonshotAI/kha/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/MoonshotAI/kha.svg)](https://pkg.go.dev/github.com/MoonshotAI/kha)
[![GitHub Release](https://img.shields.io/github/v/release/MoonshotAI/kha)](https://github.com/MoonshotAI/kha/releases)
[![Go 1.27+](https://img.shields.io/badge/go-1.27%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

</div>

**KHA** is the official Go SDK and command-line client for [Kimi Hosted Agents](https://platform.kimi.com/docs/hosted-agents/overview), a managed Harness-as-a-Service platform for running long-lived, asynchronous AI agent tasks in cloud sandboxes.

Whether you are embedding agent workflows into a Go backend, automating tasks from shell scripts and CI pipelines, or giving an AI coding assistant access to remote sandboxes, KHA provides a single, consistent toolkit:

- **Zero-dependency Go SDK** (`github.com/MoonshotAI/kha`): Strongly typed models and resource clients generated from the OpenAPI contract, paired with built-in pagination iterators (`iter.Seq2`), resilient Server-Sent Events (SSE) stream recovery, resource waiters, idempotency-aware retries, and webhook signature verification.
- **Scriptable CLI** (`kha`): Human-friendly tables in the terminal, byte-exact JSON/YAML for scripts, built-in GJSON field extraction (`--transform`), live event streaming (`attach` and `events --watch`), interactive YAML editing (`edit`), and a bundled skill for AI coding assistants (`kha install-skill`).

---

## Core Concepts

If you are new to Kimi Hosted Agents, four building blocks explain how everything fits together:

1. **Agent** (`agent_...`): The blueprint for an assistant—its model, system instructions, built-in or MCP toolsets, Skills, Plugins, and optional multi-agent roster. Every update creates an immutable version.
2. **Environment** (`env_...`): The execution runtime where code and tools run—either a managed cloud sandbox (with custom `apt`, `pip`, `npm`, `go`, or `cargo` packages, setup scripts, and network rules) or a self-hosted worker queue.
3. **Session** (`sesn_...`): A stateful, durable workspace that pairs an **Agent** with an **Environment** and optional bindings (uploaded **Files**, secret **Vaults**, and shared **Memory Stores**). Tasks execute in turns on the cloud platform and keep running even if your local client disconnects.
4. **Events & Artifacts**: Every user message, model thought, tool call, agent reply, and status transition is recorded in the Session's append-only **Event** log. Deliverables saved by the Agent are published as versioned **Artifacts** (`art_...`) or stored in the Session filesystem.

> **Note:** KHA is a client for the hosted platform, not a local agent runtime. Model inference, sandbox execution, durable Session state, and fault recovery all run in Kimi Hosted Agents.

---

## Installation

### Prerequisites

- A [Kimi Open Platform](https://platform.kimi.com/console) account with Hosted Agents enabled and an [API Key](https://platform.kimi.com/console/api-keys).
- **Go 1.27 or newer** if you are using the Go SDK or building the CLI from source. Prebuilt CLI binaries require no runtime dependencies.

### Install the Go SDK

```sh
go get github.com/MoonshotAI/kha@latest
```

### Install the CLI

#### macOS and Linux

```sh
curl -fsSL https://ha-static.moonshot.cn/kha/install.sh | sh
export PATH="$HOME/.local/bin:$PATH"
kha version
```

The installer detects your OS and architecture, verifies the SHA-256 checksum, and places `kha` in `~/.local/bin` by default. Add `~/.local/bin` to your shell profile (`~/.zshrc`, `~/.bashrc`, etc.) so `kha` stays on your `PATH` in new terminals.

#### Windows (PowerShell)

```powershell
$installer = Join-Path ([IO.Path]::GetTempPath()) 'kha-install.ps1'
Invoke-WebRequest https://ha-static.moonshot.cn/kha/install.ps1 -UseBasicParsing -OutFile $installer
& $installer
$env:Path = "$(Join-Path $env:LOCALAPPDATA 'kha\bin');$env:Path"
kha version
```

By default, the binary is installed to `%LOCALAPPDATA%\kha\bin`. Add that directory to your user `Path` environment variable to make it available across new PowerShell sessions.

#### From Source or GitHub Releases

```sh
go install github.com/MoonshotAI/kha/cmd/kha@latest
```

Prebuilt archives and `checksums.txt` for macOS, Linux, and Windows (`amd64` and `arm64`) are also available on [GitHub Releases](https://github.com/MoonshotAI/kha/releases).

<details>
<summary><strong>Customizing the CLI installation (directory, pinned version, or GitHub mirror)</strong></summary>

- **Custom directory**: Pass `KHA_INSTALL_DIR=/custom/dir` on macOS/Linux or `-InstallDir C:\custom\dir` on Windows.
- **Pinned version**: Pass `KHA_VERSION=0.X.Y` on macOS/Linux or `-Version 0.X.Y` on Windows (or run `kha upgrade --version 0.X.Y` after installation).
- **Download mirror (`KHA_RELEASE_BASE_URL`)**: Defaults to `https://ha-static.moonshot.cn/kha`. To install or upgrade a specific version directly from GitHub Releases, set `KHA_RELEASE_BASE_URL=https://github.com/MoonshotAI/kha/releases/download` alongside an explicit version (`KHA_VERSION=0.X.Y` or `kha upgrade --version 0.X.Y`). Automatic latest-version resolution relies on `stable.txt` on the default download site, so keep the default URL for automatic update checks. Always use a trusted HTTPS mirror, as both binaries and checksums are fetched from this base URL.

</details>

---

## Quick Start

### 1. Run a Task from the CLI

Authenticate with your API Key (stored in your system keychain) and verify your connection:

```sh
kha login
kha whoami
```

> `kha login` prompts for your key without echoing it to the terminal. In CI or headless environments without a system keychain, set `export KIMI_API_KEY="sk-..."` via your secret manager instead. Never pass API Keys directly on the command line or commit them to source control.

Pick an existing Agent and Environment—or create a minimal pair if you are starting fresh:

```sh
# Option A: List existing Agents and Environments
kha agents list
kha environments list

# Option B: Create a default cloud Environment and a starter Agent
export ENVIRONMENT_ID=$(kha environments create \
  --name "Quickstart runtime" \
  --wait \
  --transform id -r)

export AGENT_ID=$(kha agents create \
  --name "Quickstart assistant" \
  --model kimi-k2.5 \
  --system "You are a concise engineering assistant." \
  --transform id -r)
```

Ensure the Environment build is ready, create a Session, and send your first prompt:

```sh
kha environments wait-ready "$ENVIRONMENT_ID"

export SESSION_ID=$(kha sessions create \
  --agent "$AGENT_ID" \
  --environment "$ENVIRONMENT_ID" \
  --title "Hello KHA" \
  --transform id -r)

kha sessions send "$SESSION_ID" \
  "Reply with a short introduction to Kimi Hosted Agents." \
  --wait --timeout 10m
```

`kha sessions send --wait` blocks until the turn finishes and prints the turn's terminal status (`stop_reason`), not the Agent's text reply. To inspect the conversation or extract the Agent's reply text:

```sh
# View all persisted events in the Session
kha sessions events "$SESSION_ID" --all

# Extract only the Agent's text blocks as plain text
kha sessions events "$SESSION_ID" --all \
  --transform '#(type=="agent.message")#.data.content.#(type=="text")#.text|@flatten' -r
```

**Good to know when running live tasks:**

- **Watch in real time**: Run `kha sessions attach "$SESSION_ID"` in another terminal to stream live output as plain text (`--since now` skips history replay).
- **Chat from the terminal**: `kha sessions attach "$SESSION_ID" --since now --input` sends each line you type as a `user.message` while the stream runs; messages sent mid-turn wait in the input queue (`kha sessions pending`). Piped input works too: after the last line, `attach` exits once the Session is idle.
- **Local detach vs. remote stop**: Pressing `Ctrl-C` during `attach` or hitting a local `--timeout` on `send --wait` only stops your local CLI wait—the Session keeps running in the cloud. Check `kha sessions get "$SESSION_ID"` before retrying, or run `kha sessions interrupt "$SESSION_ID"` if you want to stop the active turn on the server.

---

### 2. Run a Task with the Go SDK

Export your credentials and resource IDs:

```sh
export KIMI_API_KEY="sk-..."
export AGENT_ID="agent_..."
export ENVIRONMENT_ID="env_..."
```

Then run a complete task lifecycle in Go:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/MoonshotAI/kha"
)

func main() {
	ctx := context.Background()

	// kha.New("") reads KIMI_API_KEY from the environment and configures
	// the production endpoint, API version header, and retry policy.
	client, err := kha.New("")
	if err != nil {
		log.Fatal(err)
	}

	// 1. Wait for the Environment snapshot build to be ready.
	envID := os.Getenv("ENVIRONMENT_ID")
	if _, err := client.Environments.WaitForReady(ctx, envID); err != nil {
		log.Fatal(err)
	}

	// 2. Create a Session pairing the Agent and Environment.
	sessionResult, err := client.Sessions.Create(ctx, kha.CreateSessionHTTPRequest{
		Body: &kha.CreateSessionRequest{
			AgentID:       os.Getenv("AGENT_ID"),
			EnvironmentID: envID,
			Title:         new("KHA Go SDK quick start"),
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	sessionID := sessionResult.Body.ID

	// 3. Send a user message and wait for the turn to complete.
	turn, err := client.Sessions.SendMessageAndWait(
		ctx,
		sessionID,
		"Reply with a short introduction to Kimi Hosted Agents.",
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Turn stopped with reason:", turn.StopReason)

	// 4. Iterate through persisted Session events and print the Agent's reply.
	events := client.Sessions.AllEvents(ctx, kha.ListSessionEventsRequest{
		ID: sessionID,
	})
	for event, err := range events {
		if err != nil {
			log.Fatal(err)
		}
		if message := event.AsAgentMessageEvent(); message != nil {
			for _, part := range message.Data.Content {
				if text := part.AsTextContentBlock(); text != nil {
					fmt.Println(text.Text)
				}
			}
		}
	}
}
```

More compile-checked examples are available in [`example_test.go`](example_test.go).

---

## Go SDK Guide

### Client Initialization

Most applications should use `kha.New`, which configures the default endpoint (`https://api.moonshot.cn`), the `Kimi-Api-Version` header (`kha.DefaultAPIVersion`), bearer authentication, and the product retry policy:

```go
// Reads KIMI_API_KEY when apiKey is empty.
client, err := kha.New("",
	kha.WithBaseURL("https://api.moonshot.cn"),
	kha.WithMaxRetries(3),
)
```

If you need full manual control without product defaults or environment variable fallbacks, use the generated constructor `kha.NewClient`:

```go
client, err := kha.NewClient("https://api.moonshot.cn",
	kha.WithBearerToken("sk-..."),
	kha.WithHeader("Kimi-Api-Version", kha.DefaultAPIVersion),
	kha.WithMaxRetries(0),
)
```

> **Tip:** Avoid setting a short global `http.Client.Timeout` when passing `kha.WithHTTPClient(hc)`. A client-wide timeout also applies to long-lived SSE event streams and file downloads. Instead, use `context.WithTimeout` on individual requests.

### Requests, `HTTPResult[T]`, and Optional Fields

Every unary operation accepts a generated request struct and returns `(kha.HTTPResult[T], error)`. The decoded model lives in `result.Body`, alongside the HTTP status code and response headers:

```go
result, err := client.Agents.Get(ctx, kha.GetAgentRequest{ID: "agent_..."})
if err != nil {
	return err
}
fmt.Println("Agent:", result.Body.Name)
fmt.Println("Status:", result.StatusCode, "Request ID:", result.Header.Get("Msh-Request-Id"))
```

- **Two-state optional fields** (`*T`): Fields that can be omitted or set use pointers. In Go 1.26+, you can pass `new("value")` directly without a helper variable.
- **Three-state patch fields** (`kha.Optional[T]`): Update fields where omitting a field ("leave unchanged"), sending `null` ("clear the value"), and sending a concrete value have distinct meanings use `kha.Optional[T]`. Construct them with `kha.NewOptional(v)`, and call `SetNull()` to send an explicit `null`.
- **Downloads and streams**: File and bundle download methods return `kha.HTTPResult[io.ReadCloser]`. Always `Close()` `result.Body` when finished.

### Per-call Options

Every generated method ends with `opts ...kha.ClientOption`. Options passed to one call apply to that call only; the shared client is never modified, so it stays safe for concurrent use:

```go
result, err := client.Sessions.Create(ctx, req,
	kha.WithHeader("X-Correlation-Id", correlationID),
	kha.WithRequestHook(func(r *http.Request, body []byte) error {
		r.Header.Set("X-Signature", sign(body))
		return nil
	}),
)
```

- `kha.WithHeader` replaces the client's default value for the same header on this call.
- `kha.WithRequestHook` runs after the URL, credentials, and headers are assembled and receives a copy of the encoded body. With `kha.New`, retries happen below the hook, so it runs once per logical call and every retry reuses the same request.
- Deadlines belong to `ctx`, and response metadata is already in `HTTPResult`. Contract fields are set on the request struct rather than by rewriting the body.

### Pagination (`List*` vs. `All*`)

Every paginated resource offers two methods:

- `List*` fetches a single page and gives you `result.Body.Items` and `result.Body.NextPageToken`.
- `All*` returns a standard Go `iter.Seq2[*T, error]` iterator that lazily fetches subsequent pages as you loop:

```go
for agent, err := range client.Agents.All(ctx, kha.ListAgentsRequest{}) {
	if err != nil {
		return err
	}
	fmt.Println(agent.ID, agent.Name)
}
```

Breaking out of the `for` loop immediately stops further page requests. If a page fails, the error is yielded once and iteration ends.

### Live Event Streaming and Recovery

Choose your event reader based on whether you want a finite snapshot or a live feed:

- **Finite history (`Sessions.AllEvents` / `Sessions.AllThreadEvents`)**: Pages through persisted events from oldest to newest and stops at the end of history.
- **Live SSE stream (`Sessions.Events` / `Sessions.ThreadEvents`)**: Replays retained history by default and stays open to stream new events in real time:

```go
stream := client.Sessions.Events(ctx, "sesn_...",
	kha.WithCursor(kha.CursorNow), // Or omit to replay retained history first
	kha.WithDeltas(true),          // Opt into live agent.delta token previews
	kha.WithOnRecovery(func() {
		// Called if an expired cursor required history backfill;
		// discard any buffered agent.delta previews before this point.
	}),
)

for event, err := range stream {
	if err != nil {
		return err
	}
	switch {
	case event.AsAgentDeltaEvent() != nil:
		// Render incremental preview chunks
	case event.AsAgentMessageEvent() != nil:
		// Authoritative completed message
	}
}
```

`Sessions.Events` handles SSE heartbeats, control frames, payload-ID deduplication (up to 4,096 recent IDs), automatic `Last-Event-ID` reconnects with exponential backoff, and expired-cursor recovery (anchoring at `cursor=now`, backfilling missed history, and resuming live delivery without a gap). Cancel `ctx` or `break` out of the loop when your application is done listening.

### Waiting for Asynchronous Work and Retry Safety

KHA includes dedicated waiters for asynchronous platform operations:

| Helper | Mechanism | Options |
| --- | --- | --- |
| `client.Sessions.SendMessageAndWait(ctx, sessionID, text, opts...)` | Sends a message and watches the Session event stream until the turn returns to `idle` (or fails with `*kha.SessionError`). | `kha.WithTimeout(d)` (default `10m`; `WithInterval` is ignored) |
| `client.Environments.WaitForReady(ctx, envID, opts...)` | Polls the Environment until its snapshot build is `ready` (or fails with `*kha.EnvironmentWaitFailedError`). | `kha.WithEnvironmentTimeout(d)`, `kha.WithEnvironmentInterval(d)` |
| `client.Vaults.WaitOAuthSession(ctx, vaultID, sessionID, opts...)` | Polls an MCP OAuth authorization session until `authorized` (or fails with `*kha.WaitFailedError`). | `kha.WithTimeout(d)`, `kha.WithInterval(d)` |
| `client.Triggers.WaitForTerminal(ctx, triggerID, runID, opts...)` | Polls a Trigger run until `completed`, `failed`, or `skipped`. | `kha.WithTimeout(d)`, `kha.WithInterval(d)` |

### Running Custom Tools

Agents can declare `custom` tools that your application executes. `Sessions.RunTools` follows the Session event stream, runs the handler registered for each `agent.custom_tool_use` call, and sends the result back as `user.custom_tool_result`:

```go
err := client.Sessions.RunTools(ctx, sessionID,
	kha.WithCustomTool("get_weather", func(ctx context.Context, call *kha.AgentCustomToolUseEvent) ([]*kha.ToolResultContent, error) {
		city, _ := call.Data.Input["city"].(string)
		return []*kha.ToolResultContent{kha.NewToolResultContentTextContentBlock(&kha.TextContentBlock{
			Type: kha.TextContentBlockTypeText, Text: lookupWeather(city),
		})}, nil
	}),
	// Optional: decide tool calls that the Agent's permission policy holds for approval.
	kha.WithToolConfirmation(func(ctx context.Context, call *kha.AgentToolUseEvent) (kha.ToolConfirmation, error) {
		return kha.ToolConfirmation{Allow: call.Data.ToolUse.Name != "bash", DenyMessage: "not allowed"}, nil
	}),
)
```

- A handler error is reported to the Agent as an error result (`is_error: true`), and a call without a registered handler is answered with an error so the Agent never waits forever.
- On start, the runner reads the Session history and answers only calls that have no result yet. It never runs the same call twice, even across reconnects.
- Each answer carries an `Idempotency-Key` derived from the call's event ID, so a lost write is retried without answering twice.
- `RunTools` runs until `ctx` is done (returning `ctx.Err()`) or the stream, a confirmation handler, or a write fails. Without `WithToolConfirmation`, calls waiting for approval are left for another client.

**How retries work in `kha.New`:**

- **Safe reads**: `GET` and `HEAD` requests automatically retry transient network errors, `429 Too Many Requests` (honoring `Retry-After`), and `5xx` responses—**except** `WorkItems.Poll` (claiming a queue item is stateful, so a blind retry could claim a second item), file downloads, and SSE streams (which use cursor-based recovery instead).
- **Idempotent writes**: Supported JSON `POST`/`PATCH` endpoints are retried only when you set `IdempotencyKey: new("your-uuid")` on the request struct. The SDK never invents idempotency keys on your behalf, and never replays multipart uploads (`kha.File`). If one of those retries receives `409 Conflict`, the original request with the same key is still running, so the SDK backs off and retries until the server replays the stored response; a `409` on the first attempt is returned immediately.

### Error Handling

Non-2xx responses declared in the API contract return `*kha.Error` (an alias for `*kha.ResponseError[*kha.APIError]`), whose `Error()` message automatically includes the HTTP status alongside `type`, `message`, `param`, and `request_id`. Unexpected statuses or truncated responses return `*kha.UnexpectedResponseError` (which also implements `kha.HTTPError`). You can inspect errors at whatever level of detail you need:

```go
result, err := client.Agents.Get(ctx, kha.GetAgentRequest{ID: "agent_..."})
if err != nil {
	// 1. Quick status classification
	if kha.IsNotFound(err) {
		log.Println("Agent does not exist")
		return
	}

	// 2. Decoded API error model + HTTP status/headers in one check
	if apiErr, ok := errors.AsType[*kha.Error](err); ok {
		log.Printf("HTTP %d [%s]: %s",
			apiErr.StatusCode(),
			apiErr.Body.Error.Type,
			apiErr.Body.Error.Message,
		)
	} else if httpErr, ok := errors.AsType[kha.HTTPError](err); ok {
		// 3. Any HTTP response error (including *kha.UnexpectedResponseError)
		log.Printf("HTTP %d (request id: %s)",
			httpErr.StatusCode(),
			httpErr.Header().Get("Msh-Request-Id"),
		)
	}
}
```

Status helpers include `kha.IsInvalidRequest` (`400`), `kha.IsUnauthorized` (`401`), `kha.IsForbidden` (`403`), `kha.IsNotFound` (`404`), `kha.IsConflict` (`409`), `kha.IsRateLimited` (`429`), and `kha.IsServerError` (`5xx`). `kha.ErrorDetails(err)` also extracts `*kha.ErrorObject` from either an HTTP `*kha.Error` or an SSE `*kha.StreamError`.

### Tracing and Transport Middleware

The root SDK package has no third-party dependencies, so tracing and response-side middleware plug in through `kha.WithHTTPClient`. Every response carries an `Msh-Request-Id` header (also attached to CLI error output); quote it when reporting a problem.

To join your traces with platform logs, wrap the transport with [`otelhttp`](https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp). The platform reads the propagated `traceparent` header and returns its trace ID as `Msh-Request-Id`:

```go
import "go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

client, err := kha.New("", kha.WithHTTPClient(&http.Client{
	Transport: otelhttp.NewTransport(http.DefaultTransport),
}))
```

For logging, metrics, or other response-side behavior, wrap the transport with your own `http.RoundTripper`. With `kha.New`, the product retry policy sits above your transport, so the wrapper observes each individual attempt:

```go
type logTransport struct{ next http.RoundTripper }

func (t logTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.next.RoundTrip(req)
	if err == nil {
		log.Printf("%s %s -> %d in %s (request id: %s)", req.Method, req.URL.Path,
			resp.StatusCode, time.Since(start), resp.Header.Get("Msh-Request-Id"))
	}
	return resp, err
}

client, err := kha.New("", kha.WithHTTPClient(&http.Client{
	Transport: logTransport{next: http.DefaultTransport},
}))
```

`kha.New` copies the `http.Client` you pass and never mutates it. Responses that do not match the API contract return `*kha.UnexpectedResponseError`, which keeps up to 1 MiB of the raw body for diagnosis.

### Embedding a Self-Hosted Worker

`kha worker poll` is a thin wrapper over the public [`worker`](worker/) and [`tools`](tools/) packages. Import them to run the same lease and write-back loop inside your own process, or implement `tools.Executor` to replace the built-in sandbox tools:

```go
pollClient, err := kha.NewPollClient(baseURL, workKey, kha.WithHTTPClient(tracedClient))
poller, err := worker.NewPoller(worker.PollConfig{
	BaseURL:       baseURL,
	PollClient:    pollClient,
	ClientOptions: []kha.ClientOption{kha.WithHTTPClient(tracedClient)}, // per-work Session clients
	EnvironmentID: environmentID,
	WorkerID:      worker.NewWorkerID(),
	Executor:      tools.NewExecutor(4 * time.Minute),
	Workdir:       "/srv/agent-work",
})
err = poller.Run(ctx)
```

Worker log records carry `environment_id` and `worker_id`, and records about a work item add `work_item_id`, `session_id`, and `generation`, matching the identifiers the platform shows for a lease. `kha worker poll` and `kha worker run` write them to stderr as `key=value` text by default; pass `--log-format json` for one JSON object per line.

`tools.NewExecutor` runs the sandbox side of both built-in toolsets: `read_file`, `write_file`, `edit_file`, `shell`, `todo_read`, and `todo_write` for `default_toolset_20260901`, and `read`, `write`, `edit`, `bash`, `glob`, and `grep` for `default_toolset_20261101`. `glob` and `grep` call [ripgrep](https://github.com/BurntSushi/ripgrep), so install `rg` on the worker host. Web search, web fetch, artifacts, MCP, and orchestration tools always run on the platform.

### Verifying Trigger Webhooks

When a Trigger is configured with a webhook notification URL, `kha.UnwrapWebhook` verifies an incoming delivery against the `webhook_secret` stored in your Vault and decodes it into a typed `*kha.TriggerWebhookEvent`. Pass the raw request body, because the signature covers the exact bytes:

```go
func handleTriggerWebhook(w http.ResponseWriter, r *http.Request) {
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	event, err := kha.UnwrapWebhook(webhookSecret, r.Header, rawBody)
	if err != nil {
		// *kha.WebhookVerificationError for a bad signature or a stale timestamp.
		http.Error(w, "invalid webhook", http.StatusUnauthorized)
		return
	}
	switch event.Data.Type {
	case kha.TriggerWebhookEventTypeTriggerRunFailed:
		// event.Data.ID is the TriggerRun ID; fetch it for the error details.
	case kha.TriggerWebhookEventTypeTriggerPaused:
		// event.Data.ID is the Trigger ID; fetch it for paused_reason.
	}
	w.WriteHeader(http.StatusNoContent)
}
```

- The verifier checks the HMAC-SHA256 signature against all `v1` entries in `Webhook-Signature` (supporting zero-downtime secret rotation) and enforces a ±5-minute timestamp replay window.
- Payloads are thin references: fetch the TriggerRun or Trigger by `event.Data.ID` for current state. Delivery is at-least-once, so deduplicate by `event.ID`, and tolerate event types you do not recognize.
- `kha.VerifyWebhookSignature` checks a signature without decoding. `kha.ParseWebhookEvent` decodes without verifying, for bodies already verified elsewhere (for example by a gateway) and for tests.

---

## CLI Guide: Terminal, Scripts, and AI Coding Assistants

### Everyday Commands and Offline Reference

Every CLI command follows `kha <resource> <verb> [flags]`:

```sh
# Browse built-in offline documentation matching your installed binary
kha docs
kha docs sessions
kha docs sessions send

# Interactively edit an Agent or Environment in $KHA_EDITOR / $EDITOR (YAML)
kha agents edit "$AGENT_ID"

# Enable tab completion (completes flags, enums, and live resource IDs)
kha completion zsh > "${fpath[1]}/_kha"
```

### Scripting, Output Formats, and GJSON Transforms

Data commands write **only data to `stdout`** and send progress notes, warnings, spinners, and errors to `stderr`:

```sh
# Output formats: table (default), json, jsonl, or yaml
kha agents list -o json
kha agents get "$AGENT_ID" -o yaml

# Auto-paginate across all pages with --all
kha sessions list --all -o json

# Stream one compact JSON object per line; --transform applies to each item
kha sessions list --all -o jsonl | jq -c 'select(.status == "idle")'
kha sessions list --all -o jsonl --transform id -r

# Filter on the server: RFC 3339 time ranges, official resources, categories, and owners
kha sessions list --updated-at-gte 2026-08-01T00:00:00Z --all -o json
kha agents list --official-only
kha skills list --category engineering
kha triggers list --agent "$AGENT_ID"

# Reshape or extract fields with --transform (GJSON syntax) and -r (raw strings)
kha agents list --transform 'items.#.id' -r
kha agents list --transform '{id,name}'

# Structured errors on stderr for automated callers
kha sessions get sesn_invalid --error-output json
```

### Passing Request Bodies, Files, and Secrets

Create and update commands take scalar flags (`--name`, `--model`, `--system-file`) plus **structured flags** for the request's object and array fields. Structured flag values accept relaxed YAML (`{name: x}`) and strict JSON interchangeably, and repeatable flags append one array element per occurrence:

```sh
kha agents create --name analyst --model kimi-k3 \
  --tool '{type: custom, name: search, description: web search, input_schema: {type: object}}' \
  --mcp-server '{name: github, url: https://mcp.example}' \
  --skill "sk_xxx@2" \
  --metadata team=infra

kha sessions create --agent "$AGENT_ID" --environment "$ENVIRONMENT_ID" \
  --resource '{type: vault, vault_id: vlt_xxx}' \
  --override '{model: kimi-k3}'

kha environments create --name analysis \
  --networking limited --allow-host example.com \
  --pip pandas==2.3.2 --setup-script-file setup.sh
```

When a body grows past a few fields, write it as one JSON/YAML document and pipe it through `--file` instead — field flags and `--file` are not mixed on the same command:

```sh
# Read a full configuration from a YAML/JSON file or stdin
kha agents create --file agent.yaml
jq -n '{title: "Updated title"}' | kha sessions update "$SESSION_ID" --file -

# Pass multi-line prompt or message content from a file or stdin
kha agents update "$AGENT_ID" --version "$VERSION" --system-file system.md
kha sessions send "$SESSION_ID" --text-file prompt.md --wait

# Send rich content blocks (e.g. a file reference) as a full user.message event
kha sessions send "$SESSION_ID" --wait \
  --event '{type: user.message, data: {content: [{type: text, text: "summarize this CSV"}, {type: file, file_id: "file_xxx", mime_type: text/csv, title: data.csv, path: upload/data.csv}]}}'

# Pass secrets via stdin so they never appear in shell history or `ps`
kha login --api-key-stdin < api-key.txt
kha vaults credentials create "$VAULT_ID" --type environment_variable --name GITHUB_TOKEN --value-stdin
```

### Exit Codes

| Exit Code | Meaning |
| --- | --- |
| `0` | Success (`plugins validate` also exits `0` when valid, even with warnings). |
| `1` | Runtime, API, or validation failure (`plugins validate` keeps findings on `stdout`). |
| `2` | Usage error (invalid flags, missing arguments, or empty message text; no API request was sent). |
| `130` | Interrupted by `SIGINT`/`SIGTERM` (pressing `Ctrl-C` on `sessions attach` detaches locally; the remote turn keeps running). |

### Use with AI Coding Assistants (`kha install-skill`)

KHA bundles an agent skill (`SKILL.md` and task/field references) that teaches AI coding assistants such as **Kimi Code**, **Claude Code**, **Codex**, and **Cursor** how to operate Kimi Hosted Agents safely through the CLI:

```sh
kha install-skill      # Install into project: ./.agents/skills/kha/
kha install-skill -g   # Install into user home: ~/.agents/skills/kha/
```

Restart or refresh your coding assistant session after installation. Running `kha upgrade` automatically updates the CLI binary and refreshes any skill copies installed in the standard project and user directories.

### Environment Variables and Profiles

| Variable | Description |
| --- | --- |
| `KIMI_API_KEY` | API Key used when `--api-key` is not set (ideal for CI and headless servers). |
| `KHA_BASE_URL` | Override the API base URL (`https://api.moonshot.cn` by default). |
| `KHA_PROFILE` | Select a named profile saved by `kha login`. |
| `KHA_EDITOR` | Editor command for `kha <resource> edit` (falls back to `$VISUAL`, `$EDITOR`, then `vi`). |
| `KHA_NO_UPDATE_CHECK=1` | Disable background CLI update notices (automatically disabled in CI). |
| `KHA_RELEASE_BASE_URL` | Override the release download mirror for `install.sh`, `install.ps1`, and `kha upgrade`. |

**Credential precedence:**

- By default, KHA resolves `--api-key` > `KIMI_API_KEY` > the selected profile's key, and `--base-url` > `KHA_BASE_URL` > the selected profile's base URL > `https://api.moonshot.cn`.
- When you pass `--profile <name>` explicitly on a command, KHA uses that profile's saved credentials and endpoint, ignoring `KIMI_API_KEY` and `KHA_BASE_URL` so ambient environment variables do not leak across profiles.
- Without `--profile` or `KHA_PROFILE`, the config's current profile applies. `kha profile list` shows saved profiles, marks the current one, and reports where each key is stored without printing it; `kha profile use <name>` switches the current profile.

---

## Documentation & Resources

Platform guides are currently maintained in Chinese; the changelog is available in [English](CHANGELOG.md) and [简体中文](docs/zh-CN/CHANGELOG.md).

| Topic | Resource | Description |
| --- | --- | --- |
| Platform Overview | [Hosted Agents Overview](https://platform.kimi.com/docs/hosted-agents/overview) | Architecture, cloud sandboxes, Skills, Plugins, and use cases |
| End-to-End Tutorial | [Official Quick Start](https://platform.kimi.com/docs/hosted-agents/quickstart) | Creating Agents and Environments, running Sessions, and downloading Artifacts |
| CLI Reference | [CLI Guide](https://platform.kimi.com/docs/hosted-agents/kha) & `kha docs` | Profiles, output transforms, request files, and troubleshooting |
| Streaming Protocol | [Event Stream Guide](https://platform.kimi.com/docs/hosted-agents/event-stream) | Event types, cursors, SSE reconnection, and history backfill |
| REST Contract | [API Reference](https://platform.kimi.com/docs/api-reference) | Endpoints, request/response schemas, and status codes |
| Go SDK Reference | [pkg.go.dev/github.com/MoonshotAI/kha](https://pkg.go.dev/github.com/MoonshotAI/kha) | Complete Go API documentation for all clients, models, and helpers |
| Runnable Go Examples | [`example_test.go`](example_test.go) | Compile-checked examples for Sessions, pagination, streaming, and error handling |
| Release History | [`CHANGELOG.md`](CHANGELOG.md) · [简体中文](docs/zh-CN/CHANGELOG.md) | Version history, new features, breaking changes, and upgrade notes |

---

## Versioning

KHA follows Semantic Versioning (`v0.X.Y`). While in `v0.x`, the public Go API and CLI flags may evolve between minor releases; breaking changes and migration steps are documented in [`CHANGELOG.md`](CHANGELOG.md).

The Kimi Hosted Agents REST API is versioned independently via the `Kimi-Api-Version` header (currently `2026-09-01-beta` by default, exposed as `kha.DefaultAPIVersion`).

## Contributing & Feedback

Contributions, bug reports, and feature requests are welcome!

- **Contribute code or docs**: Read [`CONTRIBUTING.md`](CONTRIBUTING.md) for local build/test commands, repository structure, and how pull requests are integrated. The repository is completely self-contained—only Go 1.27+ is needed to build and run the test suite.
- **Report issues**: Open a [GitHub Issue](https://github.com/MoonshotAI/kha/issues) with your `kha version` output, a minimal reproduction (CLI command or Go snippet), and the `Msh-Request-Id` response header if applicable. Please redact API Keys, tokens, and sensitive payloads before posting.

## License

Licensed under the [Apache-2.0 License](LICENSE).
