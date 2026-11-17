package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"iter"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	kha "github.com/MoonshotAI/kha"
	"github.com/MoonshotAI/kha/internal/cli/output"
	"github.com/MoonshotAI/kha/internal/cli/render"
)

var sessionHeaders = []string{"ID", "AGENT", "STATUS", "TITLE", "CREATED"}

func sessionRow(s *kha.Session) []string {
	return []string{s.ID, s.AgentID, string(s.Status), valueOr(s.Title, "-"), output.Time(s.CreatedAt)}
}

func newSessionsCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "Start and manage Sessions and their event streams",
	}
	cmd.AddCommand(
		newSessionsListCmd(g),
		newSessionsGetCmd(g),
		newSessionsCreateCmd(g),
		newSessionsUpdateCmd(g),
		newSessionsEditCmd(g),
		newSessionsArchiveCmd(g),
		newSessionsDeleteCmd(g),
		newSessionsSendCmd(g),
		newSessionsInterruptCmd(g),
		newSessionsAttachCmd(g),
		newSessionsEventsCmd(g),
		newSessionsResourcesCmd(g),
		newSessionsThreadsCmd(g),
		newSessionsFsCmd(g),
		newSessionsPendingCmd(g),
	)
	return cmd
}

func newSessionsListCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Sessions (most recently updated first)",
		Args:  cobra.NoArgs,
	}
	lf := addListFlags(cmd)
	var agentID, agentVersion, order, status, since string
	var includeArchived bool
	cmd.Flags().StringVar(&agentID, "agent", "", "restrict to Sessions running this Agent id")
	cmd.Flags().StringVar(&agentVersion, "agent-version", "", "restrict to Sessions pinned to this Agent version (a numeric string such as \"2\")")
	cmd.Flags().StringVar(&order, "order", "", "listing order: created_at, created_at_desc, updated_at, updated_at_desc (default updated_at_desc)")
	cmd.Flags().StringVar(&since, "since", "", "only Sessions updated within a relative duration (24h, 7d, 2w) or after a date (2026-11-01)")
	cmd.Flags().StringVarP(&status, "status", "s", "", "restrict to these lifecycle statuses, comma-separated (idle, running, terminated)")
	cmd.Flags().BoolVar(&includeArchived, "include-archived", false, "include archived Sessions")
	mustRegisterFlagCompletion(cmd, "status", completeFlagStaticCSV("idle", "running", "terminated"))
	mustRegisterFlagCompletion(cmd, "order", completeFlagStatic("created_at", "created_at_desc", "updated_at", "updated_at_desc"))
	mustRegisterFlagCompletion(cmd, "agent", agentIDCompletion)
	mustRegisterFlagCompletion(cmd, "agent-version", agentVersionFlagCompletion)
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if err := lf.validate(); err != nil {
			return err
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		params := kha.ListSessionsRequest{PageSize: lf.pageSize(), PageToken: lf.token()}
		if agentID != "" {
			params.AgentID = new(agentID)
		}
		if agentVersion != "" {
			params.AgentVersion = new(agentVersion)
		}
		if order != "" {
			params.Order = new(kha.SessionOrder(order))
		}
		for s := range strings.SplitSeq(status, ",") {
			if s = strings.TrimSpace(s); s != "" {
				params.Statuses = append(params.Statuses, kha.SessionState(s))
			}
		}
		if includeArchived {
			params.IncludeArchived = new(true)
		}
		if since != "" {
			t, err := parseSince(since)
			if err != nil {
				return err
			}
			params.UpdatedAtGte = new(t)
		}
		if lf.all {
			return printAll(cmd, g, sessionHeaders, client.Sessions.All(cmd.Context(), params), sessionRow)
		}
		itemsResult, err := client.Sessions.List(cmd.Context(), params)
		if err != nil {
			return err
		}
		page := itemsResult.Body
		page.Items = nonNilItems(page.Items)
		page.NextPageToken = nonEmptyPageToken(page.NextPageToken)

		rows := make([][]string, 0, len(page.Items))
		for _, item := range page.Items {
			rows = append(rows, sessionRow(item))
		}
		return printList(cmd, g, sessionHeaders, rows, page)
	})
	return cmd
}

func newSessionsGetCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "get <id>",
		Short:             "Get one Session with its resource bindings",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: sessionIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			sessResult, err := client.Sessions.Get(cmd.Context(), kha.GetSessionRequest{ID: args[0]})
			if err != nil {
				return err
			}
			sess := sessResult.Body

			return printOne(cmd, g, sessionHeaders, sessionRow(sess), sess)
		}),
	}
}

func newSessionsCreateCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a Session running an Agent",
		Args:  cobra.NoArgs,
		Example: `  kha sessions create --agent agent_xxx --environment env_xxx --title "weekly report"
  kha sessions create --agent agent_xxx --environment env_xxx --resource '{type: vault, vault_id: vlt_xxx}' --metadata ticket=PROJ-42`,
	}
	var agentID, agentVersion, title, environment string
	cmd.Flags().StringVar(&agentID, "agent", "", "Agent id to run")
	cmd.Flags().StringVar(&agentVersion, "agent-version", "", "Agent version to pin (default: latest)")
	cmd.Flags().StringVar(&title, "title", "", "human-readable title")
	cmd.Flags().StringVar(&environment, "environment", "", "Environment for the Session (required)")
	mustRegisterFlagCompletion(cmd, "agent", agentIDCompletion)
	mustRegisterFlagCompletion(cmd, "agent-version", agentVersionFlagCompletion)
	mustRegisterFlagCompletion(cmd, "environment", environmentIDCompletion)
	var resources []*kha.SessionResource
	addYAMLFlags(cmd, "one resource binding as a JSON or YAML object (repeatable)", struct {
		name   string
		target any
	}{"resource", &resources})
	bindings := addResourceBindingFlags(cmd)
	var overrides kha.SessionAgentOverrides
	cmd.Flags().Var(&yamlValue{flag: "override", target: &overrides}, "override", "session-local agent overrides as a JSON or YAML object, e.g. '{model: kimi-k3.1-pro, system: \"...\"}'")
	metadata := addKVFlag(cmd, "metadata", "key=value metadata entry (repeatable; up to 16)")
	body := addBodyFlag(cmd, "agent", "agent-version", "title", "environment", "resource", "override", "metadata", "vault", "memory-store", "file-id", "repo", "repo-token", "repo-token-stdin")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		var req kha.CreateSessionRequest
		if cmd.Flags().Changed("file") {
			if err := body.decode(cmd, &req); err != nil {
				return err
			}
		} else {
			if agentID == "" || environment == "" {
				return errUsage("sessions create requires --agent and --environment (or --file)")
			}
			agentID, pinnedVersion := splitRef(agentID)
			if pinnedVersion != nil && agentVersion != "" {
				return errUsage("--agent id@version and --agent-version cannot be combined")
			}
			req = kha.CreateSessionRequest{AgentID: agentID, EnvironmentID: environment}
			if pinnedVersion != nil {
				req.AgentVersion = pinnedVersion
			} else if agentVersion != "" {
				req.AgentVersion = new(agentVersion)
			}
			if title != "" {
				req.Title = new(title)
			}
			shorthand, err := bindings.resolve(cmd)
			if err != nil {
				return err
			}
			req.Resources = append(shorthand, resources...)
			if cmd.Flags().Changed("override") {
				req.AgentOverrides = &overrides
			}
			req.Metadata = metadata.mapValue()
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		sessResult, err := client.Sessions.Create(cmd.Context(), kha.CreateSessionHTTPRequest{Body: &req})
		if err != nil {
			return err
		}
		sess := sessResult.Body

		note(cmd, "session %s created (status %s) — send the first message with `kha sessions send %s <text>`", sess.ID, sess.Status, sess.ID)
		return printOne(cmd, g, sessionHeaders, sessionRow(sess), sess)
	})
	return cmd
}

func newSessionsUpdateCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "update <id>",
		Short:             "Update a Session's title or agent overrides",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: sessionIDCompletion,
		Example: `  kha sessions update sess_xxx --title "renamed"
  kha sessions update sess_xxx --override '{model: kimi-k3.1-pro}'`,
	}
	var title string
	cmd.Flags().StringVar(&title, "title", "", "new title")
	var overrides kha.SessionAgentOverrides
	cmd.Flags().Var(&yamlValue{flag: "override", target: &overrides}, "override", "replacement session-local agent overrides as a JSON or YAML object (each present collection replaces that override wholesale)")
	body := addBodyFlag(cmd, "title", "override")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		var req kha.UpdateSessionRequest
		if cmd.Flags().Changed("file") {
			if err := body.decode(cmd, &req); err != nil {
				return err
			}
		} else {
			if !cmd.Flags().Changed("title") && !cmd.Flags().Changed("override") {
				return errUsage("nothing to update: pass --title, --override, or --file")
			}
			if cmd.Flags().Changed("title") {
				req.Title = new(title)
			}
			if cmd.Flags().Changed("override") {
				req.AgentOverrides = &overrides
			}
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		sessResult, err := client.Sessions.Update(cmd.Context(), kha.UpdateSessionHTTPRequest{ID: args[0], Body: &req})
		if err != nil {
			return err
		}
		sess := sessResult.Body

		note(cmd, "session %s updated", sess.ID)
		return printOne(cmd, g, sessionHeaders, sessionRow(sess), sess)
	})
	return cmd
}

func newSessionsArchiveCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "archive <id>",
		Short:             "Archive a Session, terminating it (interrupt a running Session first)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: sessionIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			if _, err := client.Sessions.Archive(cmd.Context(), kha.ArchiveSessionRequest{ID: args[0]}); err != nil {
				return err
			}
			note(cmd, "session %s archived", args[0])
			return nil
		}),
	}
}

func newSessionsDeleteCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "delete <id>",
		Short:             "Permanently delete an archived Session",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: sessionIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			if _, err := client.Sessions.Delete(cmd.Context(), kha.DeleteSessionRequest{ID: args[0]}); err != nil {
				return err
			}
			note(cmd, "session %s deleted", args[0])
			return nil
		}),
	}
}

