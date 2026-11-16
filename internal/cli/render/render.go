// Package render turns a Session's live event stream into plain text for
// `kha sessions attach`: streaming plain text, no TUI framework.
//
// The renderer is a tiny state machine over SessionEvent variants:
//
//   - agent.delta text/thinking payloads append in place (typewriter feel).
//     The text fragments accumulate; when the complete agent.message event
//     then arrives it is prefix-compared with the accumulated text: a
//     message leading with exactly the streamed fragments (the normal case,
//     including a plain reconnect re-delivering the same authoritative
//     message) prints only the unseen suffix — never nothing, never twice —
//     and a diverging message prints in full.
//   - agent.message bodies print directly; agent.thinking becomes one dim
//     parenthesized summary line; agent.tool_use / agent.tool_result become
//     one summary line each (tool name + status).
//   - session.status transitions and session.error go to the error writer
//     (stderr), the latter in red.
//   - Known variants the renderer does not handle specially print a one-line
//     "[<type>]" trace so the stream stays transparent. Discriminators the
//     spec does not declare decode to a zero Type (open vocabulary, payload
//     kept in Raw) and print an "[unknown]" trace instead of failing.
//
// Colors are ANSI escapes, chosen by the caller (UseColor); when disabled
// the output is plain text. All event text is untrusted: every server or
// agent-provided string passes through stripEscapes before reaching a tty,
// so a crafted message cannot inject ANSI/OSC sequences into the user's
// terminal.
package render

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/MoonshotAI/kha"

	"github.com/MoonshotAI/kha/internal/cli/output"
)

// ANSI escape codes, emitted only when the renderer has color enabled.
const (
	ansiReset = "\x1b[0m"
	ansiDim   = "\x1b[2m"
	ansiRed   = "\x1b[31m"
	ansiCyan  = "\x1b[36m"
)

// summaryWidth caps one-line summaries (thinking, tool input).
const summaryWidth = 160

// Renderer writes a human-readable rendering of Session events to out,
// with status and error diagnostics on errOut. It is not safe for
// concurrent use.
type Renderer struct {
	out    io.Writer
	errOut io.Writer
	color  bool

	open        bool            // a delta line without trailing newline is open on out
	hasStreamed bool            // streamed holds the current agent message's streamed text fragments
	streamed    strings.Builder // the text-delta content printed for the current agent message
}

// New returns a Renderer writing to out (event stream) and errOut (status
// transitions, errors). color enables ANSI styling.
func New(out, errOut io.Writer, color bool) *Renderer {
	return &Renderer{out: out, errOut: errOut, color: color}
}

