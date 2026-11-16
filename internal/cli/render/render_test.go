package render

import (
	"bytes"
	"strings"
	"testing"

	"github.com/MoonshotAI/kha"

	json "encoding/json/v2"
)

func event(t *testing.T, doc string) *kha.SessionEvent {
	t.Helper()
	ev := new(kha.SessionEvent)
	if err := json.Unmarshal([]byte(doc), ev); err != nil {
		t.Fatalf("decoding event: %v", err)
	}
	return ev
}

func TestDeltaStreamThenMessageDoesNotDuplicate(t *testing.T) {
	var out, errOut bytes.Buffer
	r := New(&out, &errOut, false)

	r.Event(event(t, `{"id":"e1","processed_at":"2026-08-01T10:00:00Z","type":"agent.delta","data":{"kind":"text","delta":"Hello"}}`))
	r.Event(event(t, `{"id":"e2","processed_at":"2026-08-01T10:00:01Z","type":"agent.delta","data":{"kind":"text","delta":" world"}}`))
	// The complete message arrives after its deltas: the line is closed, not reprinted.
	r.Event(event(t, `{"id":"e3","processed_at":"2026-08-01T10:00:02Z","type":"agent.message","data":{"content":[{"type":"text","text":"Hello world"}]}}`))
	r.Close()

	got := out.String()
	if got != "Hello world\n" {
		t.Fatalf("out = %q, want the streamed text closed by one newline", got)
	}
}

// TestPlainReconnectMessageSuffixPrints: deltas streamed, the stream
// dropped WITHOUT an expired-cursor recovery (no Recover call), and the
// re-delivered authoritative agent.message carries MORE than the fragments
// showed. The prefix comparison must print the unseen suffix, not swallow
// the whole message.
func TestPlainReconnectMessageSuffixPrints(t *testing.T) {
	var out, errOut bytes.Buffer
	r := New(&out, &errOut, false)

	r.Event(event(t, `{"id":"e1","processed_at":"2026-08-01T10:00:00Z","type":"agent.delta","data":{"kind":"text","delta":"Hello wor"}}`))
	// The stream drops and reconnects (no recovery: Recover is NOT called);
	// the authoritative message arrives with the finalized tail.
	r.Event(event(t, `{"id":"e2","processed_at":"2026-08-01T10:00:02Z","type":"agent.message","data":{"content":[{"type":"text","text":"Hello world — final"}]}}`))
	r.Close()

	if got, want := out.String(), "Hello world — final\n"; got != want {
		t.Fatalf("out = %q, want %q (the suffix beyond the streamed fragments must print, not be swallowed)", got, want)
	}
	// A second message without its own deltas prints in full afterwards.
	r.Event(event(t, `{"id":"e3","processed_at":"2026-08-01T10:00:03Z","type":"agent.message","data":{"content":[{"type":"text","text":"next answer"}]}}`))
	if got := out.String(); !strings.HasSuffix(got, "next answer\n") {
		t.Fatalf("out = %q, want the follow-up message printed in full", got)
	}
}

// TestDivergedMessageReprintsInFull covers the other prefix-comparison
// branch: the authoritative message disagrees with what the deltas showed
// (the server rewrote the turn), so the streamed preview closes and the
// message prints in full.
func TestDivergedMessageReprintsInFull(t *testing.T) {
	var out, errOut bytes.Buffer
	r := New(&out, &errOut, false)

	r.Event(event(t, `{"id":"e1","processed_at":"2026-08-01T10:00:00Z","type":"agent.delta","data":{"kind":"text","delta":"Hello wor"}}`))
	r.Event(event(t, `{"id":"e2","processed_at":"2026-08-01T10:00:02Z","type":"agent.message","data":{"content":[{"type":"text","text":"Goodbye"}]}}`))
	r.Close()

	if got, want := out.String(), "Hello wor\nGoodbye\n"; got != want {
		t.Fatalf("out = %q, want %q (preview closed, diverging message reprinted in full)", got, want)
	}
}

