package commands

import (
	"time"

	"github.com/spf13/cobra"

	kha "github.com/MoonshotAI/kha"
	"github.com/MoonshotAI/kha/internal/cli/output"
)

var triggerHeaders = []string{"ID", "NAME", "STATUS", "AGENT", "LAST RUN"}

func triggerRow(t *kha.Trigger) []string {
	return []string{t.ID, t.Name, string(t.Status), t.AgentID, triggerTimeOpt(t.LastRunAt)}
}

// triggerTimeOpt formats an optional timestamp for a table cell, "-" when absent.
func triggerTimeOpt(t *time.Time) string {
	if t != nil {
		return output.Time(*t)
	}
	return "-"
}

func newTriggersCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "triggers",
		Short: "Manage scheduled and event-driven Triggers and their runs",
	}
	cmd.AddCommand(
		newTriggersListCmd(g),
		newTriggersGetCmd(g),
		newTriggersCreateCmd(g),
		newTriggersUpdateCmd(g),
		newTriggersEditCmd(g),
		newTriggersArchiveCmd(g),
		newTriggersRunCmd(g),
		newTriggersPauseCmd(g),
		newTriggersUnpauseCmd(g),
		newTriggersRunsCmd(g),
	)
	return cmd
}

func newTriggersListCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Triggers (archived ones excluded unless --status archived)",
		Args:  cobra.NoArgs,
	}
	lf := addListFlags(cmd)
	var status, policyType, agentFilter string
	cmd.Flags().StringVarP(&status, "status", "s", "", "filter by lifecycle status: active, paused, archived")
	cmd.Flags().StringVar(&policyType, "policy-type", "", "filter by policy kind: cron")
	cmd.Flags().StringVar(&agentFilter, "agent", "", "filter by owning Agent id")
	mustRegisterFlagCompletion(cmd, "status", completeFlagStatic("active", "paused", "archived"))
	mustRegisterFlagCompletion(cmd, "agent", agentIDCompletion)
	mustRegisterFlagCompletion(cmd, "policy-type", completeFlagStatic("cron"))
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if err := lf.validate(); err != nil {
			return err
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		params := kha.ListTriggersRequest{PageSize: lf.pageSize(), PageToken: lf.token()}
		if status != "" {
			params.Status = new(kha.TriggerStatus(status))
		}
		if policyType != "" {
			params.PolicyType = new(kha.TriggerPolicyType(policyType))
		}
		if agentFilter != "" {
			params.AgentID = new(agentFilter)
		}
		if lf.all {
			return printAll(cmd, g, triggerHeaders, client.Triggers.All(cmd.Context(), params), triggerRow)
		}
		itemsResult, err := client.Triggers.List(cmd.Context(), params)
		if err != nil {
			return err
		}
		page := itemsResult.Body
		page.Items = nonNilItems(page.Items)
		page.NextPageToken = nonEmptyPageToken(page.NextPageToken)

		rows := make([][]string, 0, len(page.Items))
		for _, t := range page.Items {
			rows = append(rows, triggerRow(t))
		}
		return printList(cmd, g, triggerHeaders, rows, page)
	})
	return cmd
}

func newTriggersGetCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "get <id>",
		Short:             "Get one Trigger",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: triggerIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			triggerResult, err := client.Triggers.Get(cmd.Context(), kha.GetTriggerRequest{ID: args[0]})
			if err != nil {
				return err
			}
			trigger := triggerResult.Body

			return printOne(cmd, g, triggerHeaders, triggerRow(trigger), trigger)
		}),
	}
}