// UseColor reports whether an attached terminal should receive ANSI colors:
// the file is a character device, NO_COLOR is unset, and TERM is not "dumb".
func UseColor(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// Event renders one Session event.
func (r *Renderer) Event(ev *kha.SessionEvent) {
	switch {
	case ev.AsUserMessageEvent() != nil:
		m := ev.AsUserMessageEvent()
		r.closeLine()
		r.line(r.dim("> " + oneLine(contentText(m.Data.Content), summaryWidth)))
	case ev.AsAgentMessageEvent() != nil:
		m := ev.AsAgentMessageEvent()
		if r.hasStreamed {
			// The message's fragments already streamed: print only what the
			// stream did not show (the prefix comparison resets either way).
			r.finishStreamedMessage(contentText(m.Data.Content))
			return
		}
		r.closeLine()
		if text := contentText(m.Data.Content); text != "" {
			_, _ = fmt.Fprintln(r.out, text)
		}
	case ev.AsAgentDeltaEvent() != nil:
		r.delta(ev.AsAgentDeltaEvent().Data)
	case ev.AsAgentThinkingEvent() != nil:
		m := ev.AsAgentThinkingEvent()
		r.closeLine()
		r.line(r.dim("(thinking) " + oneLine(contentText(m.Data.Content), summaryWidth)))
	case ev.AsAgentToolUseEvent() != nil:
		m := ev.AsAgentToolUseEvent()
		r.closeLine()
		summary := "[tool] " + stripEscapes(m.Data.ToolUse.Name)
		if perm := string(m.Data.EvaluatedPermission); perm != "" {
			summary += " (" + stripEscapes(perm) + ")"
		}
		r.line(r.cyan(summary))
	case ev.AsAgentToolResultEvent() != nil:
		m := ev.AsAgentToolResultEvent()
		r.closeLine()
		res := m.Data.ToolResult
		if res.IsError {
			r.line(r.red("[tool result] error" + toolResultSuffix(res.StructuredContent)))
		} else {
			r.line(r.dim("[tool result] ok" + toolResultSuffix(res.StructuredContent)))
		}
	case ev.AsSessionStatusEvent() != nil:
		m := ev.AsSessionStatusEvent()
		r.closeLine()
		line := "[status] " + stripEscapes(string(m.Data.Status))
		if sr := m.Data.StopReason; sr != nil {
			line += " (stop: " + stripEscapes(string(sr.Type)) + ")"
		}
		_, _ = fmt.Fprintln(r.errOut, r.dim(line))
	case ev.AsSessionErrorEvent() != nil:
		m := ev.AsSessionErrorEvent()
		r.closeLine()
		_, _ = fmt.Fprintf(r.errOut, "%s\n", r.red("[session error] "+stripEscapes(string(m.Data.ErrorType))+": "+stripEscapes(m.Data.Message)))
	case ev.AsSessionTitleUpdatedEvent() != nil:
		r.closeLine()
		_, _ = fmt.Fprintln(r.errOut, r.dim("[title] "+stripEscapes(ev.AsSessionTitleUpdatedEvent().Data.Title)))
	default:
		// Known but not specially rendered (thread lifecycle events):
		// keep a one-line trace so the stream stays transparent.
		r.closeLine()
		r.line(r.dim("[" + stripEscapes(eventDiscriminator(ev)) + "]"))
	}
}

// finishStreamedMessage prints the authoritative agent.message of a turn
// whose text already streamed as agent.delta fragments, then resets the
// accumulation. When the message leads with exactly the streamed text — the
// normal case, including a plain reconnect re-delivering the same
// authoritative message — only the unseen suffix prints (an exact match
// just closes the streamed line). When it diverges, the streamed preview
// is closed and the message prints in full.
func (r *Renderer) finishStreamedMessage(text string) {
	streamed := r.streamed.String()
	r.hasStreamed = false
	r.streamed.Reset()
	if rest, ok := strings.CutPrefix(text, streamed); ok {
		if r.open {
			_, _ = fmt.Fprintln(r.out, rest)
			r.open = false
		} else if rest != "" {
			r.line(rest)
		}
		return
	}
	r.closeLine()
	if text != "" {
		_, _ = fmt.Fprintln(r.out, text)
	}
}

// Close terminates an open delta line. Call it when the stream ends.
func (r *Renderer) Close() {
	r.closeLine()
	r.hasStreamed = false
	r.streamed.Reset()
}

// Recover resets the renderer after the SDK reports a completed
// expired-cursor recovery (kha.WithOnRecovery): the agent.delta
// fragments already printed were partial previews the spec says to discard,
// so the authoritative agent.message arriving after re-anchoring must print
// in full instead of being prefix-compared against them. The partial
// preview line is closed (printed bytes cannot be erased) and a note on
// errOut tells the user the stream jumped.
func (r *Renderer) Recover() {
	r.closeLine()
	r.hasStreamed = false
	r.streamed.Reset()
	_, _ = fmt.Fprintln(r.errOut, r.dim("note: stream recovered; partial deltas discarded"))
}

// Summary renders one event as a single plain-text line (no color, no
// writer) for history listings such as `kha sessions events`. It shares
// Event's variant knowledge but never streams: a summary is all a table row
// has room for.
func Summary(ev *kha.SessionEvent) string {
	switch {
	case ev.AsUserMessageEvent() != nil:
		return "> " + oneLine(contentText(ev.AsUserMessageEvent().Data.Content), summaryWidth)
	case ev.AsAgentMessageEvent() != nil:
		return oneLine(contentText(ev.AsAgentMessageEvent().Data.Content), summaryWidth)
	case ev.AsAgentThinkingEvent() != nil:
		return "(thinking) " + oneLine(contentText(ev.AsAgentThinkingEvent().Data.Content), summaryWidth)
	case ev.AsAgentDeltaEvent() != nil:
		return deltaSummary(ev.AsAgentDeltaEvent().Data)
	case ev.AsAgentToolUseEvent() != nil:
		m := ev.AsAgentToolUseEvent()
		s := "[tool] " + stripEscapes(m.Data.ToolUse.Name)
		if perm := string(m.Data.EvaluatedPermission); perm != "" {
			s += " (" + stripEscapes(perm) + ")"
		}
		return s
	case ev.AsAgentToolResultEvent() != nil:
		res := ev.AsAgentToolResultEvent().Data.ToolResult
		if res.IsError {
			return "[tool result] error" + toolResultSuffix(res.StructuredContent)
		}
		return "[tool result] ok" + toolResultSuffix(res.StructuredContent)
	case ev.AsAgentThreadMessageReceivedEvent() != nil:
		m := ev.AsAgentThreadMessageReceivedEvent()
		return "[thread message from " + stripEscapes(m.Data.FromSessionThreadID) + "] " + oneLine(contentText(m.Data.Content), summaryWidth)
	case ev.AsAgentThreadMessageSentEvent() != nil:
		m := ev.AsAgentThreadMessageSentEvent()
		return "[thread message to " + stripEscapes(m.Data.ToSessionThreadID) + "] " + oneLine(contentText(m.Data.Content), summaryWidth)
	case ev.AsAgentThreadContextCompactedEvent() != nil:
		return "[thread context compacted]"
	case ev.AsSessionStatusEvent() != nil:
		m := ev.AsSessionStatusEvent()
		s := "status -> " + stripEscapes(string(m.Data.Status))
		if sr := m.Data.StopReason; sr != nil {
			s += " (stop: " + stripEscapes(string(sr.Type)) + ")"
		}
		return s
	case ev.AsSessionErrorEvent() != nil:
		m := ev.AsSessionErrorEvent()
		return "[error] " + stripEscapes(string(m.Data.ErrorType)) + ": " + oneLine(stripEscapes(m.Data.Message), summaryWidth)
	case ev.AsSessionThreadCreatedEvent() != nil:
		return "[thread created] " + stripEscapes(ev.AsSessionThreadCreatedEvent().Data.AgentName)
	case ev.AsSessionThreadStatusEvent() != nil:
		m := ev.AsSessionThreadStatusEvent()
		s := "[thread status] " + stripEscapes(string(m.Data.Status))
		if sr := m.Data.StopReason; sr != nil {
			s += " (stop: " + stripEscapes(string(sr.Type)) + ")"
		}
		return s
	case ev.AsSessionTitleUpdatedEvent() != nil:
		return "[title] " + stripEscapes(ev.AsSessionTitleUpdatedEvent().Data.Title)
	default:
		return "[" + stripEscapes(eventDiscriminator(ev)) + "]"
	}
}

// deltaSummary is Summary's counterpart for one agent.delta payload.
func deltaSummary(d kha.DeltaPayload) string {
	switch {
	case d.AsTextDeltaPayload() != nil:
		return oneLine(stripEscapes(d.AsTextDeltaPayload().Delta), summaryWidth)
	case d.AsThinkingDeltaPayload() != nil:
		return "(thinking) " + oneLine(stripEscapes(d.AsThinkingDeltaPayload().Delta), summaryWidth)
	case d.AsToolUseDeltaPayload() != nil:
		if name := d.AsToolUseDeltaPayload().Name; name != nil {
			return "[tool: " + stripEscapes(*name) + "]"
		}
		return "[tool input delta]"
	default:
		return "[delta]"
	}
}

// delta renders one agent.delta payload: text and thinking append in place;
// tool-use input fragments stay hidden, surfacing only the tool name once.
// Text fragments accumulate into streamed for the agent.message prefix
// comparison.
func (r *Renderer) delta(d kha.DeltaPayload) {
	switch {
	case d.AsTextDeltaPayload() != nil:
		stripped := stripEscapes(d.AsTextDeltaPayload().Delta)
		_, _ = fmt.Fprint(r.out, stripped)
		r.streamed.WriteString(stripped)
		r.hasStreamed = true
		r.open = true
	case d.AsThinkingDeltaPayload() != nil:
		_, _ = fmt.Fprint(r.out, r.dim(stripEscapes(d.AsThinkingDeltaPayload().Delta)))
		r.open = true
	case d.AsToolUseDeltaPayload() != nil:
		if name := d.AsToolUseDeltaPayload().Name; name != nil {
			r.closeLine()
			r.line(r.dim("[tool: " + stripEscapes(*name) + "]"))
		}
	}
}

// closeLine ends an open delta line with a newline.
func (r *Renderer) closeLine() {
	if r.open {
		_, _ = fmt.Fprintln(r.out)
		r.open = false
	}
}

// line prints one full line to out.
func (r *Renderer) line(s string) {
	_, _ = fmt.Fprintln(r.out, s)
}

func (r *Renderer) style(code, s string) string {
	if !r.color || s == "" {
		return s
	}
	return code + s + ansiReset
}
func (r *Renderer) dim(s string) string  { return r.style(ansiDim, s) }
func (r *Renderer) red(s string) string  { return r.style(ansiRed, s) }
func (r *Renderer) cyan(s string) string { return r.style(ansiCyan, s) }

// contentText concatenates the text blocks of a content list; non-text
// blocks (images, files) render as a bracketed placeholder. Everything it
// returns has been through stripEscapes (server text is untrusted).
func contentText(blocks []*kha.ContentBlock) string {
	var b strings.Builder
	for _, block := range blocks {
		if block == nil {
			continue
		}
		if t := block.AsTextContentBlock(); t != nil {
			b.WriteString(stripEscapes(t.Text))
			continue
		}
		b.WriteString("[" + stripEscapes(contentBlockDiscriminator(block)) + "]")
	}
	return b.String()
}

// toolResultSuffix notes artifact deliveries on a tool result line.
// Artifact cards are user-facing: they live in structured_content, never
// in the model-facing content.
func toolResultSuffix(content []*kha.StructuredToolResultContent) string {
	for _, c := range content {
		if c == nil {
			continue
		}
		if a := c.AsArtifactToolResultContent(); a != nil {
			return " -> artifact " + stripEscapes(a.Name) + " (v" + stripEscapes(a.Version) + ")"
		}
	}
	return ""
}

// eventDiscriminator names one event variant for the one-line trace of
// variants the renderer does not handle specially. Discriminators the pinned
// spec does not declare decode to a zero Type (open vocabulary, payload kept
// in Raw) and land here as "unknown".
func eventDiscriminator(ev *kha.SessionEvent) string {
	switch ev.Type {
	case kha.SessionEventTypeUserMessageEvent:
		return "user.message"
	case kha.SessionEventTypeUserInterruptEvent:
		return "user.interrupt"
	case kha.SessionEventTypeUserToolConfirmationEvent:
		return "user.tool_confirmation"
	case kha.SessionEventTypeUserCustomToolResultEvent:
		return "user.custom_tool_result"
	case kha.SessionEventTypeAgentMessageEvent:
		return "agent.message"
	case kha.SessionEventTypeAgentThinkingEvent:
		return "agent.thinking"
	case kha.SessionEventTypeAgentToolUseEvent:
		return "agent.tool_use"
	case kha.SessionEventTypeAgentToolResultEvent:
		return "agent.tool_result"
	case kha.SessionEventTypeAgentModelStartedEvent:
		return "agent.model_started"
	case kha.SessionEventTypeAgentThinkingStartedEvent:
		return "agent.thinking_started"
	case kha.SessionEventTypeAgentMessageStartedEvent:
		return "agent.message_started"
	case kha.SessionEventTypeAgentToolStartedEvent:
		return "agent.tool_started"
	case kha.SessionEventTypeAgentCustomToolUseEvent:
		return "agent.custom_tool_use"
	case kha.SessionEventTypeAgentThreadMessageReceivedEvent:
		return "agent.thread_message_received"
	case kha.SessionEventTypeAgentThreadMessageSentEvent:
		return "agent.thread_message_sent"
	case kha.SessionEventTypeAgentThreadContextCompactedEvent:
		return "agent.thread_context_compacted"
	case kha.SessionEventTypeAgentDeltaEvent:
		return "agent.delta"
	case kha.SessionEventTypeSessionStatusEvent:
		return "session.status"
	case kha.SessionEventTypeSessionErrorEvent:
		return "session.error"
	case kha.SessionEventTypeSessionThreadCreatedEvent:
		return "session.thread_created"
	case kha.SessionEventTypeSessionThreadStatusEvent:
		return "session.thread_status"
	case kha.SessionEventTypeSessionTitleUpdatedEvent:
		return "session.title_updated"
	case kha.SessionEventTypeSessionUpdatedEvent:
		return "session.updated"
	case kha.SessionEventTypeSessionUsageEvent:
		return "session.usage"
	default:
		return "unknown"
	}
}

// contentBlockDiscriminator names a non-text content block for its
// bracketed placeholder, with the same open-vocabulary fallback as
// eventDiscriminator.
func contentBlockDiscriminator(block *kha.ContentBlock) string {
	switch block.Type {
	case kha.ContentBlockTypeTextContentBlock:
		return "text"
	case kha.ContentBlockTypeThinkingContentBlock:
		return "thinking"
	case kha.ContentBlockTypeToolUseContentBlock:
		return "tool_use"
	case kha.ContentBlockTypeToolResultContentBlock:
		return "tool_result"
	case kha.ContentBlockTypeFileToolResultContent:
		return "file"
	default:
		return "unknown"
	}
}

// oneLine collapses s to its first summaryWidth runes on a single line.
func oneLine(s string, width int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) > width {
		s = string(runes[:width]) + "..."
	}
	return s
}

// stripEscapes removes terminal control characters and escape sequences from
// untrusted event text before it reaches a tty — the shared
// output.StripEscapes under the renderer's short local name. It runs on
// untrusted text only — the renderer's own SGR colors are applied after it,
// never through it.
var stripEscapes = output.StripEscapes