// newSessionsSendCmd sends one user message. With --wait it blocks until the
// turn ends and reports the TurnResult.
func newSessionsSendCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "send <id> [text]",
		Short: "Send a message to a Session; --wait blocks until the turn ends",
		Args: cobra.MatchAll(cobra.RangeArgs(1, 2), func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("text-file") && len(args) == 2 {
				return errUsage("text and --text-file cannot be combined")
			}
			if cmd.Flags().Changed("event") {
				if len(args) == 2 || cmd.Flags().Changed("text-file") {
					return errUsage("--event cannot combine with positional text or --text-file")
				}
				return nil
			}
			if !cmd.Flags().Changed("text-file") && len(args) != 2 {
				return errUsage("provide text, --text-file (use - for stdin), or --event")
			}
			return nil
		}),
		ValidArgsFunction: sessionIDCompletion,
		Example: `  kha sessions send sess_xxx "summarize the attached CSV"
  kha sessions send sess_xxx --text-file brief.md --wait
  kha sessions send sess_xxx --event '{type: user.message, data: {content: [{type: text, text: hi}, {type: file, file_id: file_xxx}]}}' --wait
  cat brief.md | kha sessions send sess_xxx --text-file - --wait
  kha sessions send sess_xxx "draft the report" --wait --timeout 20m`,
	}
	var wait bool
	var timeout time.Duration
	var textFile string
	cmd.Flags().StringVar(&textFile, "text-file", "", `read the message from a file ("-" reads stdin); cannot combine with positional text`)
	var event kha.QueuedUserEventInput
	cmd.Flags().Var(&yamlValue{flag: "event", target: &event}, "event", "one user event as a JSON or YAML object ({type: user.message, data: {content: [...]}}), for rich content blocks such as file references; cannot combine with text")
	cmd.Flags().BoolVarP(&wait, "wait", "w", false, "wait for the turn to end and print the result")
	cmd.Flags().DurationVarP(&timeout, "timeout", "t", 10*time.Minute, "maximum time --wait blocks for")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		var input *kha.UserEventInput
		var text string
		if cmd.Flags().Changed("event") {
			input = kha.NewUserEventInputUserMessageInput((*kha.UserMessageInput)(&event))
		} else {
			if cmd.Flags().Changed("text-file") {
				data, err := readInputFile(cmd, "text-file", textFile)
				if err != nil {
					return err
				}
				text = string(data)
			} else {
				text = args[1]
			}
			if strings.TrimSpace(text) == "" {
				return errUsage("message text must not be empty or whitespace; provide a message such as Continue. to continue the Session")
			}
			input = userMessageInput(text)
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		if !wait {
			if _, err := client.Sessions.CreateEvents(cmd.Context(), kha.CreateSessionEventsRequest{ID: args[0], Body: &kha.SendEventsRequest{Events: []*kha.UserEventInput{input}}}); err != nil {
				return err
			}
			note(cmd, "message accepted by session %s — follow it with `kha sessions attach %s`", args[0], args[0])
			return nil
		}
		if cmd.Flags().Changed("event") {
			res, err := client.Sessions.SendEventAndWait(cmd.Context(), args[0], input, kha.WithTimeout(timeout))
			if err != nil {
				if sessErr, ok := errors.AsType[*kha.SessionError](err); ok {
					return fmt.Errorf("turn failed: %s: %s", sessErr.Type, sessErr.Message)
				}
				return err
			}
			note(cmd, "message accepted; turn ended")
			lastEvent := "-"
			if st := res.LastEvent.AsSessionStatusEvent(); st != nil {
				lastEvent = string(st.ID)
			}
			return printOne(cmd, g,
				[]string{"STOP REASON", "LAST EVENT"},
				[]string{output.Dash(string(res.StopReason)), lastEvent},
				res)
		}
		res, err := client.Sessions.SendMessageAndWait(cmd.Context(), args[0], text, kha.WithTimeout(timeout))
		if err != nil {
			if sessErr, ok := errors.AsType[*kha.SessionError](err); ok {
				return fmt.Errorf("turn failed: %s: %s", sessErr.Type, sessErr.Message)
			}
			return err
		}
		// Claim acceptance only after the send actually succeeded: a rejected
		// send (archived session, 409) must not report "message accepted".
		note(cmd, "message accepted; turn ended")
		lastEvent := "-"
		if st := res.LastEvent.AsSessionStatusEvent(); st != nil {
			lastEvent = string(st.ID)
		}
		return printOne(cmd, g,
			[]string{"STOP REASON", "LAST EVENT"},
			[]string{output.Dash(string(res.StopReason)), lastEvent},
			res)
	})
	return cmd
}

// userMessageInput packs one text message into the user.message event input,
// mirroring Session.SendMessageAndWait's construction.
func userMessageInput(text string) *kha.UserEventInput {
	payload := kha.UserMessagePayload{}
	if text != "" {
		payload.Content = []*kha.ContentBlock{
			kha.NewContentBlockTextContentBlock(&kha.TextContentBlock{
				Type: kha.TextContentBlockTypeText,
				Text: text,
			}),
		}
	}
	return kha.NewUserEventInputUserMessageInput(&kha.UserMessageInput{
		Type: kha.UserMessageInputTypeUserMessage,
		Data: payload,
	})
}