func TestAgentMessageWithoutDeltasPrintsBody(t *testing.T) {
	var out, errOut bytes.Buffer
	r := New(&out, &errOut, false)
	r.Event(event(t, `{"id":"e1","processed_at":"2026-08-01T10:00:00Z","type":"agent.message","data":{"content":[{"type":"text","text":"full body"}]}}`))
	if got := out.String(); got != "full body\n" {
		t.Fatalf("out = %q", got)
	}
}

// TestRecoverReprintsAuthoritativeMessage covers the WithOnRecovery wiring:
// after deltas streamed and the stream then recovered from an expired
// cursor, the partial preview is discarded — the complete agent.message of
// the same turn must print in full instead of being swallowed as
// already-streamed, and a note on errOut marks the jump.
func TestRecoverReprintsAuthoritativeMessage(t *testing.T) {
	var out, errOut bytes.Buffer
	r := New(&out, &errOut, false)

	r.Event(event(t, `{"id":"e1","processed_at":"2026-08-01T10:00:00Z","type":"agent.delta","data":{"kind":"text","delta":"Hello wor"}}`))
	// The cursor expires mid-turn; the SDK recovers and calls Recover.
	r.Recover()
	// The authoritative message for the same turn arrives after re-anchor.
	r.Event(event(t, `{"id":"e2","processed_at":"2026-08-01T10:00:02Z","type":"agent.message","data":{"content":[{"type":"text","text":"Hello world — final"}]}}`))
	r.Close()

	if got, want := out.String(), "Hello wor\nHello world — final\n"; got != want {
		t.Errorf("out = %q, want %q (partial preview closed, authoritative message reprinted in full)", got, want)
	}
	if got := errOut.String(); !strings.Contains(got, "note: stream recovered; partial deltas discarded") {
		t.Errorf("errOut = %q, want the recovery note", got)
	}
}

func TestToolLinesAndStatusRouting(t *testing.T) {
	var out, errOut bytes.Buffer
	r := New(&out, &errOut, false)

	r.Event(event(t, `{"id":"e1","processed_at":"2026-08-01T10:00:00Z","type":"agent.tool_use","data":{"tool_use":{"id":"t1","name":"shell","input":{}},"evaluated_permission":"always_allow"}}`))
	r.Event(event(t, `{"id":"e2","processed_at":"2026-08-01T10:00:01Z","type":"agent.tool_result","data":{"tool_result":{"tool_use_id":"t1","content":[],"is_error":false}}}`))
	r.Event(event(t, `{"id":"e3","processed_at":"2026-08-01T10:00:02Z","type":"session.status","data":{"status":"idle","stop_reason":{"type":"end_turn"}}}`))
	r.Event(event(t, `{"id":"e4","processed_at":"2026-08-01T10:00:03Z","type":"session.error","data":{"error_type":"rate_limited","message":"slow down"}}`))

	outGot := out.String()
	if !strings.Contains(outGot, "[tool] shell (always_allow)") {
		t.Errorf("tool_use line missing: %q", outGot)
	}
	if !strings.Contains(outGot, "[tool result] ok") {
		t.Errorf("tool_result line missing: %q", outGot)
	}
	errGot := errOut.String()
	if !strings.Contains(errGot, "[status] idle (stop: end_turn)") {
		t.Errorf("status line should go to errOut: %q", errGot)
	}
	if !strings.Contains(errGot, "[session error] rate_limited: slow down") {
		t.Errorf("session error should go to errOut: %q", errGot)
	}
	if strings.Contains(outGot, "[status]") || strings.Contains(outGot, "[session error]") {
		t.Errorf("status/error leaked to stdout: %q", outGot)
	}
}

