package kha

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// CustomToolHandler executes one custom tool call requested by the Agent.
// The returned content is sent back as the call's result. A non-nil error is
// reported to the Agent as an error result carrying the error text, and the
// runner keeps serving later calls.
type CustomToolHandler func(ctx context.Context, call *AgentCustomToolUseEvent) ([]*ToolResultContent, error)

// ToolConfirmation is the decision for one tool call waiting for approval.
type ToolConfirmation struct {
	// Allow permits the call; false denies it.
	Allow bool
	// DenyMessage optionally explains a denial to the Agent. It is ignored
	// when Allow is true.
	DenyMessage string
}

// ToolConfirmationHandler decides one tool call that the Agent's permission
// policy holds for approval (always_ask). Returning an error stops the runner
// because the call cannot be answered.
type ToolConfirmationHandler func(ctx context.Context, call *AgentToolUseEvent) (ToolConfirmation, error)

// ToolRunnerOption configures SessionsClient.RunTools.
type ToolRunnerOption func(*toolRunnerConfig)

type toolRunnerConfig struct {
	handlers map[string]CustomToolHandler
	confirm  ToolConfirmationHandler
	events   []EventsOption
}

// WithCustomTool registers the handler for custom tool calls named name.
// Registering the same name again replaces the earlier handler.
func WithCustomTool(name string, handler CustomToolHandler) ToolRunnerOption {
	return func(c *toolRunnerConfig) { c.handlers[name] = handler }
}

// WithToolConfirmation registers the handler that answers tool calls waiting
// for approval. Without it the runner leaves those calls for another client.
func WithToolConfirmation(handler ToolConfirmationHandler) ToolRunnerOption {
	return func(c *toolRunnerConfig) { c.confirm = handler }
}

// WithToolRunnerEvents passes stream options, such as WithOnRecovery, to the
// event stream the runner reads. The runner chooses the starting cursor itself,
// so WithCursor is overridden.
func WithToolRunnerEvents(opts ...EventsOption) ToolRunnerOption {
	return func(c *toolRunnerConfig) { c.events = append(c.events, opts...) }
}

