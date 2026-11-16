package commands

import (
	"github.com/MoonshotAI/kha"
	"github.com/spf13/cobra"

	"github.com/MoonshotAI/kha/internal/cli/output"
)

var sessionThreadHeaders = []string{"ID", "STATUS", "PARENT", "AGENT", "CREATED"}

// threadRow renders one Thread; the coordinator has no parent.
func threadRow(t *kha.SessionThread) []string {
	agent := "-"
	if a := t.Agent; a != nil {
		agent = a.Name + "@" + a.Version
	}
	return []string{t.ID, string(t.Status), valueOr(t.ParentThreadID, "-"), agent, output.Time(t.CreatedAt)}
}

func newSessionsThreadsCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "threads",
		Short: "Inspect a Session's Threads",
		Long: `Inspect a Session's Threads: the coordinator first, then child Threads in
creation order. Browse one Thread's history with
'kha sessions threads events <session-id> <thread-id>', or attach to its
live stream with 'kha sessions attach <session-id> --thread <thread-id>'.`,
	}
	cmd.AddCommand(
		newSessionsThreadsListCmd(g),
		newSessionsThreadsGetCmd(g),
		newSessionsThreadsEventsCmd(g),
		newSessionsThreadsArchiveCmd(g),
	)
	return cmd
}

func newSessionsThreadsListCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "list <session-id>",
		Short:             "List a Session's Threads (coordinator first)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: sessionIDCompletion,
	}
	lf := addListFlags(cmd)
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if err := lf.validate(); err != nil {
			return err
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		params := kha.ListSessionThreadsRequest{ID: args[0]}
		params.PageSize, params.PageToken = lf.pageSize(), lf.token()
		if lf.all {
			return printAll(cmd, g, sessionThreadHeaders,
				client.Sessions.AllThreads(cmd.Context(), params), threadRow)
		}
		threadsResult, err := client.Sessions.ListThreads(cmd.Context(), params)
		if err != nil {
			return err
		}
		page := threadsResult.Body
		page.Items = nonNilItems(page.Items)
		page.NextPageToken = nonEmptyPageToken(page.NextPageToken)

		rows := make([][]string, 0, len(page.Items))
		for _, item := range page.Items {
			rows = append(rows, threadRow(item))
		}
		return printList(cmd, g, sessionThreadHeaders, rows, page)
	})
	return cmd
}

func newSessionsThreadsGetCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "get <session-id> <thread-id>",
		Short:             "Get one Thread with its pinned Agent snapshot",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: sessionThreadIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			threadResult, err := client.Sessions.GetThread(cmd.Context(), kha.GetSessionThreadRequest{ID: args[0], ThreadID: args[1]})
			if err != nil {
				return err
			}
			thread := threadResult.Body

			return printOne(cmd, g, sessionThreadHeaders, threadRow(thread), thread)
		}),
	}
}

// newSessionsThreadsEventsCmd is the Thread-level counterpart of
// 'sessions events': it pages one Thread's retained event history instead of
// the coordinator's. The live stream stays with 'sessions attach --thread'.
func newSessionsThreadsEventsCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "events <session-id> <thread-id>",
		Short: "Browse one Thread's event history (oldest first)",
		Long: `Browse one Thread's retained event history in pages, oldest first
(live-only agent.delta events never appear in history). One event per row;
-o json returns the full event models. The default table ends with the
page's CURSOR for sessions attach --thread --since.`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: sessionThreadIDCompletion,
	}
	lf := addListFlags(cmd)
	var order string
	cmd.Flags().StringVar(&order, "order", "", "listing order: asc (default, oldest first) or desc")
	mustRegisterFlagCompletion(cmd, "order", completeFlagStatic("asc", "desc"))
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if err := lf.validate(); err != nil {
			return err
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		params := kha.ListThreadEventsRequest{ID: args[0], ThreadID: args[1]}
		params.PageSize, params.PageToken = lf.pageSize(), lf.token()
		if order != "" {
			params.Order = new(kha.EventOrder(order))
		}
		if lf.all {
			return printAll(cmd, g, sessionEventHeaders,
				client.Sessions.AllThreadEvents(cmd.Context(), params), eventRow)
		}
		pageResult, err := client.Sessions.ListThreadEvents(cmd.Context(), params)
		if err != nil {
			return err
		}
		page := pageResult.Body
		page.Items = nonNilItems(page.Items)

		rows := make([][]string, 0, len(page.Items))
		for i := range page.Items {
			rows = append(rows, eventRow(page.Items[i]))
		}
		footer := ""
		if page.StreamCursor != nil {
			footer = eventCursorFooter(*page.StreamCursor)
		}
		return output.PrintResponse(cmd.OutOrStdout(), output.Response{
			Model:     page,
			Headers:   sessionEventHeaders,
			Rows:      rows,
			ItemsPath: "items",
			Footer:    footer,
		}, g.response)
	})
	return cmd
}

func newSessionsThreadsArchiveCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "archive <session-id> <thread-id>",
		Short:             "Archive a child Thread (the coordinator is archived with its Session)",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: sessionThreadIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			if _, err := client.Sessions.ArchiveThread(cmd.Context(), kha.ArchiveSessionThreadRequest{ID: args[0], ThreadID: args[1]}); err != nil {
				return err
			}
			note(cmd, "thread %s archived", args[1])
			return nil
		}),
	}
}
