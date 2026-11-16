package commands

import (
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	kha "github.com/MoonshotAI/kha"
	"github.com/MoonshotAI/kha/internal/cli/output"
	"github.com/MoonshotAI/kha/internal/creds"
)

var workItemStatusNames = []string{
	string(kha.WorkItemStatusQueued),
	string(kha.WorkItemStatusStarting),
	string(kha.WorkItemStatusActive),
	string(kha.WorkItemStatusStopping),
	string(kha.WorkItemStatusCompleted),
	string(kha.WorkItemStatusFailed),
	string(kha.WorkItemStatusCancelled),
}

var workItemHeaders = []string{"ID", "SESSION", "STATUS", "GENERATION", "WORKER", "CREATED"}

func workItemRow(w *kha.WorkItem) []string {
	return []string{w.ID, w.SessionID, string(w.Status), strconv.FormatInt(w.Generation, 10), valueOr(w.WorkerID, "-"), output.Time(w.CreatedAt)}
}

// Poll alone returns the Session secret bundle; custom dispatchers read it from stdout.
var workPollHeaders = []string{"ID", "SESSION", "STATUS", "GENERATION", "WORKER", "CREATED", "SECRET"}

func workPollRow(w *kha.WorkItem) []string {
	return append(workItemRow(w), valueOr(w.Secret, "-"))
}

var workStatsHeaders = []string{"DEPTH", "CLAIMED", "WORKERS", "OLDEST QUEUED"}

func workStatsRow(s *kha.WorkQueueStats) []string {
	return []string{
		strconv.FormatInt(s.Depth, 10),
		strconv.FormatInt(s.Claimed, 10),
		strconv.FormatInt(s.Workers, 10),
		output.Time(valueOr(s.OldestQueuedAt, time.Time{})),
	}
}

var workHeartbeatHeaders = []string{"STATE", "GENERATION", "TTL SECONDS", "LAST HEARTBEAT"}

func workHeartbeatRow(h *kha.WorkHeartbeat) []string {
	return []string{string(h.State), strconv.FormatInt(h.Generation, 10), strconv.FormatInt(h.TTLSeconds, 10), output.Time(h.LastHeartbeat)}
}

// Queue commands accept a work key or user API key; Poll never auto-retries.
type queueCredFlags struct{ tokenFlags }

func (f *queueCredFlags) register(cmd *cobra.Command) {
	f.tokenFlags.register(cmd, "the environment work key (ewk-)")
}

func (f *queueCredFlags) client(cmd *cobra.Command, g *globalFlags, poll bool) (*kha.Client, error) {
	if f.explicit(cmd) {
		secret, err := f.load(cmd, "ewk-")
		if err != nil {
			return nil, err
		}
		cred, err := resolveConnection(g, false)
		if err != nil {
			return nil, err
		}
		if poll {
			return kha.NewPollClient(cred.baseURL, secret)
		}
		return creds.NewClient(cred.baseURL, secret, kha.WithUserAgent("kha-cli/"+cliVersion()))
	}
	if poll {
		client, _, err := newClient(g, kha.WithMaxRetries(0))
		return client, err
	}
	client, _, err := newClient(g)
	return client, err
}

// Lease operations require an explicit machine credential, never a user API key or keychain.
type leaseCredFlags struct{ tokenFlags }

func (f *leaseCredFlags) register(cmd *cobra.Command) {
	f.tokenFlags.register(cmd, "a Session work token (swt-) or environment work key (ewk-)")
}

func (f *leaseCredFlags) client(cmd *cobra.Command, g *globalFlags) (*kha.Client, error) {
	secret, err := f.load(cmd, "swt-", "ewk-")
	if err != nil {
		return nil, err
	}
	cred, err := resolveConnection(g, false)
	if err != nil {
		return nil, err
	}
	return creds.NewClient(cred.baseURL, secret, kha.WithUserAgent("kha-cli/"+cliVersion()))
}