// newSessionsInterruptCmd sends a user.interrupt input: the running turn
// stops and the Session returns to idle with stop reason canceled. It is the
// only way to stop an in-flight turn from the CLI — Ctrl-C on attach only
// detaches the local stream, and pending retract only drops queued inputs.
func newSessionsInterruptCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "interrupt <id>",
		Short: "Interrupt the Session's running turn",
		Long: `Interrupt the Session's running turn: the turn stops and the Session
returns to idle with stop reason canceled. Interrupting a Session that is
not running fails with 400 failed_precondition_error.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: sessionIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			input := kha.NewUserEventInputUserInterruptInput(&kha.UserInterruptInput{
				Type: kha.UserInterruptInputTypeUserInterrupt,
			})
			if _, err := client.Sessions.CreateEvents(cmd.Context(), kha.CreateSessionEventsRequest{ID: args[0], Body: &kha.SendEventsRequest{Events: []*kha.UserEventInput{input}}}); err != nil {
				return err
			}
			note(cmd, "session %s interrupted", args[0])
			return nil
		}),
	}
}

// newSessionsAttachCmd renders the live event stream as plain text. Ctrl-C
// exits gracefully with code 130. --thread attaches
// to one Thread's own stream instead of the coordinator's.
func newSessionsAttachCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attach <id>",
		Short: "Stream a Session's events as live plain text",
		Long: `Stream a Session's events as live plain text (no TUI): agent messages
print directly, agent.delta fragments append in place, thinking collapses to
a dim summary, tool calls become one-line traces. Status transitions and
errors go to stderr. Colors follow the terminal (NO_COLOR disables them).

The stream reconnects, dedupes, and backfills automatically (SDK semantics).
Ctrl-C stops it gracefully and reports how many events were received.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: sessionIDCompletion,
	}
	var since, threadID string
	var input bool
	cmd.Flags().StringVar(&since, "since", "", `resume after this cursor ("now" tails new events only)`)
	cmd.Flags().StringVar(&threadID, "thread", "", "attach to this Thread's own event stream instead of the coordinator's")
	cmd.Flags().BoolVar(&input, "input", false, "also send each nonempty stdin line as a user.message; detach after stdin closes and the last message's turn ends")
	mustRegisterFlagCompletion(cmd, "since", completeFlagStatic("now"))
	mustRegisterFlagCompletion(cmd, "thread", sessionThreadFlagCompletion)
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if input && threadID != "" {
			return errUsage("--input sends to the coordinator; it cannot combine with --thread")
		}
		ctx := cmd.Context()
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		color := false
		if f, ok := cmd.OutOrStdout().(*os.File); ok {
			color = render.UseColor(f)
		}
		r := render.New(cmd.OutOrStdout(), cmd.ErrOrStderr(), color)
		// On an expired-cursor recovery the SDK calls r.Recover, which
		// resets the streamed-delta memory so the authoritative
		// agent.message reprints in full (spec: partial previews are
		// discarded on re-anchor). Deltas stay opted in: attach renders
		// the live typewriter preview the API no longer sends by default.
		opts := []kha.EventsOption{kha.WithOnRecovery(r.Recover), kha.WithDeltas(true)}
		if since != "" {
			opts = append(opts, kha.WithCursor(since))
		}
		streamCtx, stopStream := context.WithCancel(ctx)
		defer stopStream()
		var events iter.Seq2[*kha.SessionEvent, error]
		if threadID != "" {
			events = client.Sessions.ThreadEvents(streamCtx, args[0], threadID, opts...)
		} else {
			events = client.Sessions.Events(streamCtx, args[0], opts...)
		}
		n := 0
		var streamErr error
		watch := newAttachInputWatch(cmd, client, args[0], input)
		defer watch.close()
		// A failed stdin send must tear the stream down: the resume state
		// machine would otherwise reconnect and wait forever. A clean pump
		// end tears it down once the detach condition already holds, which
		// covers streams whose frames were all consumed before stdin closed.
		watch.onPumpEnd = func(failed bool) {
			if failed || watch.detachNow() {
				stopStream()
			}
		}
		events = watch.wrap(events)
		for ev, err := range events {
			if err != nil {
				if ctx.Err() == nil {
					streamErr = err
				}
				break
			}
			n++
			r.Event(ev)
		}
		r.Close()
		if err := watch.failure(); err != nil {
			return err
		}
		if streamErr != nil {
			note(cmd, "stream failed after %d event(s)", n)
			return streamErr
		}
		if ctx.Err() != nil {
			note(cmd, "interrupted: %d event(s) received", n)
			return errInterrupted
		}
		note(cmd, "stream ended: %d event(s) received", n)
		return nil
	})
	return cmd
}