// newTriggersCreateCmd covers the common cron-policy case with flags; any
// richer configuration (initial events, notifications, resources) goes
// through --file.
func newTriggersCreateCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a Trigger with a cron policy",
		Args:  cobra.NoArgs,
		Example: `  kha triggers create --name nightly --agent agent_xxx --environment env_xxx --cron "0 2 * * *" --timezone Asia/Shanghai
  kha triggers create --name digest --agent agent_xxx --environment env_xxx --cron "0 9 * * 1" --session-mode reuse_session --initial-event '{type: user.message, data: {content: [{type: text, text: "post the digest"}]}}'`,
	}
	var name, agentID, agentVersion, environment, cron, timezone string
	cmd.Flags().StringVarP(&name, "name", "n", "", "human-readable trigger name")
	cmd.Flags().StringVar(&agentID, "agent", "", "Agent started by each run")
	cmd.Flags().StringVar(&agentVersion, "agent-version", "", "Agent version to pin (default: latest)")
	cmd.Flags().StringVar(&environment, "environment", "", "Environment for the runs' Sessions (required)")
	cmd.Flags().StringVar(&cron, "cron", "", "five-field cron expression, e.g. \"0 2 * * *\"")
	cmd.Flags().StringVar(&timezone, "timezone", "UTC", "IANA timezone for the cron schedule")
	mustRegisterFlagCompletion(cmd, "agent", agentIDCompletion)
	mustRegisterFlagCompletion(cmd, "agent-version", agentVersionFlagCompletion)
	mustRegisterFlagCompletion(cmd, "environment", environmentIDCompletion)
	var sessionMode string
	cmd.Flags().StringVar(&sessionMode, "session-mode", "", "new_session (default) or reuse_session")
	mustRegisterFlagCompletion(cmd, "session-mode", completeFlagStatic("new_session", "reuse_session"))
	var resources []*kha.SessionResource
	addYAMLFlags(cmd, "one resource binding as a JSON or YAML object (repeatable)", struct {
		name   string
		target any
	}{"resource", &resources})
	bindings := addResourceBindingFlags(cmd)
	var initialEvents []*kha.QueuedUserEventInput
	addYAMLFlags(cmd, "one opening user.message as a JSON or YAML object (repeatable)", struct {
		name   string
		target any
	}{"initial-event", &initialEvents})
	var notification kha.TriggerNotification
	cmd.Flags().Var(&yamlValue{flag: "notification", target: &notification}, "notification", "webhook configuration as a JSON or YAML object, e.g. '{url: https://example.invalid/hook, secret_vault: {vault_id: vlt_xxx}}'")
	body := addBodyFlag(cmd, "session-mode", "name", "agent", "agent-version", "environment", "cron", "timezone", "resource", "initial-event", "notification", "vault", "memory-store", "file-id", "repo", "repo-token", "repo-token-stdin")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		var req kha.CreateTriggerRequest
		if cmd.Flags().Changed("file") {
			if err := body.decode(cmd, &req); err != nil {
				return err
			}
		} else {
			if name == "" || agentID == "" || environment == "" || cron == "" {
				return errUsage("triggers create requires --name, --agent, --environment and --cron (or --file)")
			}
			agentID, pinnedVersion := splitRef(agentID)
			if pinnedVersion != nil && agentVersion != "" {
				return errUsage("--agent id@version and --agent-version cannot be combined")
			}
			req = kha.CreateTriggerRequest{Name: name, AgentID: agentID, EnvironmentID: environment, Policy: kha.TriggerPolicy{Type: kha.CronTriggerPolicyTypeCron, Expression: cron, Timezone: timezone}}
			if cmd.Flags().Changed("session-mode") {
				req.SessionMode = new(kha.SessionMode(sessionMode))
			}
			if pinnedVersion != nil {
				req.AgentVersion = pinnedVersion
			} else if agentVersion != "" {
				req.AgentVersion = new(agentVersion)
			}
			shorthand, err := bindings.resolve(cmd)
			if err != nil {
				return err
			}
			req.Resources = append(shorthand, resources...)
			req.InitialEvents = initialEvents
			if cmd.Flags().Changed("notification") {
				req.Notification = &notification
			}
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		triggerResult, err := client.Triggers.Create(cmd.Context(), kha.CreateTriggerHTTPRequest{Body: &req})
		if err != nil {
			return err
		}
		trigger := triggerResult.Body

		note(cmd, "trigger %s created (status %s)", trigger.ID, trigger.Status)
		return printOne(cmd, g, triggerHeaders, triggerRow(trigger), trigger)
	})
	return cmd
}

func newTriggersUpdateCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "update <id>",
		Short:             "Update a Trigger",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: triggerIDCompletion,
	}
	var name, cron, timezone string
	cmd.Flags().StringVarP(&name, "name", "n", "", "new name")
	cmd.Flags().StringVar(&cron, "cron", "", "new five-field cron expression")
	cmd.Flags().StringVar(&timezone, "timezone", "", "new IANA timezone (required with --cron)")
	var sessionMode string
	cmd.Flags().StringVar(&sessionMode, "session-mode", "", "new_session (default) or reuse_session")
	mustRegisterFlagCompletion(cmd, "session-mode", completeFlagStatic("new_session", "reuse_session"))
	var agentID, agentVersion, environment string
	cmd.Flags().StringVar(&agentID, "agent", "", "Agent for subsequent runs")
	cmd.Flags().StringVar(&agentVersion, "agent-version", "", "Agent version; empty uses latest at firing")
	cmd.Flags().StringVar(&environment, "environment", "", "Environment for subsequent runs")
	mustRegisterFlagCompletion(cmd, "agent", agentIDCompletion)
	mustRegisterFlagCompletion(cmd, "agent-version", agentVersionFlagCompletion)
	mustRegisterFlagCompletion(cmd, "environment", environmentIDCompletion)
	var resources []*kha.SessionResource
	var initialEvents []*kha.QueuedUserEventInput
	addYAMLFlags(cmd, "one resource binding as a JSON or YAML object (repeatable)", struct {
		name   string
		target any
	}{"resource", &resources})
	addYAMLFlags(cmd, "one opening user.message as a JSON or YAML object (repeatable)", struct {
		name   string
		target any
	}{"initial-event", &initialEvents})
	var notification kha.UpdateTriggerNotification
	cmd.Flags().Var(&yamlValue{flag: "notification", target: &notification}, "notification", "replacement webhook configuration as a JSON or YAML object; '{}' detaches it")
	body := addBodyFlag(cmd, "session-mode", "agent", "agent-version", "environment", "name", "cron", "timezone", "resource", "initial-event", "notification")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		var req kha.UpdateTriggerRequest
		if cmd.Flags().Changed("file") {
			if err := body.decode(cmd, &req); err != nil {
				return err
			}
		} else {
			changed := false
			if cmd.Flags().Changed("name") {
				req.Name = new(name)
				changed = true
			}
			if cmd.Flags().Changed("cron") || cmd.Flags().Changed("timezone") {
				if !cmd.Flags().Changed("cron") || !cmd.Flags().Changed("timezone") {
					return errUsage("--cron and --timezone must be set together")
				}
				req.Policy = new(kha.TriggerPolicy{
					Type:       kha.CronTriggerPolicyTypeCron,
					Expression: cron,
					Timezone:   timezone,
				})
				changed = true
			}
			if cmd.Flags().Changed("session-mode") {
				req.SessionMode = new(kha.SessionMode(sessionMode))
				changed = true
			}
			if cmd.Flags().Changed("agent") {
				req.AgentID = new(agentID)
				changed = true
			}
			if cmd.Flags().Changed("agent-version") {
				req.AgentVersion = new(agentVersion)
				changed = true
			}
			if cmd.Flags().Changed("environment") {
				req.EnvironmentID = new(environment)
				changed = true
			}
			if len(resources) > 0 {
				req.Resources = resources
				changed = true
			}
			if len(initialEvents) > 0 {
				req.InitialEvents = initialEvents
				changed = true
			}
			if cmd.Flags().Changed("notification") {
				req.Notification = &notification
				changed = true
			}
			if !changed {
				return errUsage("nothing to update: pass at least one field flag or --file")
			}
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		triggerResult, err := client.Triggers.Update(cmd.Context(), kha.UpdateTriggerHTTPRequest{ID: args[0], Body: &req})
		if err != nil {
			return err
		}
		trigger := triggerResult.Body

		note(cmd, "trigger %s updated", trigger.ID)
		return printOne(cmd, g, triggerHeaders, triggerRow(trigger), trigger)
	})
	return cmd
}

func newTriggersArchiveCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "archive <id>",
		Short:             "Archive a Trigger",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: triggerIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			if _, err := client.Triggers.Archive(cmd.Context(), kha.ArchiveTriggerRequest{ID: args[0]}); err != nil {
				return err
			}
			note(cmd, "trigger %s archived", args[0])
			return nil
		}),
	}
}

// printTriggerRun reports a run's final snapshot: the row on stdout, plus a
// stderr note naming the terminal status (and the failure detail, which the
// table row does not carry).
func printTriggerRun(cmd *cobra.Command, g *globalFlags, r *kha.TriggerRun) error {
	note(cmd, "run %s reached %s", r.ID, r.Status)
	if runErr := r.Error; runErr != nil {
		note(cmd, "run error: %s: %s", runErr.Type, runErr.Message)
	}
	return printOne(cmd, g, triggerRunHeaders, triggerRunRow(r), r)
}

// waitTriggerRun waits for a run with a stderr spinner. Every
// terminal status (completed, failed, skipped) is a successful wait;
// the outcome is data on the returned model.
func waitTriggerRun(cmd *cobra.Command, client *kha.Client, triggerID, runID string, wf *waitFlags) (*kha.TriggerRun, error) {
	stop := waitProgress(cmd, "waiting for trigger run %s", runID)
	done, err := client.Triggers.WaitForTerminal(cmd.Context(), triggerID, runID, wf.options()...)
	stop()
	return done.Body, err
}

func newTriggersRunCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "run <id>",
		Short:             "Fire a Trigger immediately; --wait blocks until the run ends",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: triggerIDCompletion,
		Example: `  kha triggers run trig_xxx --payload-file task.md --wait
  cat task.md | kha triggers run trig_xxx --payload-file - --wait`,
	}
	var wait bool
	payload := addTextFlags(cmd, "payload", "free-form text delivered to the run's Session as a user message")
	cmd.Flags().BoolVarP(&wait, "wait", "w", false, "wait for the run to reach a terminal status and print the outcome")
	wf := addWaitFlags(cmd)
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		text, err := payload.resolve(cmd)
		if err != nil {
			return err
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		var body *kha.RunTriggerRequest
		if text != nil {
			body = &kha.RunTriggerRequest{Payload: text}
		}
		response, err := client.Triggers.Run(cmd.Context(), kha.RunTriggerHTTPRequest{ID: args[0], Body: body})
		if err != nil {
			return err
		}
		note(cmd, "trigger %s fired (run %s)", args[0], response.Body.ID)
		return finishTriggerRun(cmd, g, client, args[0], response.Body, wait, wf)
	})
	return cmd
}

// finishTriggerRun prints a just-fired run, first waiting for a terminal
// status when wait is set.
func finishTriggerRun(cmd *cobra.Command, g *globalFlags, client *kha.Client, triggerID string, triggerRun *kha.TriggerRun, wait bool, wf *waitFlags) error {
	if !wait {
		return printOne(cmd, g, triggerRunHeaders, triggerRunRow(triggerRun), triggerRun)
	}
	switch triggerRun.Status {
	case kha.TriggerRunStatusCompleted, kha.TriggerRunStatusFailed, kha.TriggerRunStatusSkipped:
	default:
		var err error
		triggerRun, err = waitTriggerRun(cmd, client, triggerID, triggerRun.ID, wf)
		if err != nil {
			return err
		}
	}
	return printTriggerRun(cmd, g, triggerRun)
}

func newTriggersPauseCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "pause <id>",
		Short:             "Pause a Trigger",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: triggerIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			if _, err := client.Triggers.Pause(cmd.Context(), kha.PauseTriggerRequest{ID: args[0]}); err != nil {
				return err
			}
			note(cmd, "trigger %s paused", args[0])
			return nil
		}),
	}
}

func newTriggersUnpauseCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "unpause <id>",
		Short:             "Resume a paused Trigger",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: triggerIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			if _, err := client.Triggers.Unpause(cmd.Context(), kha.UnpauseTriggerRequest{ID: args[0]}); err != nil {
				return err
			}
			note(cmd, "trigger %s unpaused", args[0])
			return nil
		}),
	}
}

var triggerRunHeaders = []string{"ID", "STATUS", "SESSION", "CREATED", "FINISHED"}

func triggerRunRow(r *kha.TriggerRun) []string {
	return []string{r.ID, string(r.Status), valueOr(r.SessionID, "-"), output.Time(r.CreatedAt), triggerTimeOpt(r.FinishedAt)}
}

func newTriggersRunsCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "runs <id>",
		Short:             "List a Trigger's runs (newest first)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: triggerIDCompletion,
	}
	lf := addListFlags(cmd)
	var hasError bool
	cmd.Flags().BoolVar(&hasError, "has-error", false, "only runs with a failure")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if err := lf.validate(); err != nil {
			return err
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		return listTriggerRuns(cmd, g, client, args[0], lf, hasError)
	})
	cmd.AddCommand(newTriggersRunsWaitCmd(g))
	return cmd
}

func listTriggerRuns(cmd *cobra.Command, g *globalFlags, client *kha.Client, triggerID string, lf *listFlags, hasError bool) error {
	params := kha.ListTriggerRunsRequest{ID: triggerID, PageSize: lf.pageSize(), PageToken: lf.token()}
	if hasError {
		params.HasError = new(true)
	}
	if lf.all {
		return printAll(cmd, g, triggerRunHeaders,
			client.Triggers.AllRuns(cmd.Context(), params), triggerRunRow)
	}
	runsResult, err := client.Triggers.ListRuns(cmd.Context(), params)
	if err != nil {
		return err
	}
	page := runsResult.Body
	page.Items = nonNilItems(page.Items)
	page.NextPageToken = nonEmptyPageToken(page.NextPageToken)

	rows := make([][]string, 0, len(page.Items))
	for _, item := range page.Items {
		rows = append(rows, triggerRunRow(item))
	}
	return printList(cmd, g, triggerRunHeaders, rows, page)
}

func newTriggersRunsWaitCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "wait <trigger-id> <run-id>",
		Short:             "Wait for a run to reach a terminal status (completed, failed, or skipped)",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: triggerRunIDCompletion,
	}
	wf := addWaitFlags(cmd)
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		triggerRun, err := waitTriggerRun(cmd, client, args[0], args[1], wf)
		if err != nil {
			return err
		}
		return printTriggerRun(cmd, g, triggerRun)
	})
	return cmd
}
