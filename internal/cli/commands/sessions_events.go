package commands

import (
	"time"

	json "encoding/json/v2"

	"github.com/spf13/cobra"

	kha "github.com/MoonshotAI/kha"
	"github.com/MoonshotAI/kha/internal/cli/output"
	"github.com/MoonshotAI/kha/internal/cli/render"
)

var sessionEventHeaders = []string{"PROCESSED_AT", "ID", "TYPE", "SUMMARY"}

// eventEnvelope carries the fields every SessionEvent variant shares. The
// envelope is read through the union's own JSON form, so the listing needs
// no per-variant switch.
type eventEnvelope struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	ProcessedAt time.Time `json:"processed_at"`
}

// eventMeta extracts the shared envelope of one event via a JSON round-trip.
func eventMeta(ev *kha.SessionEvent) eventEnvelope {
	var env eventEnvelope
	if data, err := json.Marshal(ev); err == nil {
		_ = json.Unmarshal(data, &env)
	}
	return env
}

// eventRow renders one history event: timestamp, id, type, one-line summary.
func eventRow(ev *kha.SessionEvent) []string {
	env := eventMeta(ev)
	return []string{output.Time(env.ProcessedAt), output.Dash(env.ID), output.Dash(env.Type), render.Summary(ev)}
}

func eventCursorFooter(cursor string) string {
	if cursor == "" {
		return ""
	}
	return "CURSOR " + cursor
}

func newSessionsEventsCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "events <id>",
		Short: "Browse a Session's event history (oldest first), or tail it live with --watch",
		Long: `Browse the coordinator Thread's retained event history in pages, oldest
first (live-only agent.delta events never appear in history). One event per
row by default; -o json returns the full event models, and a page's
stream_cursor resumes the live stream where the history ends. The default
table ends with the page's CURSOR for --since.

--watch (-w) keeps the events coming after the history:
without --since it first streams the history pages in the
same per-event format — without --transform, the table header prints once
across both phases; transformed events render one generic table per event;
-o json emits NDJSON (one compact event per line), and -o yaml a
--- -separated document stream — then resumes the live stream from the last
page's stream_cursor. Events the stream re-delivers from the page's tail
are dropped by event id (the SDK dedupes within the stream), so the
history/live seam neither loses nor repeats events; empty history without a
stream_cursor falls back to "now". --all drains every history page before
the tail starts,
and --limit/--page-token steer the history pages; --order asc is accepted
(the default) while desc cannot combine with --watch. --since (only valid
with --watch) skips the history and resumes the live stream after the given
cursor ("now" tails new events only). The stream reconnects,
dedupes, and backfills automatically (the same SDK semantics as attach), so
only a non-retryable stream error (exit 1) or Ctrl-C (exit 130) ends the
command — each reported on stderr with the received-event count.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: sessionIDCompletion,
	}
	lf := addListFlags(cmd)
	var order, since string
	var watch bool
	cmd.Flags().StringVar(&order, "order", "", "listing order: asc (default, oldest first) or desc (desc cannot combine with --watch)")
	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "print the history, then stream live events as they arrive (cannot combine with --order desc)")
	cmd.Flags().StringVar(&since, "since", "", `only valid with --watch: skip the history and resume the live stream after this cursor ("now" tails new events only)`)
	mustRegisterFlagCompletion(cmd, "order", completeFlagStatic("asc", "desc"))
	mustRegisterFlagCompletion(cmd, "since", completeFlagStatic("now"))
	cmd.RunE = run(func(cmd *cobra.Command, args []string) error {
		if watch {
			if order == "desc" {
				return errUsage("--watch cannot be combined with --order desc")
			}
			if err := lf.validate(); err != nil {
				return err
			}
			return runEventsWatch(cmd, g, args[0], since, order, lf)
		}
		if cmd.Flags().Changed("since") {
			return errUsage("--since only applies with --watch")
		}
		if err := lf.validate(); err != nil {
			return err
		}
		client, _, err := newClient(g)
		if err != nil {
			return err
		}
		params := kha.ListSessionEventsRequest{ID: args[0]}
		params.PageSize, params.PageToken = lf.pageSize(), lf.token()
		if order != "" {
			params.Order = new(kha.EventOrder(order))
		}
		if lf.all {
			return printAll(cmd, g, sessionEventHeaders, client.Sessions.AllEvents(cmd.Context(), params), eventRow)
		}
		pageResult, err := client.Sessions.ListEvents(cmd.Context(), params)
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

// eventStreamWidths pins the untransformed watch table's fixed-width columns so the header
// lines up with the streamed rows: PROCESSED_AT is output.Time's fixed
// format, and event ids follow the spec's sevt_ + 26 shape. The pin is
// display-only — a longer id simply overflows its column.
func eventStreamWidths() []int {
	widths := make([]int, len(sessionEventHeaders))
	for i, h := range sessionEventHeaders {
		widths[i] = output.DisplayWidth(h)
	}
	widths[0] = output.DisplayWidth("2006-01-02 15:04:05")
	widths[1] = len("sevt_") + 26
	return widths
}

// runEventsWatch prints the Session's events as data rows and keeps the
// listing live. Without --since it first pages the retained history like
// events list does (first page, oldest first, default page size; --all
// drains every page, --limit/--page-token steer the pages), streaming each
// page's rows as it arrives, then resumes the live stream from the last
// page's stream_cursor — events the stream re-delivers from the page's tail
// are dropped by event id (the SDK dedupes within the stream), so the
// history/live seam neither loses nor repeats events, and empty history
// without a stream_cursor falls back to cursor=now. --since skips the
// history phase and subscribes directly. Without --transform, one table
// header spans both phases; transformed events use per-event tables;
// -o json is NDJSON and -o yaml a --- -separated document stream
// throughout. The stream carries the SDK's attach semantics — reconnect,
// dedupe, backfill — so only a non-retryable stream error (exit 1) or
// Ctrl-C (exit 130) ends the command; each is reported on stderr with the
// received-event count.
func runEventsWatch(cmd *cobra.Command, g *globalFlags, id, since, order string, lf *listFlags) error {
	ctx := cmd.Context()
	client, _, err := newClient(g)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	printer, err := output.NewStreamPrinter(out, g.response, sessionEventHeaders, eventStreamWidths())
	if err != nil {
		return err
	}
	n := 0
	cursor := since
	var tailIDs map[string]struct{}
	if cursor == "" {
		params := kha.ListSessionEventsRequest{ID: id}
		params.PageSize, params.PageToken = lf.pageSize(), lf.token()
		if order != "" {
			params.Order = new(kha.EventOrder(order))
		}
		for {
			pageResult, err := client.Sessions.ListEvents(ctx, params)
			if err != nil {
				return err
			}
			page := pageResult.Body

			// Resuming from the page's stream_cursor may re-deliver a few
			// events from the page's tail (per the spec, and the SDK's
			// dedupe cannot see events printed here), so remember this
			// page's ids — only the last page's bound the redelivery
			// window — for the live phase to drop the repeats. Empty ids
			// are never deduplicated (the SDK's rule too).
			tailIDs = make(map[string]struct{}, len(page.Items))
			for i := range page.Items {
				if page.Items[i] == nil {
					continue
				}
				if id := eventMeta(page.Items[i]).ID; id != "" {
					tailIDs[id] = struct{}{}
				}
				n++
				if err := printer.Print(page.Items[i], func() []string { return eventRow(page.Items[i]) }); err != nil {
					return err
				}
			}
			if page.StreamCursor != nil {
				cursor = *page.StreamCursor
			}
			if !lf.all || page.NextPageToken == nil || *page.NextPageToken == "" {
				break
			}
			params.PageToken = page.NextPageToken
		}
		if cursor == "" {
			cursor = kha.CursorNow
		}
	}
	var streamErr error
	// Deltas stay opted in so the followed stream keeps printing the
	// incremental previews the API no longer sends by default.
	for ev, err := range client.Sessions.Events(ctx, id, kha.WithCursor(cursor), kha.WithDeltas(true)) {
		if err != nil {
			if ctx.Err() == nil {
				streamErr = err
			}
			break
		}
		if id := eventMeta(ev).ID; id != "" {
			if _, redelivered := tailIDs[id]; redelivered {
				continue
			}
		}
		n++
		if err := printer.Print(ev, func() []string { return eventRow(ev) }); err != nil {
			return err
		}
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
}
