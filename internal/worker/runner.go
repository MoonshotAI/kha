// runner.go executes local sandbox calls from the coordinator event stream.
// session_thread_id scopes deduplication and result routing across child threads.
// The worker never writes denials or platform-tool results; those belong to the platform loop.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	jsonv2 "encoding/json/v2"

	kha "github.com/MoonshotAI/kha"
	"github.com/MoonshotAI/kha/internal/tools"
)

var errSessionTerminated = errors.New("session terminated")

// Idle settlement requires no new events or in-flight calls and an explicit idle status.
var errIdleTimeout = errors.New("session idle timeout")

const (
	// Local execution should finish before the platform loop's tool_timeout so the
	// worker can release its execution slot.
	defaultToolTimeout   = 5 * time.Minute
	defaultShutdownGrace = 30 * time.Second
	maxPollBackoff       = 30 * time.Second
	sendBaseDelay        = time.Second
	sendMaxDelay         = 30 * time.Second
	// Result retries are useful only while the platform loop is still waiting for
	// the tool result.
	defaultSendWindow = 5 * time.Minute
)

// execConcurrency bounds local resource usage; it is not file-level locking.
const execConcurrency = 8

// answered is scoped by (threadID, tool_use_id) because tool_use IDs are stable
// only within their owning thread.
type threadFeed struct {
	seen     map[string]bool
	answered map[string]bool
	pending  map[string]pendingApproval
}

// Request and confirmation may arrive in either order and are correlated by the
// confirmation's data.event_id.
type pendingApproval struct {
	request *kha.AgentToolUseEvent
	result  kha.UserToolConfirmationResult
}

func newThreadFeed() *threadFeed {
	return &threadFeed{
		seen:     make(map[string]bool),
		answered: make(map[string]bool),
		pending:  make(map[string]pendingApproval),
	}
}

func dedupKey(threadID, toolUseID string) string {
	return threadID + "\x00" + toolUseID
}

func envelopeThreadID(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}

type runner struct {
	client    *kha.Client
	sessionID string
	executor  tools.Executor
	workdir   string
	log       *slog.Logger

	poll          time.Duration
	toolTimeout   time.Duration // cooperative cancellation; should be <= platform loop tool_timeout
	shutdownGrace time.Duration
	maxIdle       time.Duration // 0 disables idle settlement
	sendWindow    time.Duration

	feed  *threadFeed
	sem   chan struct{}
	wg    sync.WaitGroup
	fatal chan error
	// Lease loss must stop both new dispatch and in-flight execution.
	stopRunner context.CancelCauseFunc

	lastActivity time.Time
	sessionIdle  bool
}

func newRunner(client *kha.Client, sessionID string, executor tools.Executor, workdir string, log *slog.Logger, poll, toolTimeout, maxIdle, sendWindow time.Duration, stopRunner context.CancelCauseFunc) *runner {
	if poll <= 0 {
		poll = time.Second
	}
	if toolTimeout <= 0 {
		toolTimeout = defaultToolTimeout
	}
	if sendWindow <= 0 {
		sendWindow = defaultSendWindow
	}
	if log == nil {
		log = slog.Default()
	}
	return &runner{
		client: client, sessionID: sessionID, executor: executor, workdir: workdir, log: log,
		poll: poll, toolTimeout: toolTimeout, shutdownGrace: defaultShutdownGrace,
		maxIdle: maxIdle, sendWindow: sendWindow,
		feed:       newThreadFeed(),
		sem:        make(chan struct{}, execConcurrency),
		fatal:      make(chan error, 1),
		stopRunner: stopRunner,
	}
}