// RunTools answers the Session's custom tool calls and, when configured, its
// tool confirmations until ctx is done or the event stream fails.
//
// It first pages through the Session history to find calls that have no
// answer yet and answers them, then follows the live stream from the end of
// that history using the resilient SessionsClient.Events state machine.
// Each agent.custom_tool_use event runs the handler registered for its tool
// name; a call without a handler is answered with an error result so the
// Agent does not wait forever. When a session.status event stops with
// requires_action, every listed agent.tool_use the runner has seen and not yet
// answered is passed to the WithToolConfirmation handler.
//
// Calls are deduplicated by event ID, including calls already answered in
// history, so a reconnect or backfill never runs the same call twice. Each
// answer carries an Idempotency-Key derived from the call's event ID, which
// lets kha.New retry a lost write without answering twice. Handlers run one at
// a time on the caller's goroutine.
//
// RunTools returns ctx's error once ctx is done, or the first stream, handler
// confirmation, or write error.
func (c *SessionsClient) RunTools(ctx context.Context, sessionID string, opts ...ToolRunnerOption) error {
	if sessionID == "" {
		return &InvalidCallError{Reason: "session id must not be empty"}
	}
	cfg := toolRunnerConfig{handlers: map[string]CustomToolHandler{}}
	for _, opt := range opts {
		opt(&cfg)
	}
	r := &toolRunner{
		sessions:  c,
		sessionID: sessionID,
		cfg:       cfg,
		answered:  map[string]bool{},
		toolUses:  map[string]*AgentToolUseEvent{},
	}
	tip, err := r.catchUp(ctx)
	if err != nil {
		return err
	}
	events := append([]EventsOption{}, cfg.events...)
	if tip != nil {
		events = append(events, WithCursor(*tip))
	}
	for ev, err := range c.Events(ctx, sessionID, events...) {
		if err != nil {
			return err
		}
		if err := r.observe(ctx, ev, true); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New("kha: event stream ended")
}

type toolRunner struct {
	sessions  *SessionsClient
	sessionID string
	cfg       toolRunnerConfig
	// answered holds call event IDs that already have a result or decision.
	answered map[string]bool
	// toolUses keeps agent.tool_use events until their confirmation, since
	// requires_action lists only their IDs.
	toolUses map[string]*AgentToolUseEvent
}

// catchUp records every answer in history before acting on any call, because
// a call and its answer can sit on different pages. It returns the stream
// cursor at the end of history.
func (r *toolRunner) catchUp(ctx context.Context) (*string, error) {
	var history []*SessionEvent
	var tip, token *string
	seen := map[string]bool{}
	for {
		resp, err := r.sessions.ListEvents(ctx, ListSessionEventsRequest{ID: r.sessionID, PageToken: token})
		if err != nil {
			return nil, err
		}
		page := resp.Body
		history = append(history, page.Items...)
		if page.StreamCursor != nil && *page.StreamCursor != "" {
			tip = page.StreamCursor
		}
		if page.NextPageToken == nil || *page.NextPageToken == "" {
			break
		}
		if seen[*page.NextPageToken] {
			return nil, fmt.Errorf("kha: event history repeated page token %q", *page.NextPageToken)
		}
		seen[*page.NextPageToken] = true
		token = page.NextPageToken
	}
	for _, ev := range history {
		r.recordAnswer(ev)
	}
	for _, ev := range history {
		if err := r.observe(ctx, ev, false); err != nil {
			return nil, err
		}
	}
	// A requires_action status in history may predate tool uses that are
	// still pending, so confirmations are decided once history is complete.
	if pending := r.pendingConfirmations(history); len(pending) > 0 {
		if err := r.confirmAll(ctx, pending); err != nil {
			return nil, err
		}
	}
	return tip, nil
}

func (r *toolRunner) recordAnswer(ev *SessionEvent) {
	switch {
	case ev.UserCustomToolResultEvent != nil:
		r.answered[ev.UserCustomToolResultEvent.Data.EventID] = true
	case ev.UserToolConfirmationEvent != nil:
		r.answered[ev.UserToolConfirmationEvent.Data.EventID] = true
		delete(r.toolUses, ev.UserToolConfirmationEvent.Data.EventID)
	}
}

// observe handles one event. live is false while replaying history, where
// requires_action is resolved after the whole history has been read.
func (r *toolRunner) observe(ctx context.Context, ev *SessionEvent, live bool) error {
	r.recordAnswer(ev)
	switch {
	case ev.AgentCustomToolUseEvent != nil:
		return r.runCustomTool(ctx, ev.AgentCustomToolUseEvent)
	case ev.AgentToolUseEvent != nil:
		if r.cfg.confirm != nil && !r.answered[ev.AgentToolUseEvent.ID] {
			r.toolUses[ev.AgentToolUseEvent.ID] = ev.AgentToolUseEvent
		}
	case live && ev.SessionStatusEvent != nil:
		if ids := requiresAction(ev.SessionStatusEvent); len(ids) > 0 {
			return r.confirmAll(ctx, ids)
		}
	}
	return nil
}

func (r *toolRunner) runCustomTool(ctx context.Context, call *AgentCustomToolUseEvent) error {
	if call.ID == "" || r.answered[call.ID] {
		return nil
	}
	var content []*ToolResultContent
	var err error
	if handler := r.cfg.handlers[call.Data.Name]; handler != nil {
		content, err = handler(ctx, call)
	} else {
		err = fmt.Errorf("no handler is registered for custom tool %q", call.Data.Name)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	payload := UserCustomToolResultPayload{EventID: call.ID, Content: content}
	if err != nil {
		payload.IsError = new(true)
		payload.Content = []*ToolResultContent{NewToolResultContentTextContentBlock(&TextContentBlock{
			Type: TextContentBlockTypeText,
			Text: err.Error(),
		})}
	}
	input := NewUserEventInputUserCustomToolResultInput(&UserCustomToolResultInput{
		Type: UserCustomToolResultInputTypeUserCustomToolResult,
		Data: payload,
	})
	if err := r.send(ctx, "custom-tool-result", call.ID, input); err != nil {
		return err
	}
	r.answered[call.ID] = true
	return nil
}

func (r *toolRunner) confirmAll(ctx context.Context, ids []string) error {
	if r.cfg.confirm == nil {
		return nil
	}
	for _, id := range ids {
		call := r.toolUses[id]
		if call == nil || r.answered[id] {
			continue
		}
		decision, err := r.cfg.confirm(ctx, call)
		if err != nil {
			return err
		}
		payload := UserToolConfirmationPayload{EventID: id, Result: UserToolConfirmationResultAllow}
		if !decision.Allow {
			payload.Result = UserToolConfirmationResultDeny
			if decision.DenyMessage != "" {
				payload.DenyMessage = new(decision.DenyMessage)
			}
		}
		input := NewUserEventInputUserToolConfirmationInput(&UserToolConfirmationInput{
			Type: UserToolConfirmationInputTypeUserToolConfirmation,
			Data: payload,
		})
		if err := r.send(ctx, "tool-confirmation", id, input); err != nil {
			return err
		}
		r.answered[id] = true
		delete(r.toolUses, id)
	}
	return nil
}

// pendingConfirmations returns the IDs listed by the latest requires_action
// status in history.
func (r *toolRunner) pendingConfirmations(history []*SessionEvent) []string {
	for _, ev := range slices.Backward(history) {
		if st := ev.SessionStatusEvent; st != nil {
			return requiresAction(st)
		}
	}
	return nil
}

func requiresAction(st *SessionStatusEvent) []string {
	if sr := st.Data.StopReason; st.Data.Status == SessionStateIdle && sr != nil && sr.Type == StopReasonRequiresAction {
		return sr.EventIds
	}
	return nil
}

// send writes one answer with a key derived from the answered event ID, so a
// retried write is replayed by the server instead of answering twice.
func (r *toolRunner) send(ctx context.Context, kind, eventID string, input *UserEventInput) error {
	_, err := r.sessions.CreateEvents(ctx, CreateSessionEventsRequest{
		ID:             r.sessionID,
		IdempotencyKey: new("kha-" + kind + "-" + eventID),
		Body:           &SendEventsRequest{Events: []*UserEventInput{input}},
	})
	return err
}