// attachInputWatch bridges stdin into a live attach: every nonempty line
// becomes one user.message on the coordinator. The watch ends attach once
// stdin has closed and the last accepted message's turn has ended — the
// Session returns to idle after that message's echo, the same echo-arm rule
// SendEventAndWait applies. A send failure (archived Session, 409) ends
// attach immediately instead of leaving the stream attached to a Session
// that rejects input.
type attachInputWatch struct {
	cmd       *cobra.Command
	client    *kha.Client
	session   string
	enabled   bool
	done      chan struct{} // closed when the pump stops (stdin EOF or failure)
	onPumpEnd func(failed bool)
	mu        sync.Mutex
	idleSeen  bool // an idle status arrived after stdin closed
	pumpDone  bool // stdin closed or failed; set before onPumpEnd fires
	sendErr   error
}

func newAttachInputWatch(cmd *cobra.Command, client *kha.Client, sessionID string, enabled bool) *attachInputWatch {
	w := &attachInputWatch{cmd: cmd, client: client, session: sessionID, enabled: enabled, done: make(chan struct{})}
	if enabled {
		go w.pump()
	}
	return w
}

// pump reads stdin line by line, sends each nonempty line, and stops at EOF
// or on the first failure.
func (w *attachInputWatch) pump() {
	defer close(w.done)
	scanner := bufio.NewScanner(w.cmd.InOrStdin())
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		resp, err := w.client.Sessions.CreateEvents(w.cmd.Context(), kha.CreateSessionEventsRequest{
			ID:   w.session,
			Body: &kha.SendEventsRequest{Events: []*kha.UserEventInput{userMessageInput(line)}},
		})
		w.mu.Lock()
		if err != nil {
			w.sendErr = fmt.Errorf("sending stdin message: %w", err)
			w.mu.Unlock()
			if w.onPumpEnd != nil {
				w.onPumpEnd(true)
			}
			return
		}
		_ = resp
		w.mu.Unlock()
		note(w.cmd, "sent %d byte(s) to session %s", len(line), w.session)
	}
	if err := scanner.Err(); err != nil {
		w.mu.Lock()
		w.sendErr = fmt.Errorf("reading stdin: %w", err)
		w.mu.Unlock()
	}
	w.mu.Lock()
	w.pumpDone = true
	w.mu.Unlock()
	if w.onPumpEnd != nil {
		w.onPumpEnd(w.sendErr != nil)
	}
}

// wrap filters the event stream: while the pump is still reading stdin the
// stream passes through untouched; once the pump has stopped, the stream
// ends at the first idle status that follows the last accepted message's
// echo (or at the next idle when the deployment returns no echo ids).
func (w *attachInputWatch) wrap(events kha.SessionEventsIterator) kha.SessionEventsIterator {
	if !w.enabled {
		return events
	}
	return func(yield func(*kha.SessionEvent, error) bool) {
		for ev, err := range events {
			if err == nil {
				if st := ev.SessionStatusEvent; ev.Type == kha.SessionEventTypeSessionStatusEvent && st != nil {
					w.mu.Lock()
					// The latest status decides: SSE ordering puts the last
					// turn's terminal state last, so once stdin has closed
					// an idle here means every accepted message's turn is
					// over. A running status simply clears it.
					w.idleSeen = st.Data.Status == kha.SessionStateIdle
					w.mu.Unlock()
				}
			}
			if !yield(ev, err) {
				return
			}
			if err != nil {
				return
			}
			if w.detachNow() {
				return
			}
		}
	}
}

// detachNow reports whether the watch's end condition is met: stdin closed
// and the Session back to idle afterwards. SSE ordering puts the last
// turn's idle last, so any idle after the pump ends settles every accepted
// message's turn.
func (w *attachInputWatch) detachNow() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.pumpDone && w.sendErr == nil && w.idleSeen
}

// failure returns the deferred stdin send failure, if any.
func (w *attachInputWatch) failure() error {
	if !w.enabled {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sendErr
}

// close is a no-op retained for symmetry with newAttachInputWatch.
func (w *attachInputWatch) close() {}
