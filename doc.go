// Package kha is the official Go SDK for Kimi Hosted Agents, providing a
// zero-dependency HTTP and Server-Sent Events (SSE) client, generated resource
// models, lazy pagination iterators, automatic event stream recovery, resource
// waiters, and webhook signature verification.
//
// # Client Initialization
//
// Most applications should initialize a [Client] with [New], which configures
// the default production endpoint (https://api.moonshot.cn), the
// Kimi-Api-Version header ([DefaultAPIVersion]), bearer token authentication,
// and an idempotency-aware retry policy:
//
//	client, err := kha.New("") // Reads KIMI_API_KEY when apiKey is empty.
//
// Use [ClientOption] values such as [WithBaseURL], [WithAPIVersion],
// [WithHTTPClient], [WithMaxRetries], or [WithAnonymous] to customize the
// client. When you need full manual control without product defaults or
// environment variable fallbacks, call the generated constructor [NewClient]
// directly.
//
// # Resource Clients and Requests
//
// A [Client] organizes operations by resource group:
//
//   - Agents ([AgentsClient]): configure Agents and inspect immutable versions.
//   - Environments ([EnvironmentsClient]): manage cloud sandboxes, self-hosted environments, and build readiness.
//   - Sessions ([SessionsClient]): create Sessions, send user messages, manage bindings/files/threads, and stream or page events.
//   - Files ([FilesClient]) and Artifacts ([ArtifactsClient]): upload inputs and download generated deliverables.
//   - Vaults ([VaultsClient]): store credentials and run MCP OAuth authorization flows.
//   - MemoryStores ([MemoryStoresClient]): manage shared Memory Stores, files, versions, and Dream policies.
//   - Skills ([SkillsClient]) and Plugins ([PluginsClient]): upload, validate, and publish bundles.
//   - Triggers ([TriggersClient]): schedule recurring runs and inspect execution history.
//   - WorkItems ([WorkItemsClient]): poll and manage self-hosted environment work queues.
//
// Ordinary unary operations accept a generated `<Operation>Request` struct and
// return `([HTTPResult][T], error)`. The decoded response model is stored in
// `result.Body`, alongside `result.StatusCode` and `result.Header`.
//
// # Optional and Nullable Fields
//
// Request and response models distinguish between two-state and three-state
// optionality:
//
//   - Two-state optional fields use pointers (`*T`). In Go 1.26+, pass `new(v)` to set a value or leave the field `nil` to omit it.
//   - Three-state patch fields where omission ("keep existing value"), explicit `null` ("clear value"), and a concrete value have distinct server semantics use [Optional][T]. Construct values with [NewOptional], and call [Optional.SetNull] to send an explicit `null`.
//
// Methods that download binary content return `HTTPResult[io.ReadCloser]`; the
// caller is responsible for closing `result.Body`.
//
// # Pagination
//
// Paginated resources provide both single-page `List*` methods and lazy `All*`
// iterators returning `iter.Seq2[*T, error]`:
//
//	for agent, err := range client.Agents.All(ctx, kha.ListAgentsRequest{}) {
//		if err != nil {
//			return err
//		}
//		fmt.Println(agent.ID, agent.Name)
//	}
//
// Breaking out of the loop stops further page fetches immediately.
//
// # Streaming Session Events
//
// Choose between finite history pagination and live SSE streaming:
//
//   - [SessionsClient.AllEvents] and [SessionsClient.AllThreadEvents] iterate through persisted history from oldest to newest and stop at the final page.
//   - [SessionsClient.Events] and [SessionsClient.ThreadEvents] open a live SSE stream (replaying retained history first unless [WithCursor]([CursorNow]) is passed) and automatically handle heartbeats, payload-ID deduplication, Last-Event-ID reconnects, and expired-cursor history backfill.
//
// Pass [WithDeltas](true) to receive incremental `agent.delta` token previews,
// and register [WithOnRecovery] to discard partial previews if an expired
// cursor triggers a history re-anchor.
//
// # Waiting for Asynchronous Work
//
// Built-in waiters block until asynchronous work reaches a terminal state or
// the context/timeout expires:
//
//   - [SessionsClient.SendMessageAndWait]: sends a user message and watches the Session event stream until the turn returns to idle (or fails with *[SessionError]).
//   - [EnvironmentsClient.WaitForReady]: polls an Environment until its build reaches `ready` (or fails with *[EnvironmentWaitFailedError]).
//   - [VaultsClient.WaitOAuthSession]: polls an MCP OAuth session until `authorized` (or fails with *[WaitFailedError]).
//   - [TriggersClient.WaitForTerminal]: polls a Trigger run until `completed`, `failed`, or `skipped`.
//
// # Custom Tools
//
// [SessionsClient.RunTools] answers an Agent's `agent.custom_tool_use` calls
// with the handlers registered through [WithCustomTool], and optionally
// decides tool calls held for approval through [WithToolConfirmation]. It
// skips calls already answered in history, never runs the same call twice
// across reconnects, and sends each answer with an Idempotency-Key derived
// from the call's event ID.
//
// # Retry Policy
//
// Clients constructed with [New] apply a product retry policy on top of the
// generated transport (clients built with [NewClient] retry nothing unless
// you configure it):
//
//   - Safe reads: GET and HEAD requests retry transient network errors, 429
//     (honoring Retry-After), and 5xx responses — except [WorkItemsClient.Poll]
//     (claiming a queue item is stateful), file downloads, and SSE streams
//     (which recover through cursors instead).
//   - Idempotent writes: JSON POST/PATCH endpoints retry only when the
//     request struct carries an IdempotencyKey; the SDK never invents keys
//     and never replays multipart uploads. A retried write that receives 409
//     Conflict means the original request with the same key is still in
//     flight, so the SDK backs off and retries until the server replays the
//     stored response; a 409 on the first attempt is a real conflict and
//     surfaces immediately.
//
// Deadlines belong to the request context: pass ctx deadlines rather than a
// short http.Client.Timeout, which would also cut long-lived SSE streams and
// file downloads.
//
// # Error Handling and Webhooks
//
// Non-2xx responses declared in the OpenAPI contract decode into *[Error]
// (an alias for *[ResponseError][*APIError]), whose Error() message includes
// the HTTP status alongside the decoded `type`, `message`, `param`, and
// `request_id`. Unexpected responses or stream errors return
// *[UnexpectedResponseError], *[StreamError], or *[StreamDecodeError]:
//
//   - Use status helpers ([IsInvalidRequest], [IsUnauthorized], [IsForbidden], [IsNotFound], [IsConflict], [IsRateLimited], [IsServerError]) for common branches.
//   - Use `errors.AsType[*kha.Error](err)` to inspect the decoded *[APIError] and HTTP metadata together, or call [ErrorDetails] to extract the *[ErrorObject] from either an HTTP *[Error] or an SSE *[StreamError].
//   - Use `errors.AsType[kha.HTTPError](err)` to read the HTTP status code and response headers (such as `Msh-Request-Id`) from any HTTP response error, including *[UnexpectedResponseError].
//
// For incoming Trigger webhook notifications, call [VerifyWebhookSignature]
// with the raw request body and the Vault's `webhook_secret` to verify the
// standard-webhooks HMAC-SHA256 signature and timestamp window.
package kha