// CAS validation requires the worker identity and generation returned by Poll.
type leaseIdentityFlags struct {
	workerID   string
	generation int64
}

func (f *leaseIdentityFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.workerID, "worker-id", "", "worker identity holding the lease (required)")
	cmd.Flags().Int64Var(&f.generation, "generation", 0, "fencing generation the poll returned (required)")
}

func (f *leaseIdentityFlags) validate(cmd *cobra.Command) error {
	if f.workerID == "" || !cmd.Flags().Changed("generation") {
		return errUsage("work %s requires --worker-id and --generation", cmd.Name())
	}
	return nil
}

func newEnvironmentsWorkCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "work",
		Short: "Inspect and operate an Environment's self-hosted work queue",
		Long:  "Inspect and operate an Environment's self-hosted work queue.\nWork keys are issued in the console; the CLI consumes existing keys only.",
	}
	cmd.AddCommand(
		newWorkListCmd(g),
		newWorkGetCmd(g),
		newWorkStatsCmd(g),
		newWorkPollCmd(g),
		newWorkAckCmd(g),
		newWorkHeartbeatCmd(g),
		newWorkStopCmd(g),
	)
	return cmd
}

func newWorkListCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "list <environment-id>",
		Short:             "List an Environment's WorkItems, newest first (terminal rows kept for audit)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: environmentIDCompletion,
	}
	lf := addListFlags(cmd)
	cf := &queueCredFlags{}
	cf.register(cmd)
	var status string
	cmd.Flags().StringVar(&status, "status", "", "filter by lifecycle status: "+strings.Join(workItemStatusNames, ", "))
	mustRegisterFlagCompletion(cmd, "status", completeFlagStatic(workItemStatusNames...))
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if err := lf.validate(); err != nil {
			return err
		}
		params := kha.ListWorkRequest{EnvironmentID: args[0], PageSize: lf.pageSize(), PageToken: lf.token()}
		if status != "" {
			if !slices.Contains(workItemStatusNames, status) {
				return errUsage("invalid --status %q: want one of %s", status, strings.Join(workItemStatusNames, ", "))
			}
			params.Status = new(kha.WorkItemStatus(status))
		}
		client, err := cf.client(cmd, g, false)
		if err != nil {
			return err
		}
		if lf.all {
			return printAll(cmd, g, workItemHeaders, client.WorkItems.All(cmd.Context(), params),
				workItemRow)
		}
		itemsResult, err := client.WorkItems.List(cmd.Context(), params)
		if err != nil {
			return err
		}
		page := itemsResult.Body
		page.Items = nonNilItems(page.Items)
		page.NextPageToken = nonEmptyPageToken(page.NextPageToken)

		rows := make([][]string, 0, len(page.Items))
		for _, w := range page.Items {
			rows = append(rows, workItemRow(w))
		}
		return printList(cmd, g, workItemHeaders, rows, page)
	})
	return cmd
}

func newWorkGetCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "get <environment-id> <work-id>",
		Short:             "Get one WorkItem by ID",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: environmentIDCompletion,
	}
	cf := &queueCredFlags{}
	cf.register(cmd)
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		client, err := cf.client(cmd, g, false)
		if err != nil {
			return err
		}
		itemResult, err := client.WorkItems.Get(cmd.Context(), kha.GetWorkItemRequest{EnvironmentID: args[0], ID: args[1]})
		if err != nil {
			return err
		}
		item := itemResult.Body

		return printOne(cmd, g, workItemHeaders, workItemRow(item), item)
	})
	return cmd
}

func newWorkStatsCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "stats <environment-id>",
		Short:             "Show queue depth, claimed items, workers and the oldest queued item",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: environmentIDCompletion,
	}
	cf := &queueCredFlags{}
	cf.register(cmd)
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		client, err := cf.client(cmd, g, false)
		if err != nil {
			return err
		}
		statsResult, err := client.WorkItems.Stats(cmd.Context(), kha.GetWorkItemStatsRequest{EnvironmentID: args[0]})
		if err != nil {
			return err
		}
		stats := statsResult.Body

		return printOne(cmd, g, workStatsHeaders, workStatsRow(stats), stats)
	})
	return cmd
}

func newWorkPollCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "poll <environment-id>",
		Short:             "Claim the oldest queued WorkItem (leased until ack, heartbeat or reclaim)",
		Long:              "Claim once without acknowledging or executing the work.\nFor continuous consumption with ack and execution, use kha worker poll.\nUnacknowledged deliveries may be reclaimed after the platform ack window (default 5s).\nThe printed SECRET column is the session's lease credential: protect it like a password and keep it out of logs and shared output.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: environmentIDCompletion,
	}
	cf := &queueCredFlags{}
	cf.register(cmd)
	var workerID string
	var block, leaseTTL, reclaimOlderThan time.Duration
	cmd.Flags().StringVar(&workerID, "worker-id", "", "worker identity claiming the work (required)")
	cmd.Flags().DurationVar(&block, "block", 0, "long-poll budget (at most 999ms); 0 returns immediately")
	cmd.Flags().DurationVar(&leaseTTL, "lease-ttl", 0, "requested lease length (non-positive takes the platform default)")
	cmd.Flags().DurationVar(&reclaimOlderThan, "reclaim-older-than", 0, "reclaim window for unacknowledged deliveries (server default 5s)")
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if workerID == "" {
			return errUsage("work poll requires --worker-id")
		}
		if block < 0 || block > 999*time.Millisecond {
			return errUsage("--block must be between 0 and 999ms, got %s", block)
		}
		params := kha.PollWorkItemsRequest{EnvironmentID: args[0], WorkerID: workerID}
		if cmd.Flags().Changed("block") {
			params.BlockMs = new(int32(block.Milliseconds()))
		}
		if cmd.Flags().Changed("lease-ttl") {
			params.LeaseTTLMs = new(int32(leaseTTL.Milliseconds()))
		}
		if cmd.Flags().Changed("reclaim-older-than") {
			params.ReclaimOlderThanMs = new(int32(reclaimOlderThan.Milliseconds()))
		}
		client, err := cf.client(cmd, g, true)
		if err != nil {
			return err
		}
		itemResult, err := client.WorkItems.Poll(cmd.Context(), params)
		if err != nil {
			return err
		}
		item := itemResult.Body
		if item == nil {
			// No claimable work within budget is a normal poll result.
			note(cmd, "no queued work in environment %s", args[0])
			return nil
		}
		note(cmd, "claimed work item %s (session %s, generation %d)", item.ID, item.SessionID, item.Generation)
		noteWorkAck(cmd, g, args[0], item.ID, workerID, item.Generation, cf.tokenFlags)
		return printOne(cmd, g, workPollHeaders, workPollRow(item), item)
	})
	return cmd
}

func newWorkAckCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "ack <environment-id> <work-id>",
		Short:             "Acknowledge a delivered WorkItem (queued -> starting)",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: environmentIDCompletion,
	}
	id := &leaseIdentityFlags{}
	id.register(cmd)
	cf := &leaseCredFlags{}
	cf.register(cmd)
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if err := id.validate(cmd); err != nil {
			return err
		}
		client, err := cf.client(cmd, g)
		if err != nil {
			return err
		}
		body := &kha.AckWorkItemRequest{WorkerID: id.workerID, Generation: id.generation}
		itemResult, err := client.WorkItems.Ack(cmd.Context(), kha.AckWorkItemHTTPRequest{EnvironmentID: args[0], ID: args[1], Body: body})
		if err != nil {
			return err
		}
		item := itemResult.Body

		note(cmd, "work item %s acknowledged", args[1])
		return printOne(cmd, g, workItemHeaders, workItemRow(item), item)
	})
	return cmd
}

func newWorkHeartbeatCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "heartbeat <environment-id> <work-id>",
		Short:             "Renew the lease on a claimed WorkItem (first heartbeat activates it)",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: environmentIDCompletion,
	}
	id := &leaseIdentityFlags{}
	id.register(cmd)
	cf := &leaseCredFlags{}
	cf.register(cmd)
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if err := id.validate(cmd); err != nil {
			return err
		}
		client, err := cf.client(cmd, g)
		if err != nil {
			return err
		}
		body := &kha.HeartbeatWorkItemRequest{WorkerID: id.workerID, Generation: id.generation}
		hbResult, err := client.WorkItems.Heartbeat(cmd.Context(), kha.HeartbeatWorkItemHTTPRequest{EnvironmentID: args[0], ID: args[1], Body: body})
		if err != nil {
			return err
		}
		hb := hbResult.Body

		note(cmd, "work item %s lease renewed (%s)", args[1], hb.State)
		return printOne(cmd, g, workHeartbeatHeaders, workHeartbeatRow(hb), hb)
	})
	return cmd
}

func newWorkStopCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "stop <environment-id> <work-id>",
		Short:             "Request a graceful stop, or confirm completion with --final-status completed",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: environmentIDCompletion,
	}
	id := &leaseIdentityFlags{}
	id.register(cmd)
	cf := &leaseCredFlags{}
	cf.register(cmd)
	var finalStatus string
	cmd.Flags().StringVar(&finalStatus, "final-status", "", `send "completed" after tools and result writes finish (omit to request a graceful stop)`)
	mustRegisterFlagCompletion(cmd, "final-status", completeFlagStatic("completed"))
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if err := id.validate(cmd); err != nil {
			return err
		}
		body := &kha.StopWorkItemRequest{WorkerID: id.workerID, Generation: id.generation}
		if cmd.Flags().Changed("final-status") {
			// final_status is a constant field: completed is currently its only valid value.
			if finalStatus != string(kha.StopWorkItemRequestFinalStatusCompleted) {
				return errUsage(`invalid --final-status %q: only "completed" is supported`, finalStatus)
			}
			body.FinalStatus = new(kha.StopWorkItemRequestFinalStatusCompleted)
		}
		client, err := cf.client(cmd, g)
		if err != nil {
			return err
		}
		if _, err := client.WorkItems.Stop(cmd.Context(), kha.StopWorkItemHTTPRequest{EnvironmentID: args[0], ID: args[1], Body: body}); err != nil {
			return err
		}
		if body.FinalStatus != nil {
			note(cmd, "work item %s completed", args[1])
		} else {
			note(cmd, "stop requested for work item %s", args[1])
		}
		return nil
	})
	return cmd
}

// Terminal escaping alone does not protect pasted commands from shell metacharacters.
func shellWord(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func noteWorkAck(cmd *cobra.Command, g *globalFlags, env, work, worker string, generation int64, token tokenFlags) {
	// Do not turn control characters into a different, pasteable identity.
	for _, value := range []string{env, work, worker, token.file, g.baseURL, g.profile} {
		if strings.ContainsFunc(value, unicode.IsControl) {
			note(cmd, "next: acknowledge the work with the same worker ID, generation and a machine credential")
			return
		}
	}
	parts := []string{"kha environments work ack", shellWord(env), shellWord(work), "--worker-id", shellWord(worker), "--generation", strconv.FormatInt(generation, 10)}
	if g.baseURL != "" {
		parts = append(parts, "--base-url", shellWord(g.baseURL))
	}
	if g.profile != "" {
		parts = append(parts, "--profile", shellWord(g.profile))
	}
	if token.file != "" && token.file != "-" {
		parts = append(parts, "--token-file", shellWord(token.file))
		note(cmd, "next (POSIX shell): %s", strings.Join(parts, " "))
	} else {
		note(cmd, "next (POSIX shell; replace TOKEN_FILE with a work-key or Session-token file): %s --token-file TOKEN_FILE", strings.Join(parts, " "))
	}
}