// TestUnknownVariantRendersAsUnknownTrace: discriminators the pinned spec
// does not declare are an open vocabulary: the generated layer decodes them
// to a
// zero Type with the payload kept in Raw (never failing), and the renderer
// falls back to a one-line [unknown] trace. A known variant the renderer
// does not handle specially still prints its own one-line trace.
func TestUnknownVariantRendersAsUnknownTrace(t *testing.T) {
	ev := new(kha.SessionEvent)
	if err := json.Unmarshal([]byte(`{"id":"e9","processed_at":"2026-08-01T10:00:00Z","type":"agent.future_thing","data":{"x":1}}`), ev); err != nil {
		t.Fatalf("unknown discriminator must decode (open vocabulary): %v", err)
	}
	if ev.Type != 0 || len(ev.Raw) == 0 {
		t.Fatalf("unknown variant: Type = %v, Raw = %s, want a zero Type with the payload kept in Raw", ev.Type, ev.Raw)
	}

	var out, errOut bytes.Buffer
	r := New(&out, &errOut, false)
	r.Event(ev)
	if got := out.String(); !strings.Contains(got, "[unknown]") {
		t.Fatalf("out = %q, want the [unknown] trace of the unknown variant", got)
	}

	r.Event(event(t, `{"id":"e10","processed_at":"2026-08-01T10:00:01Z","type":"agent.model_started"}`))
	if got := out.String(); !strings.Contains(got, "[agent.model_started]") {
		t.Fatalf("out = %q, want the one-line trace of the known variant", got)
	}
}

// TestToolResultArtifactSuffix: artifact deliveries are read from
// structured_content (user-facing), never from the model-facing content —
// an artifact block inside content alone must NOT produce the suffix.
func TestToolResultArtifactSuffix(t *testing.T) {
	var out, errOut bytes.Buffer
	r := New(&out, &errOut, false)
	r.Event(event(t, `{"id":"e1","processed_at":"2026-08-01T10:00:00Z","type":"agent.tool_result","data":{"tool_result":{"tool_use_id":"t1","structured_content":[{"type":"artifact","name":"report","version":"3"}],"content":[],"is_error":false}}}`))
	if got := out.String(); !strings.Contains(got, "[tool result] ok -> artifact report (v3)") {
		t.Fatalf("artifact suffix from structured_content missing: %q", got)
	}

	var out2, errOut2 bytes.Buffer
	r2 := New(&out2, &errOut2, false)
	r2.Event(event(t, `{"id":"e2","processed_at":"2026-08-01T10:00:01Z","type":"agent.tool_result","data":{"tool_result":{"tool_use_id":"t1","structured_content":[],"content":[{"type":"text","text":"done"}],"is_error":false}}}`))
	if got := out2.String(); !strings.Contains(got, "[tool result] ok\n") || strings.Contains(got, "artifact") {
		t.Fatalf("no suffix expected without structured_content cards: %q", got)
	}
}

func TestColorEscapesWhenEnabled(t *testing.T) {
	var out, errOut bytes.Buffer
	r := New(&out, &errOut, true)
	r.Event(event(t, `{"id":"e1","processed_at":"2026-08-01T10:00:00Z","type":"agent.thinking","data":{"content":[{"type":"text","text":"hmm"}]}}`))
	r.Event(event(t, `{"id":"e2","processed_at":"2026-08-01T10:00:01Z","type":"session.error","data":{"error_type":"timeout","message":"too slow"}}`))
	if got := out.String(); !strings.Contains(got, "\x1b[2m") || !strings.Contains(got, "(thinking) hmm") {
		t.Fatalf("colored thinking line = %q", got)
	}
	if got := errOut.String(); !strings.Contains(got, "\x1b[31m") {
		t.Fatalf("session error should be red: %q", got)
	}
}

func TestNoColorPlainText(t *testing.T) {
	var out, errOut bytes.Buffer
	r := New(&out, &errOut, false)
	r.Event(event(t, `{"id":"e1","processed_at":"2026-08-01T10:00:00Z","type":"agent.thinking","data":{"content":[{"type":"text","text":"hmm"}]}}`))
	if got := out.String(); strings.Contains(got, "\x1b[") {
		t.Fatalf("color disabled but escape found: %q", got)
	}
}