// Reentry after rejected idle settlement preserves event and deduplication state.
// Exit waits up to shutdownGrace for in-flight execution and result writes.
func (r *runner) run(ctx context.Context) (runErr error) {
	r.lastActivity = time.Now()
	r.log.InfoContext(ctx, "worker started", "poll_interval", r.poll)
	defer func() {
		if err := r.waitInFlight(); err != nil {
			runErr = err
		}
		select {
		case err := <-r.fatal:
			runErr = err
		default:
		}
	}()
	failures := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := r.pollOnce(ctx)
		var wait time.Duration
		switch {
		case errors.Is(err, errSessionTerminated):
			r.log.InfoContext(ctx, "session terminated, worker exits")
			return nil
		case errors.Is(err, ErrLeaseLost) || isLeaseLost(err):
			// Data-plane 401/412 also means lease loss (the swt expires with its
			// generation): abandon the work and perform no further unfenced reads or writes.
			r.log.ErrorContext(ctx, "worker lease lost, exiting", "error", err)
			return ErrLeaseLost
		case err != nil && kha.IsProtocolError(err):
			// Contract/decode errors do not heal with retries: exit fatally and report
			// the error until the worker is upgraded, rather than livelocking in backoff.
			r.log.ErrorContext(ctx, "poll hit a deterministic protocol error, worker exits", "error", err)
			return err
		case err != nil:
			failures++
			wait = r.backoff(failures)
			r.log.WarnContext(ctx, "poll failed", "error", err, "failures", failures, "retry_in", wait)
		default:
			failures = 0
			wait = r.poll
			if r.maxIdle > 0 && len(r.sem) == 0 && r.sessionIdle &&
				time.Since(r.lastActivity) > r.maxIdle {
				select {
				case err := <-r.fatal:
					return err
				case <-ctx.Done():
					return ctx.Err()
				default:
				}
				r.log.InfoContext(ctx, "session idle, settling work", "max_idle", r.maxIdle)
				return errIdleTimeout
			}
		}
		select {
		case err := <-r.fatal:
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

func (r *runner) backoff(failures int) time.Duration {
	d := r.poll
	for i := 1; i < failures && d < maxPollBackoff; i++ {
		d *= 2
	}
	return jitter(min(d, maxPollBackoff))
}

func (r *runner) reportFatal(err error) {
	select {
	case r.fatal <- err:
	default:
	}
	// Stop both new dispatch and in-flight execution; WithoutCancel on the send
	// path is intentional.
	if r.stopRunner != nil {
		r.stopRunner(err)
	}
}

func (r *runner) waitInFlight() error {
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-time.After(r.shutdownGrace):
		return errors.New("shutdown grace expired with in-flight tool calls")
	}
}

// A separate first pass collects tool results because descending pages place
// tool_result before tool_use. Full pages backtrack to a seen watermark to recover
// unanswered calls without repeating answered history; a cap prevents unbounded
// recovery.
func (r *runner) pollOnce(ctx context.Context) error {
	feed := r.feed
	params := kha.ListSessionEventsRequest{
		ID:       r.sessionID,
		Order:    new(kha.EventOrderDesc),
		PageSize: new(int32(eventPageSize)),
	}
	result, err := r.client.Sessions.ListEvents(ctx, params)
	if err != nil {
		return err
	}
	page := result.Body
	page.Items = slices.DeleteFunc(page.Items, func(event *kha.SessionEvent) bool { return event == nil })
	items := page.Items
	for pages := 1; ; pages++ {
		if len(page.Items) < eventPageSize || page.NextPageToken == nil || *page.NextPageToken == "" {
			break
		}
		watermark := kha.SessionEventID(items[len(items)-1])
		if watermark != "" && feed.seen[watermark] {
			break
		}
		if pages >= maxEventBacktrackPages {
			r.log.WarnContext(ctx, "event backtrack hit page cap, older unseen events skipped", "pages", pages, "page_size", eventPageSize)
			break
		}
		params.PageToken = page.NextPageToken
		result, err = r.client.Sessions.ListEvents(ctx, params)
		if err != nil {
			return err
		}
		page = result.Body
		page.Items = slices.DeleteFunc(page.Items, func(event *kha.SessionEvent) bool { return event == nil })
		items = append(items, page.Items...)
	}
	for _, evt := range items {
		if e := evt.AsAgentToolResultEvent(); e != nil && e.Data.ToolResult.ToolUseID != "" {
			feed.answered[dedupKey(envelopeThreadID(e.SessionThreadID), e.Data.ToolResult.ToolUseID)] = true
		}
	}
	for eventID, p := range feed.pending {
		if p.request != nil && feed.answered[dedupKey(envelopeThreadID(p.request.SessionThreadID), p.request.Data.ToolUse.ID)] {
			delete(feed.pending, eventID)
		}
	}
	for _, evt := range slices.Backward(items) {
		if id := kha.SessionEventID(evt); id != "" {
			if feed.seen[id] {
				continue
			}
			feed.seen[id] = true
		}
		r.lastActivity = time.Now()
		if e := evt.AsAgentToolUseEvent(); e != nil {
			if err := r.handleToolUse(ctx, feed, e); err != nil {
				return err
			}
			continue
		}
		if e := evt.AsUserToolConfirmationEvent(); e != nil {
			if err := r.handleConfirmation(ctx, feed, e); err != nil {
				return err
			}
			continue
		}
		if e := evt.AsSessionStatusEvent(); e != nil {
			// Only an explicit idle status permits idle settlement.
			r.sessionIdle = e.Data.Status == kha.SessionStateIdle
			if e.Data.Status == kha.SessionStateTerminated {
				return errSessionTerminated
			}
		}
	}
	return nil
}

func (r *runner) handleToolUse(ctx context.Context, feed *threadFeed, evt *kha.AgentToolUseEvent) error {
	callID, name := evt.Data.ToolUse.ID, evt.Data.ToolUse.Name
	threadID := envelopeThreadID(evt.SessionThreadID)
	key := dedupKey(threadID, callID)
	if callID == "" || feed.answered[key] {
		delete(feed.pending, evt.ID)
		return nil
	}
	if evt.Data.MCP != nil {
		delete(feed.pending, evt.ID)
		// Remote MCP tools never enter the local allowlist.
		feed.answered[key] = true
		r.log.DebugContext(ctx, "skipping remote MCP tool call", "thread_id", threadID, "tool_use_id", callID, "tool_name", name)
		return nil
	}
	if !tools.IsSandboxTool(name) {
		delete(feed.pending, evt.ID)
		// Writing a platform-tool result would race the platform loop.
		feed.answered[key] = true
		r.log.DebugContext(ctx, "skipping platform tool call", "thread_id", threadID, "tool_use_id", callID, "tool_name", name)
		return nil
	}
	if evt.Data.EvaluatedPermission == kha.PermissionPolicyAlwaysAsk {
		p := feed.pending[evt.ID]
		p.request = evt
		feed.pending[evt.ID] = p
		return r.resolveConfirmation(ctx, feed, evt.ID)
	}
	delete(feed.pending, evt.ID)
	feed.answered[key] = true
	raw, err := toolInputJSON(evt.Data.ToolUse.Input)
	if err != nil {
		return err
	}
	return r.dispatchTool(ctx, threadID, callID, name, raw)
}

// The generated SDK decodes tool input into a map, so the original bytes are
// unavailable.
func toolInputJSON(input map[string]any) (json.RawMessage, error) {
	raw, err := jsonv2.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("kha worker: encode tool input: %w", err)
	}
	return raw, nil
}

