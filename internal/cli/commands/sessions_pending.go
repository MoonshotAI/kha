package commands

import (
	"strings"

	"github.com/MoonshotAI/kha"

	"github.com/spf13/cobra"

	"github.com/MoonshotAI/kha/internal/cli/output"
	"github.com/MoonshotAI/kha/internal/cli/render"
)

var pendingEventHeaders = []string{"QUEUED_AT", "ID", "TYPE", "SUMMARY"}

// pendingRow renders one queued input event: queue time, event id, input
// type, and a one-line summary of the carried user events.
func pendingRow(p *kha.PendingEvent) []string {
	return []string{output.Time(p.QueuedAt), output.Dash(p.EventID), output.Dash(string(p.Type)), pendingSummary(p.Events)}
}

// pendingSummary summarizes the user events carried by one queued input. The
// input variants share the SessionEvent envelope shape, so a JSON round-trip
// (wrapUnion) reuses render.Summary's variant knowledge; discriminators this
// SDK predates round-trip to a zero Type (open vocabulary) and fall back to
// the input's own vocabulary, never crashing the listing.
func pendingSummary(events []*kha.QueuedUserEventInput) string {
	summaries := make([]string, 0, len(events))
	for _, in := range events {
		if in == nil {
			continue
		}
		if ev, err := wrapUnion[kha.SessionEvent](in); err == nil && ev.Type != 0 {
			summaries = append(summaries, render.Summary(&ev))
			continue
		}
		summaries = append(summaries, "[unknown event "+string(in.Type)+"]")
	}
	return strings.Join(summaries, "; ")
}

func newSessionsPendingCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pending",
		Short: "Inspect and retract a Session's queued input events",
	}
	cmd.AddCommand(
		newSessionsPendingListCmd(g),
		newSessionsPendingRetractCmd(g),
	)
	return cmd
}

func newSessionsPendingListCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "list <session-id>",
		Short:             "List a Session's queued input events in arrival order (not paginated)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: sessionIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			listResult, err := client.Sessions.ListPendingEvents(cmd.Context(), kha.ListPendingEventsRequest{ID: args[0]})
			if err != nil {
				return err
			}
			list := listResult.Body
			list.Items = nonNilItems(list.Items)

			rows := make([][]string, 0, len(list.Items))
			for i := range list.Items {
				rows = append(rows, pendingRow(list.Items[i]))
			}
			return printList(cmd, g, pendingEventHeaders, rows, list)
		}),
	}
}

func newSessionsPendingRetractCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:               "retract <session-id> <event-id>",
		Short:             "Retract a queued input event before it is delivered",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: sessionPendingEventIDCompletion,
		RunE: run(func(cmd *cobra.Command, args []string) error {
			client, _, err := newClient(g)
			if err != nil {
				return err
			}
			if _, err := client.Sessions.RetractEvent(cmd.Context(), kha.RetractSessionEventRequest{ID: args[0], EventID: args[1]}); err != nil {
				return err
			}
			note(cmd, "event %s retracted from session %s", args[1], args[0])
			return nil
		}),
	}
}