// TestEventTextStripsTerminalEscapes: event text carrying ANSI cursor
// control or OSC 52 (clipboard write) sequences is stripped before it
// reaches the terminal — in streamed deltas, message bodies, tool names,
// and the stderr lines (title, session error).
func TestEventTextStripsTerminalEscapes(t *testing.T) {
	var out, errOut bytes.Buffer
	r := New(&out, &errOut, true) // colors on: only the renderer's own SGR may survive

	// OSC 52 with BEL terminator + a clear-screen CSI inside a streamed delta.
	r.Event(event(t, `{"id":"e1","processed_at":"2026-08-01T10:00:00Z","type":"agent.delta","data":{"kind":"text","delta":"hi\u001b]52;c;aGVsbG8=\u0007\u001b[2J\u001b[1;1Hbye"}}`))
	// Escape-carrying tool name.
	r.Event(event(t, `{"id":"e3","processed_at":"2026-08-01T10:00:02Z","type":"agent.tool_use","data":{"tool_use":{"id":"t1","name":"sh\u001b[6nell","input":{}},"evaluated_permission":"always_allow"}}`))
	// Title and session error go to errOut.
	r.Event(event(t, `{"id":"e4","processed_at":"2026-08-01T10:00:03Z","type":"session.title_updated","data":{"title":"t\u001b[2Jitle"}}`))
	r.Event(event(t, `{"id":"e5","processed_at":"2026-08-01T10:00:04Z","type":"session.error","data":{"error_type":"boom","message":"bad \u001b]52;c;eHA=\u0007news"}}`))
	r.Close()

	// A complete message body (a separate renderer: after e1's deltas the
	// first renderer would swallow the message as already-streamed, by
	// design). OSC with an ST terminator inside the text.
	var out2, errOut2 bytes.Buffer
	r2 := New(&out2, &errOut2, false)
	r2.Event(event(t, `{"id":"e2","processed_at":"2026-08-01T10:00:01Z","type":"agent.message","data":{"content":[{"type":"text","text":"safe\u001b]0;pwned\u001b\\ text"}]}}`))
	if got := out2.String(); got != "safe text\n" {
		t.Errorf("message body = %q, want %q", got, "safe text\n")
	}

	outGot := out.String()
	for _, evil := range []string{"\x1b]", "\a", "\x1b[2J", "\x1b[1;1H", "\x1b[6n"} {
		if strings.Contains(outGot, evil) {
			t.Errorf("foreign escape %q leaked to stdout: %q", evil, outGot)
		}
	}
	for _, want := range []string{"hibye", "shell"} {
		if !strings.Contains(outGot, want) {
			t.Errorf("stdout missing stripped text %q:\n%q", want, outGot)
		}
	}
	errGot := errOut.String()
	for _, evil := range []string{"\x1b]", "\a", "\x1b[2J"} {
		if strings.Contains(errGot, evil) {
			t.Errorf("foreign escape %q leaked to stderr: %q", evil, errGot)
		}
	}
	for _, want := range []string{"title", "bad news"} {
		if !strings.Contains(errGot, want) {
			t.Errorf("stderr missing stripped text %q:\n%q", want, errGot)
		}
	}

	// The Summary path (sessions events table) strips too.
	sum := Summary(event(t, `{"id":"e6","processed_at":"2026-08-01T10:00:05Z","type":"session.title_updated","data":{"title":"x\u001b[2Jy"}}`))
	if strings.Contains(sum, "\x1b") || sum != "[title] xy" {
		t.Errorf("Summary = %q, want %q", sum, "[title] xy")
	}
}

func TestStripEscapes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain text", "plain text"},
		{"keep\nnewline", "keep\nnewline"},
		{"tab\tdropped", "tabdropped"},
		{"cr\rdropped", "crdropped"},
		{"del\x7fdropped", "deldropped"},
		{"c1\xc2\x85dropped", "c1dropped"},                                 // U+0085 NEL (two-byte UTF-8)
		{"\x1b[2Jcleared", "cleared"},                                      // CSI
		{"\x1b[1;31mred", "red"},                                           // CSI with params
		{"\x1b]52;c;aGk=\aosc", "osc"},                                     // OSC with BEL
		{"\x1b]0;t\x1b\\osc", "osc"},                                       // OSC with ST
		{"\x1bMtwo-byte", "two-byte"},                                      // two-character escape
		{"lone\x1b", "lone"},                                               // trailing ESC
		{"中文 保留", "中文 保留"},                                                 // multibyte UTF-8 untouched
		{"\xe2\x80\x9cquote\xe2\x80\x9d", "\xe2\x80\x9cquote\xe2\x80\x9d"}, // U+201C/U+201D survive (continuation bytes ≥ 0x80)
	}
	for _, c := range cases {
		if got := stripEscapes(c.in); got != c.want {
			t.Errorf("stripEscapes(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
