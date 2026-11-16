package commands

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestSessionsEventsWatchTransform(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "compact json per event",
			args: []string{"--transform", "{id,type}", "-o", "json"},
			want: "{\"id\":\"evt_w1\",\"type\":\"user.message\"}\n" +
				"{\"id\":\"evt_w2\",\"type\":\"agent.message\"}\n" +
				"{\"id\":\"evt_w3\",\"type\":\"session.status\"}\n",
		},
		{
			name: "generic table per event",
			args: []string{"--transform", "{id,type}"},
			want: "id      type\nevt_w1  user.message\n" +
				"id      type\nevt_w2  agent.message\n" +
				"id      type\nevt_w3  session.status\n",
		},
		{
			name: "yaml document per event",
			args: []string{"--transform", "id", "-o", "yaml"},
			want: "---\nevt_w1\n---\nevt_w2\n---\nevt_w3\n",
		},
		{
			name: "raw string per event",
			args: []string{"--transform", "id", "-r"},
			want: "evt_w1\nevt_w2\nevt_w3\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateEnv(t)
			srv := serve(t, sessionStreamHandler(watchTestFrames))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			out := &cancelOnMarker{buf: new(bytes.Buffer), marker: "evt_w3", cancel: cancel}
			args := append([]string{"sessions", "events", "sess_1", "--watch"}, tc.args...)
			args = append(args, "--base-url", srv.URL, "--api-key", "sk-test")

			stderr, err := runCmdCtx(t, ctx, out, args...)
			if !errors.Is(err, errInterrupted) {
				t.Fatalf("events --watch: err = %v, want errInterrupted (stderr: %s)", err, stderr)
			}
			if got := out.buf.String(); got != tc.want {
				t.Errorf("stdout = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSessionsEventsWatchRawOutputMatchesJSON(t *testing.T) {
	isolateEnv(t)
	srv := serve(t, sessionStreamHandler(watchTestFrames))
	run := func(flags ...string) string {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		out := &cancelOnMarker{buf: new(bytes.Buffer), marker: "evt_w3", cancel: cancel}
		args := append([]string{"sessions", "events", "sess_1", "--watch"}, flags...)
		args = append(args, "--base-url", srv.URL, "--api-key", "sk-test")
		stderr, err := runCmdCtx(t, ctx, out, args...)
		if !errors.Is(err, errInterrupted) {
			t.Fatalf("events --watch: err = %v, want errInterrupted (stderr: %s)", err, stderr)
		}
		return out.buf.String()
	}

	jsonOut := run("-o", "json")
	rawOut := run("-r")
	if rawOut != jsonOut {
		t.Errorf("raw-only output = %q, want the JSON stream %q", rawOut, jsonOut)
	}
}