func (r *runner) handleConfirmation(ctx context.Context, feed *threadFeed, evt *kha.UserToolConfirmationEvent) error {
	p, ok := feed.pending[evt.Data.EventID]
	if !ok && feed.seen[evt.Data.EventID] {
		return nil
	}
	if p.result == "" {
		p.result = evt.Data.Result
	}
	feed.pending[evt.Data.EventID] = p
	return r.resolveConfirmation(ctx, feed, evt.Data.EventID)
}

func (r *runner) resolveConfirmation(ctx context.Context, feed *threadFeed, eventID string) error {
	p := feed.pending[eventID]
	if p.request == nil || p.result == "" {
		return nil
	}
	delete(feed.pending, eventID)
	threadID := envelopeThreadID(p.request.SessionThreadID)
	call := p.request.Data.ToolUse
	key := dedupKey(threadID, call.ID)
	if feed.answered[key] {
		return nil
	}
	if p.result != kha.UserToolConfirmationResultAllow {
		feed.answered[key] = true
		r.log.InfoContext(ctx, "tool call denied", "thread_id", threadID, "tool_use_id", call.ID, "tool_name", call.Name)
		return nil
	}
	feed.answered[key] = true
	raw, err := toolInputJSON(call.Input)
	if err != nil {
		return err
	}
	return r.dispatchTool(ctx, threadID, call.ID, call.Name, raw)
}

func (r *runner) dispatchTool(ctx context.Context, threadID, callID, name string, input json.RawMessage) error {
	log := r.log.With("thread_id", threadID, "tool_use_id", callID, "tool_name", name)
	select {
	case r.sem <- struct{}{}:
	case err := <-r.fatal:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
	// Recheck after acquiring a slot: select is random when cancellation and sem are
	// both ready, and heartbeat lease loss cancels runCtx without writing fatal.
	select {
	case err := <-r.fatal:
		<-r.sem
		return err
	case <-ctx.Done():
		<-r.sem
		return ctx.Err()
	default:
	}
	log.InfoContext(ctx, "executing tool call")
	r.wg.Go(func() {
		defer func() { <-r.sem }()
		res := r.execute(ctx, name, input)
		// Let started result writes finish during shutdown; sendWindow bounds retries
		// independently of poll cancellation.
		if err := r.sendToolResult(context.WithoutCancel(ctx), threadID, callID, res); err != nil {
			if isLeaseLost(err) {
				// Continuing after 401/412 would write without lease fencing.
				r.reportFatal(ErrLeaseLost)
				return
			}
			// The provider's per-tool timeout eventually settles an undelivered result.
			log.ErrorContext(ctx, "send tool result failed", "error", err)
			return
		}
		log.InfoContext(ctx, "tool result sent", "is_error", res.IsError)
	})
	return nil
}

// execute dispatches one call to a local tool; framework-level errors (including
// unknown tools and ctx cancellation) become IsError results for the model.
func (r *runner) execute(ctx context.Context, name string, input json.RawMessage) tools.Result {
	if err := ctx.Err(); err != nil {
		return errorResult(err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, r.toolTimeout)
	defer cancel()
	res, err := r.executor.Execute(ctx, tools.Call{Name: name, Input: input, Workdir: r.workdir, SessionID: r.sessionID})
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errorResult(fmt.Sprintf("tool %q timed out after %s", name, r.toolTimeout))
		}
		return errorResult(err.Error())
	}
	return res
}

func errorResult(msg string) tools.Result {
	return tools.Result{
		IsError: true,
		Content: []tools.ContentBlock{{Type: tools.ContentBlockTypeError, Text: msg}},
	}
}

// sendToolResult writes one user.tool_result per execution. Each HTTP request is
// one logical write with a definite fencing outcome. A non-empty threadID routes
// the result to that child thread. sendWindow bounds exponential-backoff retries;
// 4xx responses are deterministic failures and are not retried.
func (r *runner) sendToolResult(ctx context.Context, threadID, toolUseID string, res tools.Result) error {
	input := &kha.UserToolResultInput{
		Type: kha.UserToolResultInputTypeUserToolResult,
		Data: kha.UserToolResultPayload{
			ToolUseID: toolUseID,
			Content:   contentBlocks(res),
			IsError:   res.IsError,
		},
	}
	if threadID != "" {
		input.Data.SessionThreadID = new(threadID)
	}
	body := &kha.SendEventsRequest{Events: []*kha.UserEventInput{kha.NewUserEventInputUserToolResultInput(input)}}
	err := retryWithWindow(ctx, r.sendWindow, sendBaseDelay, sendMaxDelay, func(ctx context.Context) error {
		_, err := r.client.Sessions.CreateEvents(ctx, kha.CreateSessionEventsRequest{ID: r.sessionID, Body: body})
		return err
	})
	if _, ok := errors.AsType[*windowExhaustedError](err); ok {
		return fmt.Errorf("send %w", err)
	}
	return err
}

func contentBlocks(res tools.Result) []*kha.ContentBlock {
	out := make([]*kha.ContentBlock, 0, len(res.Content))
	for _, b := range res.Content {
		switch b.Type {
		case tools.ContentBlockTypeText, tools.ContentBlockTypeError:
			out = append(out, textBlock(b.Text))
		default:
			out = append(out, textBlock(fmt.Sprintf("[unsupported content block: %s]", b.Type)))
		}
	}
	if len(out) == 0 {
		out = append(out, textBlock("(no output)"))
	}
	return out
}

func textBlock(text string) *kha.ContentBlock {
	return kha.NewContentBlockTextContentBlock(&kha.TextContentBlock{
		Type: kha.TextContentBlockTypeText,
		Text: text,
	})
}
